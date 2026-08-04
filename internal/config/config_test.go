package config

import "testing"

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
