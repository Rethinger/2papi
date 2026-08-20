package config

import "testing"

func validConfig() Config {
	return Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "vk", Key: "secret", Models: []string{"m"}, RPM: 100}},
		Models:      []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}},
		Accounts:    []Account{{Name: "a", BaseURL: "http://upstream", APIKey: "ak", Enabled: true}},
	}
}

func TestBuildV2CodexAccount(t *testing.T) {
	cfg := validConfig()
	cfg.Version = 2
	cfg.Accounts[0] = Account{
		ID:   "00000000-0000-0000-0000-000000000001",
		Name: "codex-main", Adapter: "openai-codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		Enabled: true, Weight: 1, MaxConcurrency: 3,
		Credential: Credential{Kind: "oauth", AccessToken: "at", RefreshToken: "rt", ChatGPTAccountID: "acct", Revision: 7},
	}
	cfg.Models[0].Accounts = []string{"codex-main"}
	snap, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.AccountsByName["codex-main"].Credential.Revision; got != 7 {
		t.Fatalf("revision=%d", got)
	}
}

func TestBuildV1NormalizesAPIKeyCredential(t *testing.T) {
	cfg := validConfig()
	cfg.Version = 1
	cfg.Accounts[0].APIKey = "legacy-key"
	snap, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.AccountsByName[cfg.Accounts[0].Name].Credential.APIKey; got != "legacy-key" {
		t.Fatalf("api key=%q", got)
	}
}

func TestBuildNormalizesAndValidatesPerModelRoutingStrategy(t *testing.T) {
	cfg := validConfig()
	cfg.Routing.Strategy = "priority"
	snap, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.ModelsByAlias["m"].RoutingStrategy; got != "priority" {
		t.Fatalf("inherited strategy=%q", got)
	}
	for _, strategy := range []string{"round_robin", "quota_failover"} {
		cfg.Models[0].RoutingStrategy = strategy
		if _, err := Build(cfg); err != nil {
			t.Fatalf("strategy %s rejected: %v", strategy, err)
		}
	}
	cfg.Models[0].RoutingStrategy = "random"
	if _, err := Build(cfg); err == nil {
		t.Fatal("invalid model strategy accepted")
	}
}
