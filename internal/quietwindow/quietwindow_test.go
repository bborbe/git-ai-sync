package quietwindow

import (
	"testing"
	"time"
)

func TestDecayedWindowPinned(t *testing.T) {
	want := []time.Duration{30 * time.Second, 15 * time.Second, 7500 * time.Millisecond, 3750 * time.Millisecond}
	for k, expected := range want {
		if got := DecayedWindow(30*time.Second, k); got != expected {
			t.Errorf("DecayedWindow(30s, %d) = %v, want %v", k, got, expected)
		}
	}
}

func TestDecayedWindowHalvesEveryStep(t *testing.T) {
	if got := DecayedWindow(30*time.Second, 0); got != 30*time.Second {
		t.Fatalf("DecayedWindow(30s, 0) = %v, want 30s", got)
	}
	for k := 1; k <= 10; k++ {
		previous := DecayedWindow(30*time.Second, k-1)
		got := DecayedWindow(30*time.Second, k)
		if got != previous/2 {
			t.Errorf("step %d: %v is not half of %v", k, got, previous)
		}
	}
}

func TestNegativeSkipsTreatedAsZero(t *testing.T) {
	if got := DecayedWindow(30*time.Second, -3); got != 30*time.Second {
		t.Fatalf("DecayedWindow(30s, -3) = %v, want 30s", got)
	}
}

func TestDecayResetsAfterSuccess(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTracker(t0)
	tracker.RecordSkip()
	tracker.RecordSkip()
	if got := tracker.Window(30 * time.Second); got != 7500*time.Millisecond {
		t.Fatalf("window after two skips = %v, want 7.5s", got)
	}
	if got := tracker.ConsecutiveSkips(); got != 2 {
		t.Fatalf("ConsecutiveSkips = %d, want 2", got)
	}

	now := t0.Add(90 * time.Second)
	tracker.RecordSuccess(now)
	if got := tracker.Window(30 * time.Second); got != 30*time.Second {
		t.Errorf("window after success = %v, want 30s", got)
	}
	if got := tracker.ConsecutiveSkips(); got != 0 {
		t.Errorf("ConsecutiveSkips after success = %d, want 0", got)
	}
	if got := tracker.ElapsedSinceSuccess(now); got != 0 {
		t.Errorf("ElapsedSinceSuccess after success = %v, want 0", got)
	}
}

func TestFailedCycleLeavesDecayAndClockUntouched(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTracker(t0)
	tracker.RecordSkip()
	tracker.RecordSkip()

	at10 := t0.Add(10 * time.Second)
	if got := tracker.Window(30 * time.Second); got != 7500*time.Millisecond {
		t.Fatalf("window at 10s = %v, want 7.5s", got)
	}
	if got := tracker.ElapsedSinceSuccess(at10); got != 10*time.Second {
		t.Fatalf("elapsed at 10s = %v, want 10s", got)
	}

	// A failed cycle does not call RecordSuccess.
	at20 := t0.Add(20 * time.Second)
	if got := tracker.Window(30 * time.Second); got != 7500*time.Millisecond {
		t.Errorf("window at 20s = %v, want 7.5s", got)
	}
	if got := tracker.ElapsedSinceSuccess(at20); got != 20*time.Second {
		t.Errorf("elapsed at 20s = %v, want 20s", got)
	}
}

func TestCapFiresAtExactMaxWait(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTracker(t0)

	before := tracker.Decide(time.Second, 30*time.Second, 300*time.Second, t0.Add(299*time.Second))
	if before.CapFired {
		t.Error("cap fired at 299s, want false")
	}

	at := tracker.Decide(time.Second, 30*time.Second, 300*time.Second, t0.Add(300*time.Second))
	if !at.CapFired {
		t.Error("cap did not fire at exactly 300s")
	}
	if !at.Sync {
		t.Error("cycle not accepted at exactly 300s")
	}
}

func TestWindowBoundaryUsesAtLeast(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTracker(t0)
	tracker.RecordSkip()

	if got := tracker.Decide(15*time.Second, 30*time.Second, 300*time.Second, t0); !got.Sync {
		t.Error("change exactly at the window boundary should sync")
	}
	if got := tracker.Decide(14*time.Second+999*time.Millisecond, 30*time.Second, 300*time.Second, t0); got.Sync {
		t.Error("change just inside the window should not sync")
	}
}

func TestDecideIsPure(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTracker(t0)
	tracker.RecordSkip()

	_ = tracker.Decide(20*time.Second, 30*time.Second, 300*time.Second, t0)
	_ = tracker.Decide(20*time.Second, 30*time.Second, 300*time.Second, t0)

	if got := tracker.ConsecutiveSkips(); got != 1 {
		t.Errorf("ConsecutiveSkips = %d, want 1", got)
	}
	if got := tracker.Window(30 * time.Second); got != 15*time.Second {
		t.Errorf("Window = %v, want 15s", got)
	}
	if got := tracker.ElapsedSinceSuccess(t0); got != 0 {
		t.Errorf("ElapsedSinceSuccess = %v, want 0", got)
	}
}
