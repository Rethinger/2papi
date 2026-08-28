package router

import (
	"crypto/rand"
	"encoding/binary"
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
)

type Affinity struct {
	Account string
	Expires time.Time
}

type Router struct {
	snap     atomic.Pointer[config.Snapshot]
	state    *resilience.State
	mu       sync.Mutex
	affinity map[string]Affinity
	cursor   map[string]uint64
	lkgp     map[string]string
}

func New(s *config.Snapshot, st *resilience.State) *Router {
	r := &Router{
		state:    st,
		affinity: map[string]Affinity{},
		cursor:   map[string]uint64{},
		lkgp:     map[string]string{},
	}
	r.snap.Store(s)
	return r
}

func (r *Router) Adopt(s *config.Snapshot) { r.snap.Store(s) }

func (r *Router) Plan(modelAlias, aff string) ([]config.Account, config.Model) {
	snapshot := r.snap.Load()
	m := snapshot.ModelsByAlias[modelAlias]
	var c []config.Account
	for _, n := range m.Accounts {
		a := snapshot.AccountsByName[n]
		if !a.Enabled || r.state.Cooling(a.Name) || r.state.CircuitOpen(a.Name, snapshot.CircuitReset) || r.state.LockedOut(a.Name) || !r.state.TryAcquire(a.Name, a.MaxConcurrency) {
			continue
		}
		r.state.Release(a.Name)
		c = append(c, a)
	}
	// Per-source weights (шаг 5) override account-level ordering weight
	// within this alias — candidates are value copies, the snapshot stays
	// untouched.
	for i := range c {
		if w, ok := m.WeightFor(c[i].Name); ok {
			c[i].Weight = w
		}
	}
	if len(c) == 0 {
		return nil, m
	}
	strategy := m.RoutingStrategy
	if strategy == "" {
		strategy = snapshot.Routing.Strategy
	}
	if aff != "" && strategy != "round_robin" && strategy != "quota_failover" && strategy != "p2c" && strategy != "adaptive" {
		r.mu.Lock()
		v, ok := r.affinity[aff]
		r.mu.Unlock()
		if ok && time.Now().Before(v.Expires) {
			for i, a := range c {
				if a.Name == v.Account {
					c = append([]config.Account{a}, append(c[:i], c[i+1:]...)...)
					return limit(c, snapshot.Routing.MaxAttempts), m
				}
			}
		}
	}
	switch strategy {
	case "round_robin":
		r.mu.Lock()
		start := int(r.cursor[m.Alias] % uint64(len(c)))
		r.cursor[m.Alias]++
		r.mu.Unlock()
		c = append(append([]config.Account(nil), c[start:]...), c[:start]...)
	case "quota_failover":
		// Preserve provider-declared order until a real 429 puts an account into cooldown.
	case "priority", "fallback-chain":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Priority < c[j].Priority })
	case "fastest":
		sort.SliceStable(c, func(i, j int) bool { return r.state.Latency(c[i].Name) < r.state.Latency(c[j].Name) })
	case "cheapest":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Cost < c[j].Cost })
	case "quota-drain":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Weight > c[j].Weight })
	case "least-used":
		sort.SliceStable(c, func(i, j int) bool {
			ui := r.state.TotalRequests(c[i].Name)
			uj := r.state.TotalRequests(c[j].Name)
			if ui == uj {
				return r.state.Active(c[i].Name) < r.state.Active(c[j].Name)
			}
			return ui < uj
		})
	case "p2c":
		c = r.p2c(c)
	case "lkgp":
		c = r.applyLKGP(m.Alias, c)
	case "reset-aware":
		c = r.resetAware(c)
	case "adaptive":
		c = r.adaptive(c)
	default:
		c = r.balanced(c)
	}
	if strategy == "quota_failover" {
		return c, m
	}
	return limit(c, snapshot.Routing.MaxAttempts), m
}

