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
	// Model is the public alias this entry was stored for. The similar lookup
	// considers same-model entries only: a near-duplicate question answered by
	// gpt-5 is not an answer from claude-opus-4-5. Entries written through Set
	// and entries restored from an older on-disk cache have it empty, and are
	// therefore skipped by that lookup rather than matched loosely.
	Model string
	// RequestHash stores the SHA256 of the request body so semantic hits can
	// compare the request, not just the response (Bifrost-style). Empty = exact key.
	RequestHash string
	// RequestWords are the significant words of the last user message, DISTINCT
	// and in first-appearance order, used by the cheap semantic FindSimilar
	// (Jaccard overlap) without embeddings. Storing a set rather than a bag is
	// what lets the scan compare against it without rebuilding one per entry.
	RequestWords []string
	// WordBits is a 64-bit signature of RequestWords, one bit per word, so the
	// similar scan can bound an entry's overlap with bit tests before it compares
	// a single string. Zero means "no words"; see wordSignature.
	WordBits uint64
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

// get returns a live entry and touches the LRU, without recording a hit or a
// miss. Lookup needs that: it tries exact then similar, and only the outcome it
// actually serves may be counted.
func (c *TTLResponseCache) get(key string) (Entry, bool) {
	if c == nil || key == "" {
		return Entry{}, false
	}
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
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
		return Entry{}, false
	}
	c.mu.Lock()
	if e, ok := c.lruIdx[key]; ok {
		c.lru.MoveToFront(e)
	}
	c.mu.Unlock()
	return entry, true
}

// Get is the exact-key lookup and records its own outcome. Callers that also
// want the similar lookup should use Lookup, which counts exactly once.
func (c *TTLResponseCache) Get(key string) (Entry, bool) {
	entry, ok := c.get(key)
	if !ok {
		c.noteMiss()
		return Entry{}, false
	}
	c.noteHit(false)
	return entry, true
}

// Kind reports which path answered a Lookup.
type Kind string

const (
	KindMiss    Kind = "miss"
	KindExact   Kind = "exact"
	KindSimilar Kind = "similar"
)

// Lookup resolves one request against the cache and records exactly one
// outcome. It tries the exact key first; only on a miss, and only when
// threshold > 0, does it consider a near-duplicate entry. The returned score is
// the Jaccard overlap that earned a similar hit (1 for an exact hit, 0 for a
// miss), so the caller can publish it instead of asserting it.
func (c *TTLResponseCache) Lookup(model string, body []byte, threshold float64) (Entry, Kind, float64, bool) {
	if c == nil {
		return Entry{}, KindMiss, 0, false
	}
	if entry, ok := c.get(c.KeyFor(model, body)); ok {
		c.noteHit(false)
		return entry, KindExact, 1, true
	}
	if threshold > 0 {
		if entry, _, score, ok := c.FindSimilar(model, body, threshold); ok {
			c.noteHit(true)
			return entry, KindSimilar, score, true
		}
	}
	c.noteMiss()
	return Entry{}, KindMiss, 0, false
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
	c.SetWithRequest(key, body, header, ttl, nil, "", 0, 0)
}

// SetWithRequest stores an entry and keeps the request hash + prompt cache
// accounting for semantic lookups and DeepSeek-style prompt_cache stats.
func (c *TTLResponseCache) SetWithRequest(key string, body []byte, header map[string][]string, ttl time.Duration, requestBody []byte, model string, cacheHit, cacheMiss int) {
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
		Model:     model,
	}
	if len(requestBody) > 0 {
		entry.RequestHash = requestHash(requestBody)
		entry.RequestWords = requestUserWordList(requestBody)
		entry.WordBits = wordSignature(entry.RequestWords)
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
				// A cache written by an older build has the words as they were
				// typed -- repeats and all -- and no signature. FindSimilar's
				// prunes assume a distinct list and a live signature, so both are
				// restored here instead of being defended against per lookup.
				v.RequestWords = distinctWords(v.RequestWords)
				v.WordBits = wordSignature(v.RequestWords)
				c.entries[k] = v
				c.lruIdx[k] = c.lru.PushFront(k)
			}
		}
	}
	return nil
}

