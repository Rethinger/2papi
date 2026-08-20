package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestBuildValidatesAndHashes(t *testing.T) {
	s, err := Build(Config{Version: 1, Secret: "s", VirtualKeys: []VirtualKey{{Name: "k", Key: "secret", Models: []string{"m"}, RPM: 1}}, Models: []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}}, Accounts: []Account{{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.ModelsByAlias["m"].UpstreamModel != "u" {
		t.Fatal("missing model")
	}
	if s.KeyHashHex("k") == "" {
		t.Fatal("missing hash")
	}
}
func TestBuildRejectsUnknownAccount(t *testing.T) {
	_, err := Build(Config{Version: 1, Secret: "s", VirtualKeys: []VirtualKey{{Name: "k", Key: "secret"}}, Models: []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"missing"}}}})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestBuildAcceptsPreHashedVirtualKey(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte("secret"))
	want := hex.EncodeToString(mac.Sum(nil))
	s, err := Build(Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "k", KeyHash: want}},
		Models:      []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}},
		Accounts:    []Account{{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.KeyHashHex("k"); got != want {
		t.Fatalf("hash=%s, want %s", got, want)
	}

	_, err = Build(Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "k", KeyHash: "invalid"}},
		Models:      []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}},
		Accounts:    []Account{{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true}},
	})
	if err == nil {
		t.Fatal("invalid key_hash was accepted")
	}
}

func anthropicV2Config(cred Credential) Config {
	return Config{
		Version:     2,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "k", Key: "secret", Models: []string{"m"}, RPM: 1}},
		Models:      []Model{{Alias: "m", UpstreamModel: "claude-3-5-sonnet-20241022", Accounts: []string{"claude"}}},
		Accounts:    []Account{{ID: "a1", Name: "claude", Adapter: "anthropic", BaseURL: "https://claude.ai", Credential: cred, Enabled: true}},
	}
}

func TestBuildAnthropicCredentialKinds(t *testing.T) {
	if _, err := Build(anthropicV2Config(Credential{Kind: "api_key", APIKey: "sk-ant-x", Revision: 1})); err != nil {
		t.Fatalf("api_key kind: %v", err)
	}
	if _, err := Build(anthropicV2Config(Credential{Kind: "oauth", AccessToken: "sk-ant-oauth", Revision: 1})); err != nil {
		t.Fatalf("oauth kind: %v", err)
	}
	if _, err := Build(anthropicV2Config(Credential{Kind: "cookie", Cookies: "sessionKey=sk-ant-x", OrganizationID: "org-1", Revision: 1})); err != nil {
		t.Fatalf("cookie kind: %v", err)
	}

	for _, bad := range []Credential{
		{Kind: "api_key", Revision: 1},                 // missing api_key
		{Kind: "oauth", Revision: 1},                   // missing access_token
		{Kind: "cookie", Revision: 1},                  // missing cookies
		{Kind: "token", AccessToken: "x", Revision: 1}, // unsupported kind
	} {
		if _, err := Build(anthropicV2Config(bad)); err == nil {
			t.Fatalf("credential %+v accepted", bad)
		}
	}
}

func TestBuildOptimizationFlag(t *testing.T) {	cfg := anthropicV2Config(Credential{Kind: "api_key", APIKey: "sk-ant-x", Revision: 1})
	cfg.Optimization = Optimization{RTKCompression: true, Caveman: true, Headroom: true, HeadroomReserve: 50000, HeadroomKeep: 4}
	s, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Optimization.RTKCompression || !s.Optimization.Caveman || !s.Optimization.Headroom {
		t.Fatal("optimization flags lost in snapshot")
	}
	if s.Optimization.HeadroomReserve != 50000 || s.Optimization.HeadroomKeep != 4 {
		t.Fatalf("headroom fields lost: %+v", s.Optimization)
	}
	// per-model and per-key overrides
	cfg2 := anthropicV2Config(Credential{Kind: "api_key", APIKey: "sk-ant-x", Revision: 1})
	cfg2.Models[0].Optimization = &Optimization{RTKCompression: true}
	cfg2.VirtualKeys[0].Optimization = &Optimization{Caveman: true}
	s2, err := Build(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.ModelsByAlias["m"].Optimization.RTKCompression {
		t.Fatal("per-model optimization lost")
	}
	if !s2.VirtualKeysByName["k"].Optimization.Caveman {
		t.Fatal("per-key optimization lost")
	}
}

func v2cfg(adapter string, cred Credential) Config {
	acct := Account{ID: "a1", Name: "acct", Adapter: adapter, BaseURL: "https://provider.example", Credential: cred, Enabled: true}
	return Config{
		Version:     2,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "k", Key: "secret", Models: []string{"m"}, RPM: 1}},
		Models:      []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"acct"}}},
		Accounts:    []Account{acct},
	}
}

