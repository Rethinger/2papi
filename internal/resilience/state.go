package resilience

import (
	"sync"
	"time"
)

type State struct {
	mu        sync.RWMutex
	cooldown  map[string]time.Time
	fails     map[string]int
	opened    map[string]time.Time
	active    map[string]int
	latency   map[string]time.Duration
	successes map[string]int
	requests  map[string]int
	lockouts  map[string]time.Time
}

func New() *State {
	return &State{
		cooldown:  map[string]time.Time{},
		fails:     map[string]int{},
		opened:    map[string]time.Time{},
		active:    map[string]int{},
		latency:   map[string]time.Duration{},
		successes: map[string]int{},
		requests:  map[string]int{},
		lockouts:  map[string]time.Time{},
	}
}

func (s *State) Cooling(n string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Now().Before(s.cooldown[n])
}

func (s *State) CircuitOpen(n string, reset time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.opened[n]
	if !ok {
		return false
	}
	if time.Since(t) > reset {
		delete(s.opened, n)
		s.fails[n] = 0
		return false
	}
	return true
}

func (s *State) LockedOut(n string) bool {
	s.mu.RLock()
	t, ok := s.lockouts[n]
	if !ok {
		s.mu.RUnlock()
		return false
	}
	if time.Now().After(t) {
		s.mu.RUnlock()
		s.mu.Lock()
		// re-check after upgrade
		if lt, ok := s.lockouts[n]; ok && time.Now().After(lt) {
			delete(s.lockouts, n)
		}
		s.mu.Unlock()
		return false
	}
	s.mu.RUnlock()
	return true
}

func (s *State) Lockout(n string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d <= 0 {
		return
	}
	s.lockouts[n] = time.Now().Add(d)
}

func (s *State) ResetLockout(n string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lockouts, n)
}

func (s *State) Cooldown(n string, d time.Duration) {
	s.mu.Lock()
	s.cooldown[n] = time.Now().Add(d)
	s.mu.Unlock()
}

func (s *State) Failure(n string, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[n]++
	s.requests[n]++
	if max > 0 && s.fails[n] >= max {
		s.opened[n] = time.Now()
	}
}

func (s *State) Success(n string, lat time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[n] = 0
	s.successes[n]++
	s.requests[n]++
	old := s.latency[n]
	if old == 0 {
		s.latency[n] = lat
	} else {
		s.latency[n] = (old*7 + lat) / 8
	}
}

func (s *State) TryAcquire(n string, max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max > 0 && s.active[n] >= max {
		return false
	}
	s.active[n]++
	return true
}

func (s *State) Release(n string) {
	s.mu.Lock()
	if s.active[n] > 0 {
		s.active[n]--
	}
	s.mu.Unlock()
}

func (s *State) Active(n string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active[n]
}

func (s *State) Latency(n string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latency[n]
}

func (s *State) TotalRequests(n string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.requests[n]
}

func (s *State) Successes(n string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.successes[n]
}

func (s *State) Fails(n string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fails[n]
}
