package cache

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCacheStatsAndSetWithRequest(t *testing.T) {
	c := NewTTLResponseCache(10)
	req := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello world test"}]}`)
	c.SetWithRequest("k1", []byte(`{"choices":[{"message":{"content":"hi"}}]}`), map[string][]string{"Content-Type": {"application/json"}}, time.Minute, req, "gpt-test", 100, 50)

	// exact hit
	if _, ok := c.Get("k1"); !ok {
		t.Fatal("expected exact hit")
	}
	// miss
	if _, ok := c.Get("nope"); ok {
		t.Fatal("expected miss")
	}
	stats := c.Stats()
	if stats.ExactHits != 1 || stats.Misses != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	if stats.Size != 1 || stats.MaxSize != 10 {
		t.Fatalf("size: %+v", stats)
	}
	if stats.HitRate != 0.5 {
		t.Fatalf("hit rate: %v", stats.HitRate)
	}
	// RequestHash stored
	c.mu.RLock()
	entry := c.entries["k1"]
	c.mu.RUnlock()
	if entry.RequestHash == "" {
		t.Fatal("request hash not stored")
	}
	if entry.PromptCacheHitTokens != 100 || entry.PromptCacheMissTokens != 50 {
		t.Fatalf("prompt cache: %+v", entry)
	}
	if entry.Model != "gpt-test" {
		t.Fatalf("model not stored: %q", entry.Model)
	}
}

// Both requests clear MinSimilarWords (10 and 11 significant words), so what
// these tests measure is overlap, not the gate. A fixture sitting under the
// floor would make the disjoint case below pass for the wrong reason.
const (
	reqFrance     = `{"model":"gpt-test","messages":[{"role":"user","content":"what is the capital city of france and its total resident population"}]}`
	reqFranceMore = `{"model":"gpt-test","messages":[{"role":"user","content":"what is the capital city of france and its total resident population today"}]}`
	reqQuantum    = `{"model":"gpt-test","messages":[{"role":"user","content":"explain how quantum entanglement affects modern distributed computing systems please"}]}`
)

