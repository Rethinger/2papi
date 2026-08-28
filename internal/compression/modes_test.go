package compression

import (
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
)

func TestRTKParamsForPresets(t *testing.T) {
	light := RTKParamsFor(ModeLight)
	if light.MinBytes != 8192 || light.HeadLines != 40 || light.TailLines != 40 || light.CommandAware {
		t.Fatalf("light preset wrong: %+v", light)
	}
	std := RTKParamsFor(ModeStandard)
	if std.MinBytes != MinCompressBytes || std.HeadLines != HeadLines || !std.CommandAware {
		t.Fatalf("standard preset wrong: %+v", std)
	}
	agg := RTKParamsFor(ModeAggressive)
	if agg.MinBytes != 1024 || agg.HeadLines != 8 || agg.TailLines != 8 || !agg.CommandAware {
		t.Fatalf("aggressive preset wrong: %+v", agg)
	}
	if RTKParamsFor("") != std {
		t.Fatalf("empty mode must map to standard")
	}
}

func opt(rtk, cav, hr string, rtkB, cavB bool) *config.Optimization {
	return &config.Optimization{RTKMode: rtk, CavemanMode: cav, HeadroomProfile: hr, RTKCompression: rtkB, Caveman: cavB}
}

func TestDecideRTKCascade(t *testing.T) {
	g := opt("", "", "", true, false)          // global: legacy bool
	m := opt("light", "", "", false, false)    // model: light
	vk := opt("aggressive", "", "", false, false) // vk wins

	if d := DecideRTK(g, m, vk, ""); d != (Decision{Run: true, Mode: ModeAggressive}) {
		t.Fatalf("vk must win: %+v", d)
	}
	if d := DecideRTK(g, m, nil, ""); d != (Decision{Run: true, Mode: ModeLight}) {
		t.Fatalf("model next: %+v", d)
	}
	if d := DecideRTK(g, nil, nil, ""); d != (Decision{Run: true, Mode: ModeStandard}) {
		t.Fatalf("global bool maps to standard: %+v", d)
	}
	if d := DecideRTK(nil, nil, nil, ""); d.Run {
		t.Fatalf("nothing configured: off")
	}
	// Header overrides everything, including explicit modes.
	if d := DecideRTK(g, m, vk, "false"); d.Run {
		t.Fatalf("header false disables")
	}
	if d := DecideRTK(nil, nil, nil, "true"); d != (Decision{Run: true, Mode: ModeStandard}) {
		t.Fatalf("header true = standard: %+v", d)
	}
	if d := DecideRTK(nil, nil, nil, "light"); d != (Decision{Run: true, Mode: ModeLight}) {
		t.Fatalf("header mode name: %+v", d)
	}
	if d := DecideRTK(g, m, vk, "garbage"); d != (Decision{Run: true, Mode: ModeAggressive}) {
		t.Fatalf("unknown header ignored, cascade holds: %+v", d)
	}
	// Level present but empty inherits to the next level.
	empty := &config.Optimization{}
	if d := DecideRTK(empty, g, nil, ""); d.Mode != ModeStandard {
		t.Fatalf("empty vk level inherits: %+v", d)
	}
}

func TestDecideCavemanCascade(t *testing.T) {
	g := opt("", "", "", false, true)
	vk := opt("", "lite", "", false, false)

	if d := DecideCaveman(g, nil, vk, ""); d != (Decision{Run: true, Mode: ModeLite}) {
		t.Fatalf("vk lite wins: %+v", d)
	}
	if d := DecideCaveman(g, nil, nil, ""); d != (Decision{Run: true, Mode: ModeFull}) {
		t.Fatalf("global bool maps to full: %+v", d)
	}
	if d := DecideCaveman(g, nil, nil, "false"); d.Run {
		t.Fatalf("header false disables")
	}
	if d := DecideCaveman(nil, nil, nil, "lite"); d != (Decision{Run: true, Mode: ModeLite}) {
		t.Fatalf("header lite: %+v", d)
	}
}

func TestDecideHeadroomProfiles(t *testing.T) {
	g := opt("", "", ModeAggressive, false, false)
	run, profile, reserve, keep := DecideHeadroom(g, nil, nil, "")
	if !run || profile != ModeAggressive || keep != 4 || reserve != 80_000 {
		t.Fatalf("aggressive profile wrong: run=%v %s %d/%d", run, profile, reserve, keep)
	}

	conservative := opt("", "", ModeConservative, false, false)
	_, profile, reserve, keep = DecideHeadroom(conservative, nil, nil, "")
	if profile != ModeConservative || keep != 16 || reserve != 150_000 {
		t.Fatalf("conservative wrong: %s %d/%d", profile, reserve, keep)
	}

	// Legacy bool + explicit numbers keep old semantics.
	legacy := &config.Optimization{Headroom: true, HeadroomReserve: 4000, HeadroomKeep: 6}
	_, profile, reserve, keep = DecideHeadroom(legacy, nil, nil, "")
	if profile != ModeBalanced || reserve != 4000 || keep != 6 {
		t.Fatalf("legacy params must hold: %s %d/%d", profile, reserve, keep)
	}

	// Profile + overrides: override wins where non-zero.
	overridden := opt("", "", ModeConservative, false, false)
	overridden.HeadroomKeep = 10
	_, _, _, keep = DecideHeadroom(overridden, nil, nil, "")
	if keep != 10 {
		t.Fatalf("explicit keep overrides profile: %d", keep)
	}

	// Header profile selects even without config; garbage header falls through.
	if ok, prof, _, _ := DecideHeadroom(nil, nil, nil, "conservative"); !ok || prof != ModeConservative {
		t.Fatalf("header conservative failed")
	}
	if ok, _, _, _ := DecideHeadroom(nil, nil, nil, "nonsense"); ok {
		t.Fatalf("garbage header must not enable")
	}
	if ok, _, _, _ := DecideHeadroom(nil, nil, nil, ""); ok {
		t.Fatalf("unconfigured = off")
	}
}

func TestDirectiveForModes(t *testing.T) {
	if DirectiveFor("") != CavemanDirective || DirectiveFor("full") != CavemanDirective {
		t.Fatalf("full/default must use the classic directive")
	}
	lite := DirectiveFor(ModeLite)
	if lite == CavemanDirective {
		t.Fatalf("lite must differ from full")
	}
	for _, safety := range []string{"security warnings", "irreversible-action", "multi-step"} {
		if !strings.Contains(lite, safety) {
			t.Fatalf("lite lost the safety clause %q", safety)
		}
	}
}

func TestCompressIsIdempotentOnElidedBlocks(t *testing.T) {
	p := RTKParams{MinBytes: 1, HeadLines: 2, TailLines: 2, CommandAware: true}
	var sb strings.Builder
	sb.WriteString(strings.Repeat("line one\n", 30))
	first, saved := compressText(sb.String(), p)
	if saved == 0 || !strings.Contains(first, elisionMarker) {
		t.Fatalf("first pass must compress with marker, got saved=%d", saved)
	}
	second, saved2 := compressText(first, p)
	if second != first || saved2 != 0 {
		t.Fatalf("second pass must be a no-op (cache stability), changed=%v saved=%d", second != first, saved2)
	}
}
