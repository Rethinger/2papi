package resilience

import (
	"testing"
	"time"
)

func TestCooldownAndCircuitReset(t *testing.T) {
	s := New()
	if s.Cooling("a") || s.CircuitOpen("a", time.Second) {
		t.Fatal("new account should be healthy")
	}

	s.Cooldown("a", 15*time.Millisecond)
	if !s.Cooling("a") {
		t.Fatal("cooldown was not applied")
	}
	time.Sleep(20 * time.Millisecond)
	if s.Cooling("a") {
		t.Fatal("cooldown did not expire")
	}

	s.Failure("a", 2)
	if s.CircuitOpen("a", time.Second) {
		t.Fatal("circuit opened before threshold")
	}
	s.Failure("a", 2)
	if !s.CircuitOpen("a", time.Second) {
		t.Fatal("circuit did not open at threshold")
	}
	time.Sleep(12 * time.Millisecond)
	if s.CircuitOpen("a", 5*time.Millisecond) {
		t.Fatal("circuit did not reset")
	}
}

func TestConcurrencyAndLatencyEWMA(t *testing.T) {
	s := New()
	if !s.TryAcquire("a", 1) {
		t.Fatal("first concurrency slot rejected")
	}
	if s.TryAcquire("a", 1) {
		t.Fatal("concurrency limit was not enforced")
	}
	if got := s.Active("a"); got != 1 {
		t.Fatalf("active=%d, want 1", got)
	}
	s.Release("a")
	if got := s.Active("a"); got != 0 {
		t.Fatalf("active=%d, want 0", got)
	}

	s.Success("a", 80*time.Millisecond)
	s.Success("a", 160*time.Millisecond)
	if got, want := s.Latency("a"), 90*time.Millisecond; got != want {
		t.Fatalf("latency=%s, want %s", got, want)
	}

	s.Failure("a", 1)
	if !s.CircuitOpen("a", time.Second) {
		t.Fatal("expected open circuit")
	}
	s.Success("a", 90*time.Millisecond)
	// A success clears consecutive failures but an already-open circuit stays open
	// until its reset window, which prevents flapping.
	if !s.CircuitOpen("a", time.Second) {
		t.Fatal("open circuit should remain open until reset window")
	}
}

func TestLockoutAndCounters(t *testing.T) {
	s := New()
	if s.LockedOut("a") {
		t.Fatal("new account should not be locked out")
	}
	s.Lockout("a", 15*time.Millisecond)
	if !s.LockedOut("a") {
		t.Fatal("lockout should be active")
	}
	time.Sleep(20 * time.Millisecond)
	if s.LockedOut("a") {
		t.Fatal("lockout should have expired")
	}

	s.Lockout("a", time.Hour)
	s.ResetLockout("a")
	if s.LockedOut("a") {
		t.Fatal("lockout should be cleared on reset")
	}

	s.Success("a", 50*time.Millisecond)
	s.Failure("a", 3)
	if s.Successes("a") != 1 || s.Fails("a") != 1 || s.TotalRequests("a") != 2 {
		t.Fatalf("counters mismatch: succ=%d fails=%d total=%d", s.Successes("a"), s.Fails("a"), s.TotalRequests("a"))
	}
}
