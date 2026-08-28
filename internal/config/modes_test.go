package config

import (
	"strings"
	"testing"
)

func baseConfig() Config {
	return Config{
		Version:     1,
		Secret:      "s",
		VirtualKeys: []VirtualKey{{Name: "k", Key: "sk-test-123456"}},
		Accounts:    []Account{{Name: "a", BaseURL: "http://x", APIKey: "ak", Enabled: true}},
		Models:      []Model{{Alias: "m", UpstreamModel: "u", Accounts: []string{"a"}}},
	}
}

func TestBuildAcceptsValidOptimizationModes(t *testing.T) {
	c := baseConfig()
	c.Optimization = Optimization{RTKCompression: true, RTKMode: "aggressive", CavemanMode: "lite", HeadroomProfile: "auto"}
	if _, err := Build(c); err != nil {
		t.Fatalf("valid modes rejected: %v", err)
	}
	c.Models[0].Optimization = &Optimization{RTKMode: "light"}
	c.VirtualKeys[0].Optimization = &Optimization{HeadroomProfile: "conservative"}
	if _, err := Build(c); err != nil {
		t.Fatalf("per-model/vk modes rejected: %v", err)
	}
}

func TestBuildRejectsUnknownOptimizationModes(t *testing.T) {
	for _, tc := range []struct {
		where string
		mut   func(*Config)
		want  string
	}{
		{"global", func(c *Config) { c.Optimization.RTKMode = "turbo" }, "invalid rtk_mode"},
		{"global", func(c *Config) { c.Optimization.CavemanMode = "ultra" }, "invalid caveman_mode"},
		{"global", func(c *Config) { c.Optimization.HeadroomProfile = "extreme" }, "invalid headroom_profile"},
		{"model", func(c *Config) { c.Models[0].Optimization = &Optimization{RTKMode: "max"} }, "model m"},
		{"vk", func(c *Config) { c.VirtualKeys[0].Optimization = &Optimization{HeadroomProfile: "wild"} }, "virtual key k"},
	} {
		c := baseConfig()
		tc.mut(&c)
		_, err := Build(c)
		if err == nil || !contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got %v", tc.where, tc.want, err)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestBuildSquozeExclusive(t *testing.T) {
	// squoze alone is valid on every cascade level.
	c := baseConfig()
	c.Optimization.Squoze = true
	if _, err := Build(c); err != nil {
		t.Fatalf("pure squoze config rejected: %v", err)
	}
	c.Models[0].Optimization = &Optimization{Squoze: true}
	c.VirtualKeys[0].Optimization = &Optimization{Squoze: true}
	if _, err := Build(c); err != nil {
		t.Fatalf("per-level pure squoze rejected: %v", err)
	}

	// Any combination with the built-in optimizers must fail fast.
	for _, tc := range []struct {
		name string
		mut  func(*Optimization)
		want string
	}{
		{"rtk_mode", func(o *Optimization) { o.RTKMode = "standard" }, "rtk"},
		{"rtk_legacy", func(o *Optimization) { o.RTKCompression = true }, "rtk"},
		{"caveman_mode", func(o *Optimization) { o.CavemanMode = "lite" }, "caveman"},
		{"caveman_legacy", func(o *Optimization) { o.Caveman = true }, "caveman"},
		{"headroom_profile", func(o *Optimization) { o.HeadroomProfile = "balanced" }, "headroom"},
		{"headroom_legacy", func(o *Optimization) { o.Headroom = true }, "headroom"},
	} {
		c := baseConfig()
		c.Optimization.Squoze = true
		tc.mut(&c.Optimization)
		_, err := Build(c)
		if err == nil || !contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected conflict error mentioning %q, got %v", tc.name, tc.want, err)
		}
	}

	// Per-model conflicts are caught with their scope in the message.
	c = baseConfig()
	c.Models[0].Optimization = &Optimization{Squoze: true, RTKMode: "light"}
	if _, err := Build(c); err == nil || !contains(err.Error(), "model m") {
		t.Fatalf("expected scoped model conflict, got %v", err)
	}
}
