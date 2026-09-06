package config

import "testing"

func thr(v float64) *float64 { return &v }

// AC-1.1 at the config level: the two pre-existing modes keep validating, and
// the third one is accepted with and without an explicit threshold.
func TestBuildAcceptsCacheModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		limit *float64
	}{
		{"empty", "", nil},
		{"off", "off", nil},
		{"exact", "exact", nil},
		{"similar default threshold", "similar", nil},
		{"similar explicit threshold", "similar", thr(0.9)},
		{"similar threshold at the upper bound", "similar", thr(1)},
	} {
		c := baseConfig()
		c.Models[0].Cache = tc.mode
		c.Models[0].CacheSimilarThreshold = tc.limit
		if _, err := Build(c); err != nil {
			t.Fatalf("%s: rejected: %v", tc.name, err)
		}
	}
}

// AC-1.2: a threshold outside (0, 1] must stop the gateway from starting, with a
// message that names the field. An explicit 0 is in that set on purpose -- it is
// why the field is a pointer: as a plain float it would be indistinguishable
// from "unset" and would silently disable the mode it looks like it configures.
func TestBuildRejectsBadCacheConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		limit *float64
		want  string
	}{
		{"threshold above one", "similar", thr(1.5), "cache_similar_threshold must be in (0, 1]"},
		{"explicit zero", "similar", thr(0), "cache_similar_threshold must be in (0, 1]"},
		{"negative threshold", "similar", thr(-0.5), "cache_similar_threshold must be in (0, 1]"},
		{"threshold without the mode", "exact", thr(0.9), "requires cache: similar"},
		{"threshold with no cache at all", "", thr(0.9), "requires cache: similar"},
		{"unknown mode", "fuzzy", nil, "cache must be off|exact|similar|empty"},
	} {
		c := baseConfig()
		c.Models[0].Cache = tc.mode
		c.Models[0].CacheSimilarThreshold = tc.limit
		_, err := Build(c)
		if err == nil || !contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected an error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}
