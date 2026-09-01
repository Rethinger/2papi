package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Body      []byte
	Header    map[string][]string
	ExpiresAt time.Time
	// RequestHash stores the SHA256 of the request body so semantic hits can
	// compare the request, not just the response (Bifrost-style). Empty = exact key.
	RequestHash string
	// RequestWords are the significant words of the last user message, used by
	// the cheap semantic FindSimilar (Jaccard overlap) without embeddings.
	RequestWords []string
	// PromptCacheHitTokens approximates deepseek/gemini prompt_cache_hit_tokens.
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
}

type TTLResponseCache struct {
	mu      sync.RWMutex
	entries map[string]Entry
	maxSize int
	// lru orders entries most-recently-used → least-recently-used (front →
	// back); lruIdx maps keys to their list elements for O(1) touch/evict.
	// Eviction always drops the LRU entry (vitok 9: response-cache LRU).
	lru         *list.List
	lruIdx      map[string]*list.Element
	exactHits   uint64
	similarHits uint64
	misses      uint64
	// hitRateSeries keeps last 100 hit/miss deltas for /api/cache/stats
	hitSeries [100]bool
	seriesPos int
}

func NewTTLResponseCache(maxSize int) *TTLResponseCache {
	if maxSize <= 0 {
		maxSize = 2048
	}
	c := &TTLResponseCache{
		entries: map[string]Entry{},
		maxSize: maxSize,
		lru:     list.New(),
		lruIdx:  map[string]*list.Element{},
	}
	return c
}

func (c *TTLResponseCache) KeyFor(model string, body []byte) string {
	var payload struct {
		Model       string          `json:"model"`
		Messages    json.RawMessage `json:"messages"`
		Temperature *float64        `json:"temperature,omitempty"`
		MaxTokens   int             `json:"max_tokens,omitempty"`
		Tools       json.RawMessage `json:"tools,omitempty"`
	}
	_ = json.Unmarshal(body, &payload)
	payload.Model = model

	norm, err := json.Marshal(payload)
	if err != nil {
		norm = body
	}
	h := sha256.Sum256(norm)
	return hex.EncodeToString(h[:])
}

func (c *TTLResponseCache) Get(key string) (Entry, bool) {
	if c == nil || key == "" {
		return Entry{}, false
	}
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		c.noteMiss()
		return Entry{}, false
	}
	if time.Now().After(entry.ExpiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		if e, ok := c.lruIdx[key]; ok {
			c.lru.Remove(e)
			delete(c.lruIdx, key)
		}
		c.mu.Unlock()
		c.noteMiss()
		return Entry{}, false
	}
	c.mu.Lock()
	if e, ok := c.lruIdx[key]; ok {
		c.lru.MoveToFront(e)
	}
	c.mu.Unlock()
	c.noteHit(false)
	return entry, true
}

// Stats captures cache hit/miss counters for the Token Lens widget.
type Stats struct {
	Size        int     `json:"size"`
	MaxSize     int     `json:"max_size"`
	HitRate     float64 `json:"hit_rate"`
	ExactHits   uint64  `json:"exact_hits"`
	SimilarHits uint64  `json:"similar_hits"`
	Misses      uint64  `json:"misses"`
}

func (c *TTLResponseCache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	total := c.exactHits + c.similarHits + c.misses
	rate := 0.0
	if total > 0 {
		rate = float64(c.exactHits+c.similarHits) / float64(total)
	}
	return Stats{Size: len(c.entries), MaxSize: c.maxSize, HitRate: rate, ExactHits: c.exactHits, SimilarHits: c.similarHits, Misses: c.misses}
}

func (c *TTLResponseCache) noteHit(similar bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if similar {
		c.similarHits++
	} else {
		c.exactHits++
	}
	c.hitSeries[c.seriesPos%100] = true
	c.seriesPos++
	c.mu.Unlock()
}

func (c *TTLResponseCache) noteMiss() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.misses++
	c.hitSeries[c.seriesPos%100] = false
	c.seriesPos++
	c.mu.Unlock()
}

// Set stores an entry. requestBody is the incoming chat payload (used for
// semantic RequestHash); pass nil for exact-key-only caches.
func (c *TTLResponseCache) Set(key string, body []byte, header map[string][]string, ttl time.Duration) {
	c.SetWithRequest(key, body, header, ttl, nil, 0, 0)
}