func TestLookupExactSimilarMiss(t *testing.T) {
	c := NewTTLResponseCache(10)
	req1 := []byte(reqFrance)
	c.SetWithRequest(c.KeyFor("gpt-test", req1), []byte("response-fr"), nil, time.Minute, req1, "gpt-test", 0, 0)

	// Exact: same body, same key.
	if _, kind, score, ok := c.Lookup("gpt-test", req1, 0.8); !ok || kind != KindExact || score != 1 {
		t.Fatalf("exact: ok=%v kind=%q score=%v", ok, kind, score)
	}
	// Similar: one extra word, 10 of 11 shared -> 0.909.
	entry, kind, score, ok := c.Lookup("gpt-test", []byte(reqFranceMore), 0.8)
	if !ok || kind != KindSimilar {
		t.Fatalf("similar: ok=%v kind=%q", ok, kind)
	}
	if score < 0.8 || score > 1 {
		t.Fatalf("similar score out of range: %v", score)
	}
	if string(entry.Body) != "response-fr" {
		t.Fatalf("similar returned the wrong entry: %q", entry.Body)
	}
	// Disjoint: eligible, but nothing overlaps.
	if _, kind, _, ok := c.Lookup("gpt-test", []byte(reqQuantum), 0.8); ok || kind != KindMiss {
		t.Fatalf("disjoint: ok=%v kind=%q", ok, kind)
	}

	// One outcome per Lookup, never two: three calls, three counts.
	stats := c.Stats()
	if stats.ExactHits != 1 || stats.SimilarHits != 1 || stats.Misses != 1 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestLookupSimilarOffByDefault(t *testing.T) {
	c := NewTTLResponseCache(10)
	req1 := []byte(reqFrance)
	c.SetWithRequest(c.KeyFor("gpt-test", req1), []byte("response-fr"), nil, time.Minute, req1, "gpt-test", 0, 0)

	// threshold 0 is the exact-only caller: a near-duplicate must not be served.
	if _, kind, _, ok := c.Lookup("gpt-test", []byte(reqFranceMore), 0); ok || kind != KindMiss {
		t.Fatalf("threshold 0 served a similar entry: ok=%v kind=%q", ok, kind)
	}
}

func TestFindSimilarModelIsolation(t *testing.T) {
	c := NewTTLResponseCache(10)
	req := []byte(reqFrance)
	c.SetWithRequest("k1", []byte("answer-from-gpt"), nil, time.Minute, req, "gpt-test", 0, 0)

	// Same words, different model: an answer from gpt-test is not an answer from
	// claude-test, however similar the question reads.
	if _, _, _, ok := c.FindSimilar("claude-test", []byte(reqFranceMore), 0.8); ok {
		t.Fatal("expected a miss across models")
	}
	if _, key, _, ok := c.FindSimilar("gpt-test", []byte(reqFranceMore), 0.8); !ok || key != "k1" {
		t.Fatalf("same model: ok=%v key=%q", ok, key)
	}

	// Entries written through Set carry no model and are skipped, not matched
	// loosely -- same for entries restored from a cache file that predates the
	// field.
	c2 := NewTTLResponseCache(10)
	c2.SetWithRequest("k2", []byte("answer"), nil, time.Minute, req, "", 0, 0)
	if _, _, _, ok := c2.FindSimilar("gpt-test", []byte(reqFranceMore), 0.8); ok {
		t.Fatal("entry with no model was matched")
	}
}

func TestSimilarEligibleGate(t *testing.T) {
	user := `{"role":"user","content":"what is the capital city of france and its total resident population"}`
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"plain single turn", `{"messages":[` + user + `]}`, true},
		{"system plus user", `{"messages":[{"role":"system","content":"be terse"},` + user + `]}`, true},
		{"user and assistant", `{"messages":[` + user + `,{"role":"assistant","content":"paris"}]}`, true},
		{"tools null", `{"tools":null,"messages":[` + user + `]}`, true},
		{"tools empty", `{"tools":[],"messages":[` + user + `]}`, true},
		{"tools present", `{"tools":[{"type":"function"}],"messages":[` + user + `]}`, false},
		{"tool role", `{"messages":[` + user + `,{"role":"tool","content":"result"}]}`, false},
		{"three turns", `{"messages":[` + user + `,{"role":"assistant","content":"paris"},{"role":"user","content":"and the total resident population of the city itself"}]}`, false},
		{"under the word floor", `{"messages":[{"role":"user","content":"what is the capital of france"}]}`, false},
		{"multimodal content", `{"messages":[{"role":"user","content":[{"type":"text","text":"what is the capital city of france and its total population"}]}]}`, false},
		{"no user message", `{"messages":[{"role":"assistant","content":"the capital city of france is paris and it is large"}]}`, false},
		{"not json", `nonsense`, false},
	}
	for _, tc := range cases {
		words, ok := similarEligible([]byte(tc.body))
		if ok != tc.want {
			t.Errorf("%s: eligible=%v, want %v", tc.name, ok, tc.want)
		}
		if ok && len(words) < MinSimilarWords {
			t.Errorf("%s: eligible with only %d words", tc.name, len(words))
		}
		if !ok && words != nil {
			t.Errorf("%s: rejected but returned words", tc.name)
		}
	}
}

func TestFindSimilarGateAppliesToCachedEntry(t *testing.T) {
	c := NewTTLResponseCache(10)
	req := []byte(reqFrance)
	c.SetWithRequest("k1", []byte("answer"), nil, time.Minute, req, "gpt-test", 0, 0)

	// A tool-carrying request is refused even though a near-duplicate answer is
	// sitting in the cache: the gate lives inside FindSimilar, so no caller can
	// widen the mode by forgetting it.
	withTools := `{"model":"gpt-test","tools":[{"type":"function"}],"messages":[{"role":"user","content":"what is the capital city of france and its total resident population today"}]}`
	if _, _, _, ok := c.FindSimilar("gpt-test", []byte(withTools), 0.8); ok {
		t.Fatal("tool-carrying request was served from the similar cache")
	}
}

