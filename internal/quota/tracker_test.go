package quota

import (
	"strings"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"net/http"
)

func testSnapshot() *config.Snapshot {
	s, err := config.Build(config.Config{
		Version: 2,
		Secret:  "secret",
		VirtualKeys: []config.VirtualKey{{Name: "k", Key: "secret", RPM: 100}},
		Models:   []config.Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"claude-primary", "codex-primary"}}},
		Accounts: []config.Account{
			{ID: "c1", Name: "claude-primary", BaseURL: "https://claude.ai", Adapter: "anthropic", Credential: config.Credential{Kind: "oauth", AccessToken: "t", Revision: 1}, Enabled: true},
			{ID: "x1", Name: "codex-primary", BaseURL: "https://chatgpt.com", Adapter: "openai-codex", Credential: config.Credential{Kind: "oauth", AccessToken: "t", ChatGPTAccountID: "a", Revision: 1}, Enabled: true},
			{ID: "o1", Name: "opencode", BaseURL: "https://opencode.example", Adapter: "opencode", Credential: config.Credential{Kind: "free", Revision: 1}, Enabled: true},
		},
	})
	if err != nil {
		panic(err)
	}
	return s
}

func TestAdoptCreatesAccounts(t *testing.T) {
	tr := New()
	tr.Adopt(testSnapshot())
	got := tr.List()
	if len(got) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(got))
	}
	families := map[string]bool{}
	for _, aq := range got {
		families[aq.Family] = true
	}
	if !families["claude"] || !families["codex"] || !families["free"] {
		t.Fatalf("family mapping wrong: %+v", families)
	}
}

func TestObserveAndSummary(t *testing.T) {
	tr := New()
	tr.Adopt(testSnapshot())
	tr.Observe(Observation{
		Account: "codex-primary", Kind: "oauth", Family: "codex",
		Used: 120000, Limit: 200000, ResetAt: time.Now().Add(48 * time.Hour),
		Status: "active", Source: "local",
	})
	pct, used, limit, active := tr.Summary()
	if active != 1 { // only codex has a limit set
		t.Fatalf("active=%d (want 1 account with limit)", active)
	}
	_ = used
	_ = limit
	if pct != 60 {
		t.Fatalf("percent=%d want 60", pct)
	}
	if used != 120000 || limit != 200000 {
		t.Fatalf("used=%d limit=%d", used, limit)
	}
}

func TestObserveRawCodexWindows(t *testing.T) {
	tr := New()
	tr.Adopt(testSnapshot())
	tr.ObserveRaw("codex-primary", "oauth", "codex", map[string]int64{"primary": 34, "secondary": 12})
	list := tr.List()
	var found bool
	for _, aq := range list {
		if aq.Account == "codex-primary" {
			found = true
			if aq.Percent != 34 {
				t.Fatalf("percent=%d want max 34", aq.Percent)
			}
			if aq.Status != "active" {
				t.Fatalf("status=%s", aq.Status)
			}
		}
	}
	if !found {
		t.Fatal("codex account missing")
	}
}

func TestParseHeadersCodex(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-credits-primary-used-percent", "40")
	h.Set("x-codex-credits-secondary-used-percent", "10")
	h.Set("x-codex-credits-primary-reset-at", "2000000000")
	o, weights, ok := ParseHeaders(h)
	if !ok {
		t.Fatal("expected parsed")
	}
	if weights["primary"] != 40 || weights["secondary"] != 10 {
		t.Fatalf("weights=%v", weights)
	}
	if o.Used != 40 {
		t.Fatalf("used=%d", o.Used)
	}
	if o.ResetAt.IsZero() {
		t.Fatal("reset missing")
	}
}

func TestParseHeadersGeneric(t *testing.T) {
	h := http.Header{}
	h.Set("X-Provider-Quota-Used", "5000")
	h.Set("X-Provider-Quota-Limit", "10000")
	h.Set("X-Provider-Quota-ResetAt", "2026-08-25T00:00:00Z")
	o, _, ok := ParseHeaders(h)
	if !ok || o.Used != 5000 || o.Limit != 10000 || o.ResetAt.IsZero() {
		t.Fatalf("generic parse: %+v ok=%v", o, ok)
	}
}

func TestFamilyFor(t *testing.T) {
	cases := map[string]string{
		"anthropic":   "claude",
		"claudeai":    "claude",
		"openai-codex": "codex",
		"cursor":      "cursor",
		"kimi":        "kimi",
		"gemini":      "gemini",
		"deepseek":    "deepseek",
		"opencode":    "free",
		"free":        "free",
		"":            "unknown",
	}
	for in, want := range cases {
		if got := familyFor(in); got != want {
			t.Fatalf("familyFor(%q)=%s want %s", in, got, want)
		}
	}
}

func TestStatusThresholds(t *testing.T) {
	if s := statusFor(100); s != "exhausted" {
		t.Fatalf("100 -> %s", s)
	}
	if s := statusFor(92); s != "critical" {
		t.Fatalf("92 -> %s", s)
	}
	if s := statusFor(75); s != "warn" {
		t.Fatalf("75 -> %s", s)
	}
	if s := statusFor(30); s != "active" {
		t.Fatalf("30 -> %s", s)
	}
	_ = strings.TrimSpace("")
}
