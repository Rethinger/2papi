package cache

import (
	"strconv"
	"testing"
	"time"
)

// The gateway builds its cache with NewTTLResponseCache(4096) (internal/proxy:
// proxy.go), so that is the size the scan has to hold up at. Benchmarking a
// half-empty cache would measure nothing worth knowing: FindSimilar is a linear
// scan, and its cost is the number of live entries.
const benchCacheSize = 4096

// benchQuestion keeps every entry's word set distinct -- topic-N survives
// wordList (longer than two characters), so entry 7 does not share a set with
// entry 8. Thirteen significant words, well clear of MinSimilarWords, so the
// scan does the Jaccard work rather than bailing at the gate.
func benchQuestion(n int) string {
	return "what is the recommended replication factor for cluster topic-" + strconv.Itoa(n) + " and its total resident storage"
}

func benchBody(question string) []byte {
	return []byte(`{"model":"bench","stream":false,"messages":[{"role":"user","content":"` + question + `"}]}`)
}

// fullBenchCache fills a cache to capacity with same-model entries: the worst
// case for FindSimilar, because the model filter rejects nothing and every entry
// has to be considered. What each one then costs depends on the query, which is
// what the benchmarks below separate.
func fullBenchCache(tb testing.TB) *TTLResponseCache {
	tb.Helper()
	c := NewTTLResponseCache(benchCacheSize)
	for i := 0; i < benchCacheSize; i++ {
		body := benchBody(benchQuestion(i))
		c.SetWithRequest(c.KeyFor("bench", body), []byte(`{"choices":[{"message":{"content":"answer"}}]}`), nil, time.Hour, body, "bench", 0, 0)
	}
	if c.Size() != benchCacheSize {
		tb.Fatalf("cache filled to %d of %d entries", c.Size(), benchCacheSize)
	}
	return c
}

// The worst case, and the one NFR-1 is about: thirteen words like the entries'
// thirteen, so the size prune rules out nothing and all 4096 entries reach the
// signature test -- which then rejects them, because the question shares no words
// with any of them. The answer is still no, and a request that misses paid for
// the whole scan before it went upstream.
func BenchmarkFindSimilarFullCacheMiss(b *testing.B) {
	c := fullBenchCache(b)
	query := benchBody("explain how quantum entanglement affects modern distributed computing systems please without any diagrams")
	if _, _, _, ok := c.FindSimilar("bench", query, 0.9); ok {
		b.Fatal("fixture is not a miss")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.FindSimilar("bench", query, 0.9)
	}
}

// A hit costs the same scan -- the best candidate is only known once every entry
// has been considered -- so this measures the same walk with the winner returned.
// The fourteen-word query passes the size prune against every thirteen-word entry
// and shares twelve words with each, and twelve of fourteen is 0.8: the signature
// bound rejects all 4095 near-twins and only the entry this question was built
// from is scored word by word. That is the mode working, not luck -- overlap high
// enough to survive the bound is exactly what a near-duplicate is.
func BenchmarkFindSimilarFullCacheHit(b *testing.B) {
	c := fullBenchCache(b)
	query := benchBody(benchQuestion(0) + " today")
	if _, _, _, ok := c.FindSimilar("bench", query, 0.9); !ok {
		b.Fatal("fixture is not a hit")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.FindSimilar("bench", query, 0.9)
	}
}

// The same miss with a query too short to be a near-duplicate of anything here:
// ten significant words against thirteen cannot reach 0.9 whatever the words are,
// so the size prune drops all 4096 for one integer comparison each, without a
// word read or a bit tested. This is therefore the floor of a full scan -- what
// walking 4096 map entries costs on its own -- and the gap between it and the two
// benchmarks above is what the word work costs once the prunes have done theirs.
func BenchmarkFindSimilarSizePruned(b *testing.B) {
	c := fullBenchCache(b)
	query := benchBody("explain how quantum entanglement affects modern distributed computing systems please")
	if _, _, _, ok := c.FindSimilar("bench", query, 0.9); ok {
		b.Fatal("fixture is not a miss")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.FindSimilar("bench", query, 0.9)
	}
}

// The gate is the cheap path and worth measuring separately: a request under the
// word floor (or carrying tools) is refused before the scan starts, so an agent
// loop pays parsing only, not the 4096-entry walk.
func BenchmarkFindSimilarGateRejects(b *testing.B) {
	c := fullBenchCache(b)
	query := benchBody("continue")
	if _, _, _, ok := c.FindSimilar("bench", query, 0.9); ok {
		b.Fatal("short prompt passed the gate")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.FindSimilar("bench", query, 0.9)
	}
}

// Reference point: an exact hit is a map lookup, and Lookup tries it first. The
// similar scan only ever runs after this has already missed.
func BenchmarkLookupExactHitFullCache(b *testing.B) {
	c := fullBenchCache(b)
	query := benchBody(benchQuestion(benchCacheSize / 2))
	if _, kind, _, ok := c.Lookup("bench", query, 0.9); !ok || kind != KindExact {
		b.Fatalf("fixture is not an exact hit: ok=%v kind=%q", ok, kind)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Lookup("bench", query, 0.9)
	}
}