func TestFindSimilarDeterministicTiebreak(t *testing.T) {
	c := NewTTLResponseCache(10)
	// The same user text under two keys, so both candidates score identically.
	// The bodies differ (temperature) only to keep their request hashes apart.
	a := []byte(`{"model":"gpt-test","temperature":0,"messages":[{"role":"user","content":"what is the capital city of france and its total resident population"}]}`)
	z := []byte(`{"model":"gpt-test","temperature":1,"messages":[{"role":"user","content":"what is the capital city of france and its total resident population"}]}`)
	c.SetWithRequest("aaa", []byte("first"), nil, time.Minute, a, "gpt-test", 0, 0)
	c.SetWithRequest("zzz", []byte("second"), nil, time.Minute, z, "gpt-test", 0, 0)

	// Map iteration order is randomised per range, so an untied scan would
	// return either key across enough repeats.
	for i := 0; i < 50; i++ {
		_, key, _, ok := c.FindSimilar("gpt-test", []byte(reqFranceMore), 0.8)
		if !ok {
			t.Fatalf("run %d: expected a hit", i)
		}
		if key != "aaa" {
			t.Fatalf("run %d: tiebreak picked %q", i, key)
		}
	}
}

func TestFindSimilarSkipsExpired(t *testing.T) {
	c := NewTTLResponseCache(10)
	req := []byte(reqFrance)
	c.SetWithRequest("k1", []byte("stale"), nil, time.Millisecond, req, "gpt-test", 0, 0)
	time.Sleep(5 * time.Millisecond)
	if _, _, _, ok := c.FindSimilar("gpt-test", []byte(reqFranceMore), 0.8); ok {
		t.Fatal("expired entry was served as similar")
	}
}

func TestCacheSaveLoadRoundtrip(t *testing.T) {
	c := NewTTLResponseCache(10)
	c.SetWithRequest("k1", []byte("body"), nil, time.Hour, []byte("req"), "gpt-test", 5, 10)
	path := t.TempDir() + "/cache.json"
	if err := c.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	restored := NewTTLResponseCache(10)
	if err := restored.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	if restored.Size() != 1 {
		t.Fatalf("restored size=%d", restored.Size())
	}
	entry, ok := restored.Get("k1")
	if !ok {
		t.Fatal("expected restored hit")
	}
	// Model has to survive the round trip, or a restored cache would answer
	// every model's requests from whatever was cached for one of them.
	if entry.Model != "gpt-test" {
		t.Fatalf("model lost on reload: %q", entry.Model)
	}
}

// An on-disk cache written before RequestWords was a set carries the words as
// they were typed and no signature at all, and FindSimilar's prunes trust both.
// So the restore normalises them, and this is the case that proves it: the words
// below hold two repeats, the file has no WordBits key, and the near-duplicate
// question below has to be answered anyway. Drop either half of the restore and
// it is not -- a zero signature prunes the entry, and the repeats push the score
// to 10/13, under the threshold asked for.
func TestLoadFromFileNormalizesLegacyWords(t *testing.T) {
	legacy := map[string]map[string]any{
		"k1": {
			"Body":      []byte("legacy-body"),
			"ExpiresAt": time.Now().Add(time.Hour),
			"Model":     "gpt-test",
			"RequestWords": []string{
				"what", "the", "capital", "city", "france", "and",
				"its", "total", "resident", "population", "france", "the",
			},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/legacy.json"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewTTLResponseCache(10)
	if err := c.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	entry := c.entries["k1"]
	c.mu.RUnlock()
	if len(entry.RequestWords) != 10 {
		t.Fatalf("legacy words not deduped on load: %v", entry.RequestWords)
	}
	if entry.WordBits == 0 {
		t.Fatal("legacy entry restored without a word signature")
	}

	_, key, score, ok := c.FindSimilar("gpt-test", []byte(reqFranceMore), 0.8)
	if !ok {
		t.Fatal("restored legacy entry was not found as a near-duplicate")
	}
	if key != "k1" {
		t.Fatalf("key=%q, want k1", key)
	}
	if score < 0.9 || score > 0.92 {
		t.Fatalf("score=%v, want the 10-of-11 overlap (0.909)", score)
	}
}
