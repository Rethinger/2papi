package policy

import (
	"crypto/hmac"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rethinger/2papi/internal/config"
)

type Bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rpm    int
}

type daySpend struct {
	day   string
	spent float64
}

type shard struct {
	mu       sync.Mutex
	buckets  map[string]*Bucket
	tpm      map[string]*Bucket
	spend    map[string]*daySpend
	inflight map[string]int64
}

// Auth owns key verification plus per-key request metering: RPM token
// buckets, a deferred-commit TPM bucket, in-flight concurrency slots, and a
// rolling daily USD budget. Meter state survives snapshot adoption.
// Sharded 16-way for ~10ns key pick at 5k RPS (Bifrost-style).
type Auth struct {
	snap      atomic.Pointer[config.Snapshot]
	shards    [16]shard
	teamMu    sync.Mutex
	teamSpend map[string]*daySpend
}

func New(s *config.Snapshot) *Auth {
	auth := &Auth{
		teamSpend: map[string]*daySpend{},
	}
	for i := range auth.shards {
		auth.shards[i].buckets = map[string]*Bucket{}
		auth.shards[i].tpm = map[string]*Bucket{}
		auth.shards[i].spend = map[string]*daySpend{}
		auth.shards[i].inflight = map[string]int64{}
	}
	auth.snap.Store(s)
	return auth
}

func (a *Auth) Adopt(s *config.Snapshot) { a.snap.Store(s) }

func shardIndex(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % 16)
}

func (a *Auth) shardFor(key string) *shard {
	return &a.shards[shardIndex(key)]
}

func (a *Auth) Authenticate(r *http.Request) (config.VirtualKey, bool) {
	h := r.Header.Get("Authorization")
	key := strings.TrimPrefix(h, "Bearer ")
	if key == h || key == "" {
		return config.VirtualKey{}, false
	}
	snapshot := a.snap.Load()
	got := snapshot.HashPresented(key)
	for _, vk := range snapshot.VirtualKeys {
		if hsh := snapshot.KeyHashes[vk.Name]; hmac.Equal(got, hsh) {
			return vk, true
		}
	}
	return config.VirtualKey{}, false
}

