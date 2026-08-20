package quota

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rethinger/2papi/internal/config"
)

// Observation is a per-request quota signal collected from the upstream
// response (headers, codex credits) and local usage accounting.
type Observation struct {
	Account   string
	Kind      string // oauth | cookie | free | api_key
	Family    string // claude | codex | cursor | copilot | kimi | gemini | deepseek | opencode | free
	Used      int64
	Limit     int64 // 0 = unknown/unlimited
	ResetAt   time.Time
	Status    string // active | exhausted | free
	Source    string // header | api | local
	Timestamp time.Time
}

// AccountQuota is the aggregated live state for a single account.
type AccountQuota struct {
	Account  string            `json:"account"`
	Kind     string            `json:"kind"`
	Family   string            `json:"family"`
	Adapter  string            `json:"adapter"`
	Used     int64             `json:"used"`
	Limit    int64             `json:"limit"`
	Percent  int               `json:"percent"`
	ResetAt  *time.Time        `json:"reset_at,omitempty"`
	Status   string            `json:"status"`
	Enabled  bool              `json:"enabled"`
	SubQuota map[string]int64  `json:"sub_quota,omitempty"` // e.g. codex primary/secondary windows
}

type Tracked struct {
	mu         sync.RWMutex
	enabled    map[string]bool
	byAccount  map[string]*AccountQuota
	adapters   map[string]string // account -> adapter
	lastWeight map[string]int64
}

func New() *Tracked {
	return &Tracked{
		enabled:    map[string]bool{},
		byAccount:  map[string]*AccountQuota{},
		adapters:   map[string]string{},
		lastWeight: map[string]int64{},
	}
}

// Adopt syncs account presence/enabled state from a snapshot.
func (t *Tracked) Adopt(s *config.Snapshot) {
	if s == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := map[string]bool{}
	for _, a := range s.Accounts {
		seen[a.Name] = true
		t.adapters[a.Name] = a.Adapter
		if _, ok := t.byAccount[a.Name]; !ok {
			kind := "api_key"
			if a.Credential.Kind != "" {
				kind = a.Credential.Kind
			}
			family := familyFor(a.Adapter)
			t.byAccount[a.Name] = &AccountQuota{Account: a.Name, Kind: kind, Family: family, Adapter: a.Adapter, Status: "unknown"}
		}
		t.byAccount[a.Name].Enabled = a.Enabled
		t.byAccount[a.Name].Adapter = a.Adapter
	}
	for name := range t.byAccount {
		if !seen[name] {
			delete(t.byAccount, name)
		}
	}
}

// Observe records a quota signal for an account.
func (t *Tracked) Observe(o Observation) {
	if o.Account == "" {
		return
	}
	if o.Timestamp.IsZero() {
		o.Timestamp = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	aq, ok := t.byAccount[o.Account]
	if !ok {
		kind := o.Kind
		if kind == "" {
			kind = "api_key"
		}
		aq = &AccountQuota{Account: o.Account, Kind: kind, Family: familyFor(o.Family), Status: "unknown"}
		t.byAccount[o.Account] = aq
	}
	if o.Family != "" {
		aq.Family = o.Family
	}
	if o.Kind != "" {
		aq.Kind = o.Kind
	}
	if o.Used > 0 || o.Limit > 0 {
		aq.Used = o.Used
		aq.Limit = o.Limit
		aq.Percent = pct(o.Used, o.Limit)
	}
	if o.ResetAt.After(time.Now()) {
		aq.ResetAt = &o.ResetAt
	}
	if o.Status != "" {
		aq.Status = o.Status
	}
	if o.Source == "header" && (o.Used == 0 || o.Limit == 0) {
		// keep existing local estimate as fallback
	}
}

// ObserveRaw aggregates codex-style per-window percentages (primary/secondary).
func (t *Tracked) ObserveRaw(account string, kind string, family string, weights map[string]int64) {
	if account == "" || len(weights) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	aq, ok := t.byAccount[account]
	if !ok {
		aq = &AccountQuota{Account: account, Kind: kind, Family: family, Status: "unknown"}
		t.byAccount[account] = aq
	}
	aq.SubQuota = weights
	// percent = max of sub-windows (the binding one)
	maxPct := int64(0)
	for _, p := range weights {
		if p > maxPct {
			maxPct = p
		}
	}
	aq.Percent = int(maxPct)
	aq.Status = statusFor(int(maxPct))
}

// Guard is nil-safe read helper.
type Guard struct {
	AQ *AccountQuota
}

func (g Guard) Percent() int {
	if g.AQ == nil {
		return 0
	}
	return g.AQ.Percent
}

// List returns sorted per-account quota snapshot.
func (t *Tracked) List() []AccountQuota {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]AccountQuota, 0, len(t.byAccount))
	for _, aq := range t.byAccount {
		cp := *aq
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		// enabled first, then higher percent
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		return out[i].Percent > out[j].Percent
	})
	return out
}

// Summary returns the combined percent for the overview bar.
func (t *Tracked) Summary() (percent int, used, limit int64, active int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var totalUsed, totalLimit int64
	for _, aq := range t.byAccount {
		if !aq.Enabled || aq.Limit <= 0 {
			continue
		}
		totalUsed += aq.Used
		totalLimit += aq.Limit
		active++
	}
	if totalLimit <= 0 {
		return 0, totalUsed, totalLimit, active
	}
	return pct(totalUsed, totalLimit), totalUsed, totalLimit, active
}

func pct(used, limit int64) int {
	if limit <= 0 {
		return 0
	}
	p := int(used * 100 / limit)
	if p > 100 {
		return 100
	}
	return p
}

func statusFor(p int) string {
	switch {
	case p >= 100:
		return "exhausted"
	case p >= 90:
		return "critical"
	case p >= 70:
		return "warn"
	case p > 0:
		return "active"
	default:
		return "unknown"
	}
}

func familyFor(adapterOrFamily string) string {
	switch {
	case strings.Contains(adapterOrFamily, "claude"), strings.Contains(adapterOrFamily, "anthropic"):
		return "claude"
	case strings.Contains(adapterOrFamily, "codex"), strings.Contains(adapterOrFamily, "openai"):
		return "codex"
	case strings.Contains(adapterOrFamily, "cursor"):
		return "cursor"
	case strings.Contains(adapterOrFamily, "copilot"), strings.Contains(adapterOrFamily, "github"):
		return "copilot"
	case strings.Contains(adapterOrFamily, "kimi"):
		return "kimi"
	case strings.Contains(adapterOrFamily, "gemini"):
		return "gemini"
	case strings.Contains(adapterOrFamily, "deepseek"):
		return "deepseek"
	case strings.Contains(adapterOrFamily, "opencode"):
		return "free"
	case adapterOrFamily == "free":
		return "free"
	default:
		if adapterOrFamily == "" {
			return "unknown"
		}
		return adapterOrFamily
	}
}
