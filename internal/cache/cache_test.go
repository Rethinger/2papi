package cache

import (
	"testing"
	"time"
)

func TestCacheStatsAndSetWithRequest(t *testing.T) {
	c := NewTTLResponseCache(10)
	req := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello world test"}]}`)
	c.SetWithRequest("k1", []byte(`{"choices":[{"message":{"content":"hi"}}]}`), map[string][]string{"Content-Type": {"application/json"}}, time.Minute, req, 100, 50)

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
}

func TestSemanticFindSimilar(t *testing.T) {
	c := NewTTLResponseCache(10)
	req1 := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"what is the capital of france and its population"}]}`)
	c.SetWithRequest("k1", []byte("response-fr"), nil, time.Minute, req1, 0, 0)
	// similar request (overlapping words >80%)
	req2 := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"what is the capital of france and its population size"}]}`)
	if _, _, ok := c.FindSimilar("gpt-test", req2, 0.8); !ok {
		t.Fatal("expected semantic hit for similar request")
	}
	stats := c.Stats()
	if stats.SimilarHits != 1 {
		t.Fatalf("similar hits: %+v", stats)
	}
	// disjoint request → miss
	req3 := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"totally unrelated different topic about quantum physics"}]}`)
	if _, _, ok := c.FindSimilar("gpt-test", req3, 0.8); ok {
		t.Fatal("expected miss for disjoint request")
	}
}

func TestCacheSaveLoadRoundtrip(t *testing.T) {
	c := NewTTLResponseCache(10)
	c.SetWithRequest("k1", []byte("body"), nil, time.Hour, []byte("req"), 5, 10)
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
	if _, ok := restored.Get("k1"); !ok {
		t.Fatal("expected restored hit")
	}
}