func Allows(v config.VirtualKey, model string) bool {
	if len(v.Models) == 0 {
		return true
	}
	for _, m := range v.Models {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

type BeginResult struct {
	Allowed                bool
	Reason                 string
	RPMRemaining           int
	TPMRemaining           int
	ConcurrencyRemaining   int
	BudgetUSD              float64
	BudgetRemainingUSD     float64
	TeamBudgetUSD          float64
	TeamBudgetRemainingUSD float64
}

func (a *Auth) Begin(vk config.VirtualKey) BeginResult {
	now := time.Now()
	result := BeginResult{Allowed: true, RPMRemaining: -1, TPMRemaining: -1, ConcurrencyRemaining: -1}
	sh := a.shardFor(vk.Name)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if vk.MaxConcurrency > 0 {
		cur := sh.inflight[vk.Name]
		if cur >= int64(vk.MaxConcurrency) {
			result.ConcurrencyRemaining = 0
			result.Allowed = false
			result.Reason = "concurrency_limited"
			return result
		}
		sh.inflight[vk.Name] = cur + 1
		result.ConcurrencyRemaining = vk.MaxConcurrency - int(cur+1)
	}
	if vk.RPM > 0 {
		b := sh.buckets[vk.Name]
		if b == nil {
			b = &Bucket{tokens: float64(vk.RPM), last: now, rpm: vk.RPM}
			sh.buckets[vk.Name] = b
		}
		b.mu.Lock()
		elapsed := now.Sub(b.last).Minutes()
		b.tokens += elapsed * float64(b.rpm)
		if b.tokens > float64(b.rpm) {
			b.tokens = float64(b.rpm)
		}
		b.last = now
		if b.tokens < 1 {
			b.mu.Unlock()
			result.RPMRemaining = 0
			result.Allowed = false
			result.Reason = "rate_limited"
			return result
		}
		b.tokens--
		result.RPMRemaining = int(b.tokens)
		b.mu.Unlock()
	}
	if vk.TPM > 0 {
		b := sh.tpm[vk.Name]
		if b == nil {
			b = &Bucket{tokens: float64(vk.TPM), last: now, rpm: vk.TPM}
			sh.tpm[vk.Name] = b
		}
		b.mu.Lock()
		elapsed := now.Sub(b.last).Seconds()
		b.tokens += elapsed * float64(b.rpm) / 60
		if b.tokens > float64(b.rpm) {
			b.tokens = float64(b.rpm)
		}
		b.last = now
		result.TPMRemaining = int(b.tokens)
		if b.tokens < 1 {
			b.mu.Unlock()
			result.Allowed = false
			result.Reason = "rate_limited"
			return result
		}
		b.mu.Unlock()
	}
	if vk.BudgetUSD > 0 {
		day := now.UTC().Format("2006-01-02")
		spent := sh.spendFor(vk.Name, day)
		result.BudgetUSD = vk.BudgetUSD
		result.BudgetRemainingUSD = vk.BudgetUSD - spent
		if spent >= vk.BudgetUSD {
			result.Allowed = false
			result.Reason = "budget_exceeded"
			return result
		}
	}
	if vk.Team != nil && vk.Team.BudgetUSD > 0 {
		day := now.UTC().Format("2006-01-02")
		spent := a.teamSpendFor(vk.Team.ID, day)
		result.TeamBudgetUSD = vk.Team.BudgetUSD
		result.TeamBudgetRemainingUSD = vk.Team.BudgetUSD - spent
		if spent >= vk.Team.BudgetUSD {
			result.Allowed = false
			result.Reason = "budget_exceeded"
			return result
		}
	}
	if vk.Team != nil && vk.Team.ShareUSD > 0 {
		day := now.UTC().Format("2006-01-02")
		spent := sh.spendFor(vk.Name, day)
		if spent >= vk.Team.ShareUSD {
			result.Allowed = false
			result.Reason = "budget_exceeded"
			return result
		}
	}
	return result
}

func (a *Auth) teamSpendFor(teamID, day string) float64 {
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	entry := a.teamSpend[teamID]
	if entry == nil {
		return 0
	}
	if entry.day != day {
		delete(a.teamSpend, teamID)
		return 0
	}
	return entry.spent
}

func (a *Auth) Finalize(vk config.VirtualKey, tokens int64, costUSD float64, committed bool) {
	now := time.Now()
	sh := a.shardFor(vk.Name)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if cur := sh.inflight[vk.Name]; cur > 0 {
		sh.inflight[vk.Name] = cur - 1
	}
	if !committed || (tokens <= 0 && costUSD <= 0) {
		return
	}
	if vk.TPM > 0 && tokens > 0 {
		b := sh.tpm[vk.Name]
		if b == nil {
			b = &Bucket{tokens: float64(vk.TPM), last: now, rpm: vk.TPM}
			sh.tpm[vk.Name] = b
		}
		b.mu.Lock()
		elapsed := now.Sub(b.last).Seconds()
		b.tokens += elapsed * float64(b.rpm) / 60
		if b.tokens > float64(b.rpm) {
			b.tokens = float64(b.rpm)
		}
		b.last = now
		b.tokens -= float64(tokens)
		b.mu.Unlock()
	}
	if (vk.BudgetUSD > 0 || (vk.Team != nil && vk.Team.ShareUSD > 0)) && costUSD > 0 {
		day := now.UTC().Format("2006-01-02")
		spent := sh.spendFor(vk.Name, day)
		sh.spend[vk.Name] = &daySpend{day: day, spent: spent + costUSD}
	}
	if vk.Team != nil && vk.Team.BudgetUSD > 0 && costUSD > 0 {
		day := now.UTC().Format("2006-01-02")
		a.teamMu.Lock()
		entry := a.teamSpend[vk.Team.ID]
		var spent float64
		if entry != nil {
			if entry.day != day {
				delete(a.teamSpend, vk.Team.ID)
			} else {
				spent = entry.spent
			}
		}
		a.teamSpend[vk.Team.ID] = &daySpend{day: day, spent: spent + costUSD}
		a.teamMu.Unlock()
	}
}

func (s *shard) spendFor(name, day string) float64 {
	entry := s.spend[name]
	if entry == nil {
		return 0
	}
	if entry.day != day {
		delete(s.spend, name)
		return 0
	}
	return entry.spent
}
