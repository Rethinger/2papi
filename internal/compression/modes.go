package compression

import "github.com/Rethinger/2papi/internal/config"

// Mode presets for the three request optimizations. Empty string anywhere in
// the cascade means "not set" and falls through to the next level; the final
// empty result maps to the legacy behavior (standard/full/balanced).
//
// Cascade priority (same as the boolean toggles): runtime header >
// per-virtual-key > per-model > global config. Headers accept true/false
// plus explicit mode names; unknown header values are ignored.

const (
	ModeLight        = "light"
	ModeStandard     = "standard"
	ModeAggressive   = "aggressive"
	ModeConservative = "conservative"
	ModeBalanced     = "balanced"
	ModeLite         = "lite"
	ModeFull         = "full"
	ModeAuto         = "auto" // resolved per-request later (see auto.go)
)

// Decision is the outcome of resolving one optimization for a request.
// Run=false means skip entirely. Mode is concrete ("light"/"standard"/...)
// or "auto"; auto is refined against the request body before execution.
type Decision struct {
	Run  bool
	Mode string
}

var validHeaderModes = map[string]bool{
	ModeLight: true, ModeStandard: true, ModeAggressive: true,
	ModeLite: true, ModeFull: true,
	ModeConservative: true, ModeBalanced: true,
	ModeAuto: true,
}

// headerDecision interprets an override header: "true" enables with the
// legacy default mode, "false" disables, a mode name selects it, anything
// else is ignored (returns nil so the cascade continues).
func headerDecision(header, defaultMode string) *Decision {
	switch header {
	case "true":
		return &Decision{Run: true, Mode: defaultMode}
	case "false":
		return &Decision{Run: false}
	case "":
		return nil
	default:
		if validHeaderModes[header] {
			return &Decision{Run: true, Mode: header}
		}
		return nil
	}
}

func decideLevel(o *config.Optimization, modeField string, legacyDefault string) *Decision {
	if o == nil {
		return nil
	}
	mode := ""
	switch modeField {
	case "rtk":
		mode = o.RTKMode
		if mode == "" && o.RTKCompression {
			mode = legacyDefault
		}
	case "caveman":
		mode = o.CavemanMode
		if mode == "" && o.Caveman {
			mode = legacyDefault
		}
	default:
		return nil
	}
	if mode == "" {
		return nil // level present but nothing set: inherit from the next one
	}
	return &Decision{Run: true, Mode: mode}
}

// DecideRTK resolves RTK compression for a request. The returned mode is
// concrete unless every configured layer chose "auto".
func DecideRTK(global, model, vk *config.Optimization, header string) Decision {
	if d := headerDecision(header, ModeStandard); d != nil {
		return *d
	}
	for _, o := range []*config.Optimization{vk, model, global} {
		if d := decideLevel(o, "rtk", ModeStandard); d != nil {
			return *d
		}
	}
	return Decision{Run: false}
}

// DecideCaveman resolves the caveman directive injection.
func DecideCaveman(global, model, vk *config.Optimization, header string) Decision {
	if d := headerDecision(header, ModeFull); d != nil {
		return *d
	}
	for _, o := range []*config.Optimization{vk, model, global} {
		if d := decideLevel(o, "caveman", ModeFull); d != nil {
			return *d
		}
	}
	return Decision{Run: false}
}

// ProfileParams are the concrete headroom parameters a profile maps to.
type ProfileParams struct {
	Profile string
	Keep    int
	Reserve int
}

// RTKParamsFor maps an RTK mode preset to concrete compression parameters
// (see compression.go for how they are applied). Empty/unknown → standard.
func RTKParamsFor(mode string) RTKParams {
	switch mode {
	case ModeLight:
		return RTKParams{MinBytes: 8192, HeadLines: 40, TailLines: 40, CommandAware: false}
	case ModeAggressive:
		return RTKParams{MinBytes: 1024, HeadLines: 8, TailLines: 8, CommandAware: true}
	default:
		return StandardRTKParams()
	}
}

// HeadroomProfileParams maps a profile name to concrete pruning parameters.
// Explicit reserve/keep overrides replace the profile defaults when non-zero.
func HeadroomProfileParams(profile string, reserveOverride, keepOverride int) ProfileParams {
	p := ProfileParams{Profile: profile, Keep: DefaultHeadroomKeep, Reserve: DefaultHeadroomReserve}
	switch profile {
	case ModeConservative:
		p.Keep, p.Reserve = 16, 150_000
	case ModeAggressive:
		p.Keep, p.Reserve = 4, 80_000
	}
	if keepOverride > 0 {
		p.Keep = keepOverride
	}
	if reserveOverride > 0 {
		p.Reserve = reserveOverride
	}
	return p
}

func isHeadroomProfile(mode string) bool {
	return mode == ModeConservative || mode == ModeBalanced || mode == ModeAggressive || mode == ModeAuto
}

func decideHeadroomLevel(o *config.Optimization) *ProfileParams {
	if o == nil {
		return nil
	}
	if o.HeadroomProfile != "" {
		p := HeadroomProfileParams(o.HeadroomProfile, o.HeadroomReserve, o.HeadroomKeep)
		return &p
	}
	if o.Headroom {
		reserve, keep := o.HeadroomReserve, o.HeadroomKeep
		if reserve <= 0 {
			reserve = DefaultHeadroomReserve
		}
		if keep <= 0 {
			keep = DefaultHeadroomKeep
		}
		return &ProfileParams{Profile: ModeBalanced, Keep: keep, Reserve: reserve}
	}
	return nil
}

// DecideHeadroom resolves headroom pruning: whether it runs, which profile
// applies and the concrete keep/reserve parameters.
func DecideHeadroom(global, model, vk *config.Optimization, header string) (bool, string, int, int) {
	if header == "true" {
		return true, ModeBalanced, DefaultHeadroomReserve, DefaultHeadroomKeep
	}
	if header == "false" {
		return false, "", 0, 0
	}
	if h := headerDecision(header, ""); h != nil && h.Run && isHeadroomProfile(h.Mode) {
		p := HeadroomProfileParams(h.Mode, 0, 0)
		return true, p.Profile, p.Reserve, p.Keep
	}
	for _, o := range []*config.Optimization{vk, model, global} {
		if p := decideHeadroomLevel(o); p != nil {
			return true, p.Profile, p.Reserve, p.Keep
		}
	}
	return false, "", 0, 0
}
