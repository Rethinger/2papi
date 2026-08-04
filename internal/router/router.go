package router

import (
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/resilience"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

type Affinity struct {
	Account string
	Expires time.Time
}
type Router struct {
	snap     *config.Snapshot
	state    *resilience.State
	mu       sync.Mutex
	affinity map[string]Affinity
}

func New(s *config.Snapshot, st *resilience.State) *Router {
	return &Router{snap: s, state: st, affinity: map[string]Affinity{}}
}
func (r *Router) Plan(modelAlias, aff string) ([]config.Account, config.Model) {
	m := r.snap.ModelsByAlias[modelAlias]
	var c []config.Account
	for _, n := range m.Accounts {
		a := r.snap.AccountsByName[n]
		if !a.Enabled || r.state.Cooling(a.Name) || r.state.CircuitOpen(a.Name, r.snap.CircuitReset) || !r.state.TryAcquire(a.Name, a.MaxConcurrency) {
			continue
		}
		r.state.Release(a.Name)
		c = append(c, a)
	}
	if len(c) == 0 {
		return nil, m
	}
	if aff != "" {
		r.mu.Lock()
		v, ok := r.affinity[aff]
		r.mu.Unlock()
		if ok && time.Now().Before(v.Expires) {
			for i, a := range c {
				if a.Name == v.Account {
					c = append([]config.Account{a}, append(c[:i], c[i+1:]...)...)
					return limit(c, r.snap.Routing.MaxAttempts), m
				}
			}
		}
	}
	switch r.snap.Routing.Strategy {
	case "priority", "fallback-chain":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Priority < c[j].Priority })
	case "fastest":
		sort.SliceStable(c, func(i, j int) bool { return r.state.Latency(c[i].Name) < r.state.Latency(c[j].Name) })
	case "cheapest":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Cost < c[j].Cost })
	case "quota-drain":
		sort.SliceStable(c, func(i, j int) bool { return c[i].Weight > c[j].Weight })
	default:
		c = r.balanced(c)
	}
	return limit(c, r.snap.Routing.MaxAttempts), m
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
func (r *Router) CommitAffinity(k, acct string) {
	if k == "" {
		return
	}
	r.mu.Lock()
	r.affinity[k] = Affinity{acct, time.Now().Add(r.snap.StickyTTL)}
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
