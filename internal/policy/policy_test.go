package policy

import (
	"net/http/httptest"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
)

func testSnapshot(t *testing.T) *config.Snapshot {
	t.Helper()
	s, err := config.Build(config.Config{
		Version: 1,
		Secret:  "secret",
		VirtualKeys: []config.VirtualKey{
			{Name: "limited", Key: "sk-limited", Models: []string{"model-a"}, RPM: 1},
			{Name: "all", Key: "sk-all", Models: []string{"*"}},
		},
		Models:   []config.Model{{Alias: "model-a", UpstreamModel: "upstream", Accounts: []string{"account"}}},
		Accounts: []config.Account{{Name: "account", BaseURL: "http://example.test", APIKey: "upstream-key", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthenticateAndModelPermissions(t *testing.T) {
	auth := New(testSnapshot(t))

	missing := httptest.NewRequest("GET", "/", nil)
	if _, ok := auth.Authenticate(missing); ok {
		t.Fatal("missing bearer token authenticated")
	}

	wrong := httptest.NewRequest("GET", "/", nil)
	wrong.Header.Set("Authorization", "Bearer wrong")
	if _, ok := auth.Authenticate(wrong); ok {
		t.Fatal("wrong bearer token authenticated")
	}

	valid := httptest.NewRequest("GET", "/", nil)
	valid.Header.Set("Authorization", "Bearer sk-limited")
	vk, ok := auth.Authenticate(valid)
	if !ok || vk.Name != "limited" {
		t.Fatalf("valid token rejected: ok=%v key=%+v", ok, vk)
	}
	if !Allows(vk, "model-a") || Allows(vk, "model-b") {
		t.Fatal("model allowlist was not enforced")
	}

	wildcard := config.VirtualKey{Models: []string{"*"}}
	if !Allows(wildcard, "anything") || !Allows(config.VirtualKey{}, "anything") {
		t.Fatal("wildcard or empty model allowlist should allow access")
	}
}

func TestRateLimitBucket(t *testing.T) {
	auth := New(testSnapshot(t))
	vk := config.VirtualKey{Name: "limited", RPM: 1}
	if !auth.Begin(vk).Allowed {
		t.Fatal("first request should consume initial token")
	}
	if result := auth.Begin(vk); result.Allowed || result.Reason != "rate_limited" {
		t.Fatalf("second immediate request should be rate limited: %+v", result)
	}
	if result := auth.Begin(config.VirtualKey{Name: "unlimited", RPM: 0}); !result.Allowed {
		t.Fatal("non-positive RPM should be unlimited")
	}
}

func TestBudgetBlocksWhenDailySpendReached(t *testing.T) {
	auth := New(testSnapshot(t))
	vk := config.VirtualKey{Name: "budgeted", RPM: 0, BudgetUSD: 1.0}
	if result := auth.Begin(vk); !result.Allowed || result.BudgetRemainingUSD != 1.0 {
		t.Fatalf("fresh budget should allow with full remaining: %+v", result)
	}
	auth.Finalize(vk, 0, 0.6, true)
	if result := auth.Begin(vk); !result.Allowed {
		t.Fatalf("partial spend should still allow: %+v", result)
	}
	auth.Finalize(vk, 0, 0.5, true)
	result := auth.Begin(vk)
	if result.Allowed || result.Reason != "budget_exceeded" {
		t.Fatalf("spend above budget should block: %+v", result)
	}
	if result.BudgetRemainingUSD >= 0 {
		t.Fatalf("overspend should be visible as negative remaining: %+v", result)
	}
}

func TestConcurrencySlotsGateAndRelease(t *testing.T) {
	auth := New(testSnapshot(t))
	vk := config.VirtualKey{Name: "conc", RPM: 0, MaxConcurrency: 1}
	if result := auth.Begin(vk); !result.Allowed || result.ConcurrencyRemaining != 0 {
		t.Fatalf("slot acquired, remaining should be zero: %+v", result)
	}
	if result := auth.Begin(vk); result.Allowed || result.Reason != "concurrency_limited" {
		t.Fatalf("second concurrent request should be limited: %+v", result)
	}
	auth.Finalize(vk, 0, 0, false)
	if result := auth.Begin(vk); !result.Allowed || result.ConcurrencyRemaining != 0 {
		t.Fatalf("slot should free after finalize: %+v", result)
	}
}

func TestTeamBudgetSharedAcrossKeys(t *testing.T) {
	auth := New(testSnapshot(t))
	team := &config.Team{ID: "team-1", BudgetUSD: 1.0}
	vk1 := config.VirtualKey{Name: "k1", RPM: 0, BudgetUSD: 0, Team: team}
	vk2 := config.VirtualKey{Name: "k2", RPM: 0, BudgetUSD: 0, Team: team}

	if result := auth.Begin(vk1); !result.Allowed || result.TeamBudgetRemainingUSD != 1.0 {
		t.Fatalf("fresh team budget should be full: %+v", result)
	}
	auth.Finalize(vk1, 0, 0.7, true)

	// Second key shares the same team budget
	if result := auth.Begin(vk2); !result.Allowed {
		t.Fatalf("partial team spend should still allow: %+v", result)
	}
	auth.Finalize(vk2, 0, 0.5, true)

	result := auth.Begin(vk1)
	if result.Allowed || result.Reason != "budget_exceeded" {
		t.Fatalf("team overspend should block: %+v", result)
	}
	if result.TeamBudgetRemainingUSD >= 0 {
		t.Fatalf("overspend should be visible as negative remaining: %+v", result)
	}
}

func TestFairShareCapsPerKeyWithinTeam(t *testing.T) {
	auth := New(testSnapshot(t))
	team := &config.Team{ID: "team-fair", BudgetUSD: 1.0, ShareUSD: 0.5}
	vk1 := config.VirtualKey{Name: "fair-1", RPM: 0, Team: team}
	vk2 := config.VirtualKey{Name: "fair-2", RPM: 0, Team: team}

	if result := auth.Begin(vk1); !result.Allowed {
		t.Fatalf("first fair-share request should pass: %+v", result)
	}
	auth.Finalize(vk1, 0, 0.5, true)

	// vk1 exhausted its 0.5 share -> blocked even though the team still has budget
	if result := auth.Begin(vk1); result.Allowed || result.Reason != "budget_exceeded" {
		t.Fatalf("vk1 should be capped by its fair share: %+v", result)
	}
	// vk2 still has its own full share
	if result := auth.Begin(vk2); !result.Allowed {
		t.Fatalf("vk2 should have its own share available: %+v", result)
	}
	auth.Finalize(vk2, 0, 0.5, true)
	// Team aggregate now exhausted
	if result := auth.Begin(vk2); result.Allowed {
		t.Fatalf("team aggregate should block after full spend: %+v", result)
	}
}

func TestTPMWindowRefillsAndDeferredCommit(t *testing.T) {
	auth := New(testSnapshot(t))
	vk := config.VirtualKey{Name: "tpm", RPM: 0, TPM: 100}
	if result := auth.Begin(vk); !result.Allowed || result.TPMRemaining != 100 {
		t.Fatalf("fresh token window should be full: %+v", result)
	}
	auth.Finalize(vk, 90, 0, true)
	if result := auth.Begin(vk); !result.Allowed {
		t.Fatalf("90 of 100 tokens leaves pre-check headroom: %+v", result)
	}
	auth.Finalize(vk, 60, 0, true)
	if result := auth.Begin(vk); result.Allowed || result.Reason != "rate_limited" {
		t.Fatalf("150 committed tokens should exhaust the 100 TPM window: %+v", result)
	}
}


func TestOrgBudgetCapsTeamBudget(t *testing.T) {
	auth := New(testSnapshot(t))
	org := &config.Org{ID: "org-1", BudgetUSD: 0.5}
	team := &config.Team{ID: "team-org", BudgetUSD: 1.0, Org: org}
	vk := config.VirtualKey{Name: "org-k1", RPM: 0, Team: team}

	if result := auth.Begin(vk); !result.Allowed || result.TeamBudgetRemainingUSD != 0.5 {
		t.Fatalf("team budget should be capped by the org budget: %+v", result)
	}
	auth.Finalize(vk, 0, 0.4, true)
	if result := auth.Begin(vk); !result.Allowed {
		t.Fatalf("spend under the org cap should pass: %+v", result)
	}
	auth.Finalize(vk, 0, 0.2, true)
	result := auth.Begin(vk)
	if result.Allowed || result.Reason != "budget_exceeded" {
		t.Fatalf("org cap must block at its own limit, not the team's: %+v", result)
	}
}

func TestOrgBudgetBoundsUnlimitedTeam(t *testing.T) {
	auth := New(testSnapshot(t))
	org := &config.Org{ID: "org-2", BudgetUSD: 0.3}
	// Team budget 0 (= unlimited): the org cap still applies.
	team := &config.Team{ID: "team-unl", BudgetUSD: 0, Org: org}
	vk := config.VirtualKey{Name: "unl-k1", RPM: 0, Team: team}

	if result := auth.Begin(vk); !result.Allowed || result.TeamBudgetRemainingUSD != 0.3 {
		t.Fatalf("org cap must bound an unlimited team: %+v", result)
	}
	auth.Finalize(vk, 0, 0.3, true)
	if result := auth.Begin(vk); result.Allowed || result.Reason != "budget_exceeded" {
		t.Fatalf("unlimited team must stop at the org cap: %+v", result)
	}
}

func TestOrgWithoutBudgetLeavesTeamUnlimited(t *testing.T) {
	auth := New(testSnapshot(t))
	org := &config.Org{ID: "org-3", BudgetUSD: 0}
	team := &config.Team{ID: "team-free", BudgetUSD: 0, Org: org}
	vk := config.VirtualKey{Name: "free-k1", RPM: 0, Team: team}

	result := auth.Begin(vk)
	if !result.Allowed || result.TeamBudgetRemainingUSD != 0 {
		t.Fatalf("org with no budget must not cap the team: %+v", result)
	}
	auth.Finalize(vk, 0, 5.0, true)
	if result := auth.Begin(vk); !result.Allowed {
		t.Fatalf("no budgets configured -> never blocks: %+v", result)
	}
}
