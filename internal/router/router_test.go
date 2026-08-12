package router

import (
	"testing"
	"time"

	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/resilience"
)

func routerSnapshot(t *testing.T, strategy string, attempts int) *config.Snapshot {
	t.Helper()
	s, err := config.Build(config.Config{
		Version:     1,
		Secret:      "secret",
		VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk"}},
		Models:      []config.Model{{Alias: "model", UpstreamModel: "upstream", Accounts: []string{"primary", "secondary", "disabled"}}},
		Accounts: []config.Account{
			{Name: "primary", BaseURL: "http://primary.test", APIKey: "p", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 1, Cost: 2},
			{Name: "secondary", BaseURL: "http://secondary.test", APIKey: "s", Enabled: true, Priority: 1, Weight: 3, MaxConcurrency: 1, Cost: 1},
			{Name: "disabled", BaseURL: "http://disabled.test", APIKey: "d", Enabled: false, Priority: 0, Weight: 10, MaxConcurrency: 1, Cost: 0},
		},
		Routing:    config.Routing{Strategy: strategy, MaxAttempts: attempts, StickyTTL: "1h"},
		Resilience: config.Resilience{CircuitFailures: 1, CircuitReset: "1h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanStrategiesAndCandidateFiltering(t *testing.T) {
	tests := []struct {
		strategy string
		prepare  func(*resilience.State)
		want     string
	}{
		{strategy: "priority", want: "secondary"},
		{strategy: "fallback-chain", want: "secondary"},
		{strategy: "cheapest", want: "secondary"},
		{strategy: "quota-drain", want: "secondary"},
		{strategy: "balanced", want: "secondary"},
		{strategy: "fastest", prepare: func(s *resilience.State) {
			s.Success("primary", 10*time.Millisecond)
			s.Success("secondary", 100*time.Millisecond)
		}, want: "primary"},
	}

	for _, tc := range tests {
		t.Run(tc.strategy, func(t *testing.T) {
			snap := routerSnapshot(t, tc.strategy, 1)
			state := resilience.New()
			if tc.prepare != nil {
				tc.prepare(state)
			}
			plan, model := New(snap, state).Plan("model", "")
			if model.UpstreamModel != "upstream" || len(plan) != 1 || plan[0].Name != tc.want {
				t.Fatalf("plan=%+v model=%+v, want first=%s", plan, model, tc.want)
			}
		})
	}

	snap := routerSnapshot(t, "priority", 3)
	state := resilience.New()
	state.Cooldown("secondary", time.Hour)
	plan, _ := New(snap, state).Plan("model", "")
	if len(plan) != 1 || plan[0].Name != "primary" {
		t.Fatalf("cooling account was not filtered: %+v", plan)
	}

	state = resilience.New()
	state.Failure("secondary", 1)
	plan, _ = New(snap, state).Plan("model", "")
	if len(plan) != 1 || plan[0].Name != "primary" {
		t.Fatalf("open-circuit account was not filtered: %+v", plan)
	}

	state = resilience.New()
	if !state.TryAcquire("secondary", 1) {
		t.Fatal("failed to occupy concurrency slot")
	}
	plan, _ = New(snap, state).Plan("model", "")
	if len(plan) != 1 || plan[0].Name != "primary" {
		t.Fatalf("saturated account was not filtered: %+v", plan)
	}
}

func TestAffinityAndAffinityKey(t *testing.T) {
	snap := routerSnapshot(t, "priority", 2)
	r := New(snap, resilience.New())
	r.CommitAffinity("session", "primary")
	plan, _ := r.Plan("model", "session")
	if len(plan) != 2 || plan[0].Name != "primary" {
		t.Fatalf("affinity was not preferred: %+v", plan)
	}

	if got := AffinityKey("header", "user", "model", nil); got != "header" {
		t.Fatalf("header affinity=%q", got)
	}
	if got := AffinityKey("", "user", "model", map[string]any{"gateway_session": "metadata"}); got != "metadata" {
		t.Fatalf("metadata affinity=%q", got)
	}
	if a, b := AffinityKey("", "user", "model", nil), AffinityKey("", "user", "model", nil); a == "" || a != b {
		t.Fatalf("derived affinity should be stable and non-empty: %q %q", a, b)
	}
}

func TestProviderModelStrategies(t *testing.T) {
	snap := routerSnapshot(t, "priority", 2)
	model := snap.ModelsByAlias["model"]
	model.RoutingStrategy = "round_robin"
	snap.ModelsByAlias["model"] = model
	r := New(snap, resilience.New())
	r.CommitAffinity("same", "secondary")
	want := []string{"primary", "secondary", "primary", "secondary"}
	for i, expected := range want {
		plan, _ := r.Plan("model", "same")
		if len(plan) == 0 || plan[0].Name != expected {
			t.Fatalf("round robin %d=%+v want %s", i, plan, expected)
		}
	}

	model.RoutingStrategy = "quota_failover"
	snap.ModelsByAlias["model"] = model
	state := resilience.New()
	r = New(snap, state)
	r.CommitAffinity("same", "secondary")
	for i := 0; i < 2; i++ {
		plan, _ := r.Plan("model", "same")
		if len(plan) == 0 || plan[0].Name != "primary" {
			t.Fatalf("quota failover reordered before 429: %+v", plan)
		}
	}
	state.Cooldown("primary", time.Hour)
	plan, _ := r.Plan("model", "same")
	if len(plan) == 0 || plan[0].Name != "secondary" {
		t.Fatalf("cooling primary not skipped: %+v", plan)
	}
}