// SetWithRequest stores an entry and keeps the request hash + prompt cache
// accounting for semantic lookups and DeepSeek-style prompt_cache stats.
func (c *TTLResponseCache) SetWithRequest(key string, body []byte, header map[string][]string, ttl time.Duration, requestBody []byte, cacheHit, cacheMiss int) {
	if c == nil || key == "" || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxSize {
		// Evict expired first
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.ExpiresAt) {
				delete(c.entries, k)
				if e, ok := c.lruIdx[k]; ok {
					c.lru.Remove(e)
					delete(c.lruIdx, k)
				}
			}
		}
		// If still full, drop LRU entries until under capacity.
		for len(c.entries) >= c.maxSize {
			back := c.lru.Back()
			if back == nil {
				break
			}
			k := back.Value.(string)
			delete(c.entries, k)
			c.lru.Remove(back)
			delete(c.lruIdx, k)
		}
	}

	entry := Entry{
		Body:      body,
		Header:    header,
		ExpiresAt: time.Now().Add(ttl),
	}
	if len(requestBody) > 0 {
		entry.RequestHash = requestHash(requestBody)
		entry.RequestWords = requestUserWordList(requestBody)
	}
	if cacheHit > 0 {
		entry.PromptCacheHitTokens = cacheHit
	}
	if cacheMiss > 0 {
		entry.PromptCacheMissTokens = cacheMiss
	}
	c.entries[key] = entry
	if e, ok := c.lruIdx[key]; ok {
		c.lru.MoveToFront(e)
	} else {
		c.lruIdx[key] = c.lru.PushFront(key)
	}
}

func (c *TTLResponseCache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *TTLResponseCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]Entry{}
	c.lru = list.New()
	c.lruIdx = map[string]*list.Element{}
}

// SaveToFile persists cache to disk (best-effort, for restart).
func (c *TTLResponseCache) SaveToFile(path string) error {
	if c == nil || path == "" {
		return nil
	}
	c.mu.RLock()
	data, err := json.Marshal(c.entries)
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadFromFile restores cache from disk.
func (c *TTLResponseCache) LoadFromFile(path string) error {
	if c == nil || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]Entry
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.lru = list.New()
	c.lruIdx = map[string]*list.Element{}
	for k, v := range m {
		if now.Before(v.ExpiresAt) {
			if len(c.entries) < c.maxSize {
				c.entries[k] = v
				c.lruIdx[k] = c.lru.PushFront(k)
			}
		}
	}
	return nil
}

// FindSimilar returns the most similar cached entry by Jaccard on the request body.
// threshold 0.8 means 80% word overlap on last user message required.
// This is a cheap semantic-like cache (Bifrost-style) — no embeddings needed.
func (c *TTLResponseCache) FindSimilar(model string, body []byte, threshold float64) (Entry, string, bool) {
	if c == nil || threshold <= 0 {
		return Entry{}, "", false
	}
	queryHash := requestHash(body)
	queryWords := requestUserWordList(body)
	if len(queryHash) == 0 || len(queryWords) == 0 {
		return Entry{}, "", false
	}
	querySet := wordSetFromList(queryWords)
	c.mu.RLock()
	now := time.Now()
	bestScore := 0.0
	var best Entry
	var bestKey string
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			continue
		}
		if v.RequestHash == queryHash {
			// exact request match (should have been caught by Get, but safe)
			bestScore = 1.0
			best = v
			bestKey = k
			break
		}
		if len(v.RequestWords) > 0 {
			score := jaccard(querySet, wordSetFromList(v.RequestWords))
			if score > bestScore {
				bestScore = score
				best = v
				bestKey = k
			}
		}
	}
	c.mu.RUnlock()
	if bestScore >= threshold {
		c.noteHit(true)
		return best, bestKey, true
	}
	c.noteMiss()
	return Entry{}, "", false
}

// requestHash returns the SHA256 of the request body (same as KeyFor hashing the whole body).
func requestHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// requestUserWordList extracts the significant words of the last user message.
func requestUserWordList(body []byte) []string {
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Messages) == 0 {
		return nil
	}
	var lastUser string
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		if payload.Messages[i].Role == "user" {
			lastUser = payload.Messages[i].Content
			break
		}
	}
	if lastUser == "" {
		return nil
	}
	return wordList(lastUser)
}

func wordSet(body []byte) map[string]struct{} {
	return wordSetFromList(requestUserWordList(body))
}

func wordSetFromList(words []string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

func wordList(s string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if len(w) > 2 {
			out = append(out, w)
		}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