// MinSimilarWords is the floor on significant words in the last user message
// before a near-duplicate lookup is allowed at all. Short prompts ("continue",
// "yes", "fix it") overlap almost everything, so Jaccard on them is noise
// dressed as a score. wordList drops words of two characters or fewer and
// repeats, so this counts DISTINCT words that carry meaning -- eight of the same
// word is still one word's worth of signal.
const MinSimilarWords = 8

// DefaultSimilarThreshold is the Jaccard overlap a near-duplicate must reach
// when a model turns the mode on without naming a threshold. It is deliberately
// high: a similar hit answers a question that was not asked, so the two
// wordings have to be nearly the same one.
const DefaultSimilarThreshold = 0.95

// similarEligible parses the request once and answers two questions together:
// may this request be served by a near-duplicate answer at all, and what are the
// significant words to score it by. It is deliberately fail-closed: anything it
// cannot parse or does not recognise returns false, so a caller can never widen
// the mode by forgetting a check.
//
// Rejected: a request carrying tools (the answer depends on tool results, not
// on the wording), any tool-role message (same reason, mid-conversation), more
// than two messages besides system (an agent loop's "continue" says nothing
// about the task it continues), and a last user message under MinSimilarWords.
// A multimodal content array fails the unmarshal, which is a rejection too.
func similarEligible(body []byte) ([]string, bool) {
	var payload struct {
		Tools    json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Messages) == 0 {
		return nil, false
	}
	if t := strings.TrimSpace(string(payload.Tools)); t != "" && t != "null" && t != "[]" {
		return nil, false
	}
	turns := 0
	lastUser := ""
	for _, m := range payload.Messages {
		switch m.Role {
		case "system":
			continue
		case "tool":
			return nil, false
		case "user":
			lastUser = m.Content
		}
		turns++
		if turns > 2 {
			return nil, false
		}
	}
	if lastUser == "" {
		return nil, false
	}
	words := wordList(lastUser)
	if len(words) < MinSimilarWords {
		return nil, false
	}
	return words, true
}

// FindSimilar returns the closest cached entry for the same model whose last
// user message overlaps this one by at least threshold (Jaccard, 0 < t <= 1),
// together with its key and the score it earned. It is a cheap stand-in for a
// semantic cache: no embeddings, no extra dependency, one linear scan.
//
// It records nothing. Accounting belongs to Lookup, which knows whether the
// exact path already answered.
func (c *TTLResponseCache) FindSimilar(model string, body []byte, threshold float64) (Entry, string, float64, bool) {
	if c == nil || threshold <= 0 {
		return Entry{}, "", 0, false
	}
	queryWords, ok := similarEligible(body)
	if !ok {
		return Entry{}, "", 0, false
	}
	queryHash := requestHash(body)
	// The query becomes a set once, plus one bit per word for the prune below.
	// Candidates are then probed against it, so the scan allocates nothing per
	// entry -- which is the difference between microseconds and milliseconds at
	// four thousand entries.
	querySet := make(map[string]struct{}, len(queryWords))
	queryBits := make([]uint64, 0, len(queryWords))
	for _, w := range queryWords {
		if _, dup := querySet[w]; dup {
			continue
		}
		querySet[w] = struct{}{}
		queryBits = append(queryBits, wordBit(w))
	}
	querySize := len(querySet)

	c.mu.RLock()
	now := time.Now()
	bestScore := 0.0
	var best Entry
	var bestKey string
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			continue
		}
		// Empty Model means the entry predates this field (Set, or an on-disk
		// cache written by an older build): skipped, never matched loosely.
		if v.Model == "" || v.Model != model {
			continue
		}
		score := 0.0
		switch {
		case v.RequestHash == queryHash:
			// Same request body under a different key: KeyFor projects the body
			// onto a subset of its fields, so two keys can share a hash.
			score = 1
		case len(v.RequestWords) > 0:
			candidateSize := len(v.RequestWords)
			// Two prunes before any string work, both exact upper bounds on the
			// score. First the sizes alone: the intersection is at most the
			// smaller set, so a candidate too long or too short is out for one
			// integer comparison.
			if !overlapReaches(min(querySize, candidateSize), querySize, candidateSize, threshold) {
				continue
			}
			// Then the signatures: a query word whose bit is missing from the
			// candidate is certainly not in it, so the bits that do appear cap
			// the intersection. Thirteen AND-tests replace thirteen string
			// hashes, and on a cache of unrelated questions that is where nearly
			// every candidate is dropped.
			maxInter := 0
			for _, bit := range queryBits {
				if v.WordBits&bit != 0 {
					maxInter++
				}
			}
			if !overlapReaches(maxInter, querySize, candidateSize, threshold) {
				continue
			}
			score = wordOverlap(querySet, querySize, v.RequestWords)
		}
		if score == 0 {
			continue
		}
		// One selection rule for every candidate, including the hash match, and
		// a tiebreak on the key. Without it the winner among equal scores would
		// follow Go's map iteration order, which is randomised per run.
		if score > bestScore || (score == bestScore && k < bestKey) {
			bestScore, best, bestKey = score, v, k
		}
	}
	c.mu.RUnlock()

	if bestScore >= threshold {
		return best, bestKey, bestScore, true
	}
	return Entry{}, "", 0, false
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