func TestBuildThirdpartyFreeAccounts(t *testing.T) {
	// free providers accept kind=free with no secret
	for _, adapter := range []string{"opencode", "felo", "qoder", "free", "openai-compatible", "kimi"} {
		if _, err := Build(v2cfg(adapter, Credential{Kind: "free", Revision: 1})); err != nil {
			t.Fatalf("%s free account: %v", adapter, err)
		}
	}
	// oauth providers accept oauth token
	for _, adapter := range []string{"cursor", "copilot", "kimi"} {
		if _, err := Build(v2cfg(adapter, Credential{Kind: "oauth", AccessToken: "at", Revision: 1})); err != nil {
			t.Fatalf("%s oauth account: %v", adapter, err)
		}
		// missing token rejected
		if _, err := Build(v2cfg(adapter, Credential{Kind: "oauth", Revision: 1})); err == nil {
			t.Fatalf("%s oauth missing token accepted", adapter)
		}
	}
}

func baseV2Config() Config {
	return anthropicV2Config(Credential{Kind: "api_key", APIKey: "sk-ant-x", Revision: 1})
}

func TestBuildParsesAccountProxy(t *testing.T) {
	cfg := baseV2Config()
	cfg.Accounts[0].Proxy = "host-a:8080\nsocks5://user:pass@host-b:1080"
	s, err := Build(cfg)
	if err != nil {
		t.Fatalf("valid account proxy: %v", err)
	}
	entries, ok := s.AccountProxies["claude"]
	if !ok || len(entries) != 2 {
		t.Fatalf("AccountProxies = %v", s.AccountProxies)
	}
	if entries[0].Scheme != "http" || entries[0].Port != 8080 {
		t.Errorf("entry[0] = %+v", entries[0])
	}
	if entries[1].Scheme != "socks5" || entries[1].User != "user" {
		t.Errorf("entry[1] = %+v", entries[1])
	}
	// No proxy configured → account absent from the map.
	cfg2 := baseV2Config()
	s2, err := Build(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.AccountProxies["claude"]; ok {
		t.Fatal("account without proxy should not be in AccountProxies")
	}
	if len(s2.GlobalProxies) != 0 {
		t.Fatal("empty global pool should stay empty")
	}
}

func TestBuildRejectsInvalidAccountProxy(t *testing.T) {
	cfg := baseV2Config()
	cfg.Accounts[0].Proxy = "host-a:1\nbad entry!!"
	if _, err := Build(cfg); err == nil {
		t.Fatal("invalid account proxy accepted")
	}
}

func TestBuildParsesGlobalProxyPool(t *testing.T) {
	cfg := baseV2Config()
	cfg.Proxies = []string{"http://g1:8080", "socks4a://g2:1080", "g3:3128"}
	s, err := Build(cfg)
	if err != nil {
		t.Fatalf("valid global pool: %v", err)
	}
	if len(s.GlobalProxies) != 3 {
		t.Fatalf("GlobalProxies = %d entries, want 3", len(s.GlobalProxies))
	}
	if s.GlobalProxies[2].Scheme != "http" || s.GlobalProxies[2].Port != 3128 {
		t.Errorf("entry[2] = %+v", s.GlobalProxies[2])
	}
}

func TestBuildRejectsInvalidGlobalProxyPool(t *testing.T) {
	cfg := baseV2Config()
	cfg.Proxies = []string{"http://ok:1", "socks6://nope:2"}
	_, err := Build(cfg)
	if err == nil {
		t.Fatal("invalid global pool accepted")
	}
}
