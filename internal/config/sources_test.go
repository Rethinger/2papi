package config

import "testing"

func sourcesConfig(sources []ModelSource) Config {
	return Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []VirtualKey{{Name: "k", Key: "secret", Models: []string{"m"}, RPM: 1}},
		Accounts: []Account{
			{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true},
			{Name: "b", BaseURL: "http://y", APIKey: "ak", Enabled: true},
		},
		Models: []Model{
			{Alias: "m", UpstreamModel: "u", Accounts: []string{"a", "b"}, Sources: sources},
		},
	}
}

func TestUpstreamForPrefersSourceOverride(t *testing.T) {
	m := Model{
		Alias:         "fast",
		UpstreamModel: "default-upstream",
		Accounts:      []string{"a", "b"},
		Sources: []ModelSource{
			{Account: "b", UpstreamModel: "provider-b-model"},
		},
	}
	if got := m.UpstreamFor("a"); got != "default-upstream" {
		t.Fatalf("account without source should use alias default: got %q", got)
	}
	if got := m.UpstreamFor("b"); got != "provider-b-model" {
		t.Fatalf("source override should win: got %q", got)
	}
}

func TestResolvedForAppliesOverrideAndCosts(t *testing.T) {
	m := Model{
		Alias:             "fast",
		UpstreamModel:     "default-upstream",
		InputCostPerMtok:  1.0,
		OutputCostPerMtok: 2.0,
		Sources: []ModelSource{
			{Account: "b", UpstreamModel: "provider-b-model", InputCostPerMtok: 0.1, OutputCostPerMtok: 0.2},
			{Account: "c", InputCostPerMtok: 0.5},
		},
	}

	resolved := m.ResolvedFor("b")
	if resolved.UpstreamModel != "provider-b-model" || resolved.InputCostPerMtok != 0.1 || resolved.OutputCostPerMtok != 0.2 {
		t.Fatalf("full override expected: %+v", resolved)
	}
	partial := m.ResolvedFor("c")
	if partial.UpstreamModel != "default-upstream" || partial.InputCostPerMtok != 0.5 || partial.OutputCostPerMtok != 2.0 {
		t.Fatalf("partial override keeps alias defaults where unset: %+v", partial)
	}
	intact := m.ResolvedFor("a")
	if intact.UpstreamModel != "default-upstream" || intact.InputCostPerMtok != 1.0 {
		t.Fatalf("no source -> untouched copy: %+v", intact)
	}
	if m.UpstreamModel != "default-upstream" || m.Sources[0].UpstreamModel != "provider-b-model" {
		t.Fatalf("ResolvedFor mutated the receiver: %+v", m)
	}
}

func TestBuildRejectsBrokenSources(t *testing.T) {
	if _, err := Build(sourcesConfig([]ModelSource{{Account: "ghost"}})); err == nil {
		t.Fatal("unknown source account must be rejected")
	}
	if _, err := Build(sourcesConfig([]ModelSource{{Account: "a"}, {Account: "a"}})); err == nil {
		t.Fatal("duplicate sources for one account must be rejected")
	}
	if _, err := Build(sourcesConfig([]ModelSource{{Account: "a", Weight: -1}})); err == nil {
		t.Fatal("negative weight must be rejected")
	}
	if _, err := Build(sourcesConfig(nil)); err != nil {
		t.Fatalf("empty sources stay valid (legacy behavior): %v", err)
	}
}

func TestBuildKeepsSourcesInSnapshot(t *testing.T) {
	s, err := Build(sourcesConfig([]ModelSource{{Account: "b", UpstreamModel: "u-b", Weight: 3}}))
	if err != nil {
		t.Fatal(err)
	}
	got := s.ModelsByAlias["m"]
	if len(got.Sources) != 1 || got.Sources[0].Account != "b" || got.Sources[0].UpstreamModel != "u-b" {
		t.Fatalf("sources must survive into the snapshot: %+v", got.Sources)
	}
}
