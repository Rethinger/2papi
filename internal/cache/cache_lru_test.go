package cache

import (
	"testing"
	"time"
)

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewTTLResponseCache(3)
	c.Set("a", []byte("1"), nil, time.Minute)
	c.Set("b", []byte("2"), nil, time.Minute)
	c.Set("c", []byte("3"), nil, time.Minute)

	// Touch "a" so the LRU order becomes c, b, a.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a must be present")
	}

	// Insert a fourth key: capacity 3 → "b" (now LRU) must be evicted.
	c.Set("d", []byte("4"), nil, time.Minute)

	if _, ok := c.Get("b"); ok {
		t.Fatal("b must be evicted as the least-recently-used entry")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("%s must survive the eviction", k)
		}
	}
}

func TestLRUTouchOnGetKeepsHotEntry(t *testing.T) {
	c := NewTTLResponseCache(2)
	c.Set("hot", []byte("1"), nil, time.Minute)
	c.Set("cold", []byte("2"), nil, time.Minute)

	// Touch hot, then fill over capacity.
	_, _ = c.Get("hot")
	c.Set("new", []byte("3"), nil, time.Minute)

	if _, ok := c.Get("cold"); ok {
		t.Fatal("cold must be evicted before the touched hot entry")
	}
	if _, ok := c.Get("hot"); !ok {
		t.Fatal("hot entry must survive")
	}
}

func TestExpiredEntriesArePurgedOnGetAndLRUDropped(t *testing.T) {
	c := NewTTLResponseCache(10)
	c.Set("gone", []byte("1"), nil, time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, ok := c.Get("gone"); ok {
		t.Fatal("expired entry must not be a hit")
	}
	if c.Size() != 0 {
		t.Fatalf("expired entry must be purged, size=%d", c.Size())
	}
}
