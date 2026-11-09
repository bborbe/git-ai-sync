// Package quietwindow decides when a watch cycle may sync.
package quietwindow

import (
	"math"
	"time"
)

// DecayedWindow returns the quiet window required after consecutiveSkips skips.
// It starts at interval and halves on every consecutive skip. Negative skip
// counts are treated as zero. The result is not clamped or rounded.
func DecayedWindow(interval time.Duration, consecutiveSkips int) time.Duration {
	if consecutiveSkips < 0 {
		consecutiveSkips = 0
	}
	return time.Duration(float64(interval) * math.Pow(0.5, float64(consecutiveSkips)))
}

// Decision is one watch cycle's debounce verdict.
type Decision struct {
	Sync     bool
	Window   time.Duration
	CapFired bool
}

// Tracker holds the consecutive-skip count and the last successful sync time.
type Tracker struct {
	consecutiveSkips int
	lastSuccess      time.Time
}

// NewTracker starts with a zero skip count and the caller's clock reading.
func NewTracker(now time.Time) *Tracker {
	return &Tracker{lastSuccess: now}
}

// ConsecutiveSkips reports the consecutive skipped cycles since the last success.
func (t *Tracker) ConsecutiveSkips() int {
	return t.consecutiveSkips
}

// Window returns the quiet window in force, in seconds.
func (t *Tracker) Window(interval time.Duration) time.Duration {
	return DecayedWindow(interval, t.consecutiveSkips)
}

// ElapsedSinceSuccess reports the time since the last successful sync.
func (t *Tracker) ElapsedSinceSuccess(now time.Time) time.Duration {
	return now.Sub(t.lastSuccess)
}

// Decide reports whether the cycle may sync. The cycle is accepted when the
// newest change is at least as old as the window in force, or when the maximum
// wait has elapsed since the last success — whichever happens first.
func (t *Tracker) Decide(
	secondsSinceChange time.Duration,
	interval time.Duration,
	maxWait time.Duration,
	now time.Time,
) Decision {
	window := t.Window(interval)
	capFired := t.ElapsedSinceSuccess(now) >= maxWait
	return Decision{
		Sync:     capFired || secondsSinceChange >= window,
		Window:   window,
		CapFired: capFired,
	}
}

// RecordSkip counts one skipped cycle, halving the window for the next one.
func (t *Tracker) RecordSkip() {
	t.consecutiveSkips++
}

// RecordSuccess resets the decay and restarts the maximum-wait clock.
func (t *Tracker) RecordSuccess(now time.Time) {
	t.consecutiveSkips = 0
	t.lastSuccess = now
}
