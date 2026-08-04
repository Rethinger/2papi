package resilience

import (
	"sync"
	"time"
)

type State struct {
	mu        sync.Mutex
	cooldown  map[string]time.Time
	fails     map[string]int
	opened    map[string]time.Time
	active    map[string]int
	latency   map[string]time.Duration
	successes map[string]int
}

func New() *State {
	return &State{cooldown: map[string]time.Time{}, fails: map[string]int{}, opened: map[string]time.Time{}, active: map[string]int{}, latency: map[string]time.Duration{}, successes: map[string]int{}}
}
func (s *State) Cooling(n string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *State) Cooldown(n string, d time.Duration) {
	s.mu.Lock()
	s.cooldown[n] = time.Now().Add(d)
	s.mu.Unlock()
}
func (s *State) Failure(n string, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[n]++
	if s.fails[n] >= max {
		s.opened[n] = time.Now()
	}
}
func (s *State) Success(n string, lat time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[n] = 0
	s.successes[n]++
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
	if s.active[n] >= max {
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
func (s *State) Active(n string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.active[n] }
func (s *State) Latency(n string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latency[n]
}
