package policy

import (
	"crypto/hmac"
	"github.com/1jehuang/2papi/internal/config"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Bucket struct {
	tokens float64
	last   time.Time
	rpm    int
}
type Auth struct {
	snap    *config.Snapshot
	mu      sync.Mutex
	buckets map[string]*Bucket
}

func New(s *config.Snapshot) *Auth { return &Auth{snap: s, buckets: map[string]*Bucket{}} }
func (a *Auth) Authenticate(r *http.Request) (config.VirtualKey, bool) {
	h := r.Header.Get("Authorization")
	key := strings.TrimPrefix(h, "Bearer ")
	if key == h || key == "" {
		return config.VirtualKey{}, false
	}
	got := a.snap.HashPresented(key)
	for _, vk := range a.snap.VirtualKeys {
		if hsh := a.snap.KeyHashes[vk.Name]; hmac.Equal(got, hsh) {
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
func (a *Auth) AllowRate(v config.VirtualKey) bool {
	if v.RPM <= 0 {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.buckets[v.Name]
	now := time.Now()
	if b == nil {
		b = &Bucket{tokens: float64(v.RPM), last: now, rpm: v.RPM}
		a.buckets[v.Name] = b
	}
	elapsed := now.Sub(b.last).Minutes()
	b.tokens += elapsed * float64(b.rpm)
	if b.tokens > float64(b.rpm) {
		b.tokens = float64(b.rpm)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