// wordList returns the significant words of s: lowercased, longer than two
// characters, and distinct. Deduping on the way in is what makes a stored
// RequestWords slice usable as a set, so neither the gate nor the scan has to
// build one. First appearance wins, so the order is stable.
func wordList(s string) []string {
	fields := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		if len(w) > 2 {
			out = append(out, w)
		}
	}
	return distinctWords(out)
}

// distinctWords keeps the first appearance of each word and drops the rest. It
// runs when an entry is stored or restored, never inside the scan, so it favours
// being obvious over saving the copy.
func distinctWords(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(words))
	out := make([]string, 0, len(words))
	for _, w := range words {
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	return out
}

// wordSignature packs a word set into 64 bits, one bit per word. Two sets that
// share a word share its bit, so a candidate missing a query word's bit cannot
// contain that word -- which is what lets FindSimilar bound an entry's score
// without touching its strings. Collisions (two words on one bit) only ever make
// that bound looser, never wrong: a false positive costs one exact comparison,
// and a false negative cannot happen.
func wordSignature(words []string) uint64 {
	var bits uint64
	for _, w := range words {
		bits |= wordBit(w)
	}
	return bits
}

func wordBit(w string) uint64 {
	return 1 << (fnv64a(w) & 63)
}

// fnv64a is FNV-1a over the bytes of s, written out rather than taken from
// hash/fnv because that allocates a hash object per call and this runs on every
// stored request. It is seedless, so a signature means the same thing in every
// process and survives a trip through the on-disk cache.
func fnv64a(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// overlapReaches reports whether an intersection of at most inter words could
// still reach threshold. The union is at least querySize+candidateSize-inter and
// Jaccard grows with the intersection, so this is exact as an upper bound: false
// means no arrangement of the words can qualify.
func overlapReaches(inter, querySize, candidateSize int, threshold float64) bool {
	union := querySize + candidateSize - inter
	if union <= 0 {
		return false
	}
	return float64(inter) >= threshold*float64(union)
}

// wordOverlap returns the exact Jaccard overlap between the query set and a
// candidate's words without allocating: the candidate slice is probed against
// the query map instead of becoming a set of its own. Building one map per entry
// is what made a full-cache scan cost milliseconds rather than microseconds.
// RequestWords is distinct by construction -- wordList dedupes what it stores and
// LoadFromFile normalises what older builds wrote -- so its length is its set
// size.
func wordOverlap(querySet map[string]struct{}, querySize int, words []string) float64 {
	inter := 0
	for _, w := range words {
		if _, ok := querySet[w]; ok {
			inter++
		}
	}
	union := querySize + len(words) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