func (r *Router) p2c(c []config.Account) []config.Account {
	if len(c) < 2 {
		return c
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := uint64(len(c))
	i := int(binary.LittleEndian.Uint32(b[:4]) % uint32(n))
	j := int(binary.LittleEndian.Uint32(b[4:]) % uint32(n))
	if i == j {
		j = (i + 1) % len(c)
	}
	// Score candidates: active in-flight count first, then EWMA latency.
	score := func(acct config.Account) (int, time.Duration) {
		return r.state.Active(acct.Name), r.state.Latency(acct.Name)
	}
	ai, li := score(c[i])
	aj, lj := score(c[j])
	var best, runner int
	if ai < aj || (ai == aj && li <= lj) {
		best, runner = i, j
	} else {
		best, runner = j, i
	}
	out := make([]config.Account, 0, len(c))
	out = append(out, c[best], c[runner])
	for k, a := range c {
		if k != best && k != runner {
			out = append(out, a)
		}
	}
	return out
}

func (r *Router) applyLKGP(modelAlias string, c []config.Account) []config.Account {
	r.mu.Lock()
	lastGood := r.lkgp[modelAlias]
	r.mu.Unlock()
	if lastGood == "" {
		return r.balanced(c)
	}
	for i, a := range c {
		if a.Name == lastGood {
			return append([]config.Account{a}, append(c[:i], c[i+1:]...)...)
		}
	}
	return r.balanced(c)
}

func (r *Router) CommitLKGP(modelAlias, acct string) {
	if modelAlias == "" || acct == "" {
		return
	}
	r.mu.Lock()
	r.lkgp[modelAlias] = acct
	r.mu.Unlock()
}

func (r *Router) resetAware(c []config.Account) []config.Account {
	if len(c) < 2 {
		return c
	}
	out := append([]config.Account(nil), c...)
	sort.SliceStable(out, func(i, j int) bool {
		ti := parseResetOrExpiry(out[i])
		tj := parseResetOrExpiry(out[j])
		if !ti.IsZero() && !tj.IsZero() && !ti.Equal(tj) {
			return ti.Before(tj) // drain accounts whose quota resets soonest
		}
		if !ti.IsZero() && tj.IsZero() {
			return true
		}
		if ti.IsZero() && !tj.IsZero() {
			return false
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Weight > out[j].Weight
	})
	return out
}

func parseResetOrExpiry(a config.Account) time.Time {
	if a.Credential.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, a.Credential.ExpiresAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (r *Router) balanced(c []config.Account) []config.Account {
	if len(c) < 2 {
		return c
	}
	out := append([]config.Account(nil), c...)
	sort.SliceStable(out, func(i, j int) bool {
		ai := r.state.Active(out[i].Name)
		aj := r.state.Active(out[j].Name)
		if ai == aj {
			return out[i].Weight > out[j].Weight
		}
		return ai < aj
	})
	return out
}

func (r *Router) adaptive(c []config.Account) []config.Account {
	if len(c) < 2 {
		return c
	}
	out := append([]config.Account(nil), c...)
	sort.SliceStable(out, func(i, j int) bool {
		ai := r.state.Active(out[i].Name)
		aj := r.state.Active(out[j].Name)
		li := r.state.Latency(out[i].Name)
		lj := r.state.Latency(out[j].Name)
		if li == 0 {
			li = 50 * time.Millisecond
		}
		if lj == 0 {
			lj = 50 * time.Millisecond
		}
		// score: active heavily weighted, latency moderate, weight bonus
		si := float64(ai)*100 + float64(li.Milliseconds())*0.1 - float64(out[i].Weight)*5
		sj := float64(aj)*100 + float64(lj.Milliseconds())*0.1 - float64(out[j].Weight)*5
		if si == sj {
			return out[i].Priority < out[j].Priority
		}
		return si < sj
	})
	return out
}

func (r *Router) CommitAffinity(k, acct string) {
	if k == "" {
		return
	}
	r.mu.Lock()
	r.affinity[k] = Affinity{acct, time.Now().Add(r.snap.Load().StickyTTL)}
	r.mu.Unlock()
}

func AffinityKey(header, user, model string, metadata map[string]any) string {
	if header != "" {
		return header
	}
	if v, ok := metadata["gateway_session"].(string); ok && v != "" {
		return v
	}
	h := fnv.New64a()
	h.Write([]byte(user + ":" + model))
	return string(h.Sum(nil))
}

func limit(a []config.Account, n int) []config.Account {
	if n > 0 && len(a) > n {
		return a[:n]
	}
	return a
}
