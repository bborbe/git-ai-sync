// Package clocktest provides a controllable clock for tests.
package clocktest

import (
	"sync"
	"time"
)

// Fake is a Clock whose time is set by the test.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake clock reading now.
func NewFake(now time.Time) *Fake {
	return &Fake{now: now}
}

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// SetNow sets the fake's current time.
func (f *Fake) SetNow(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// Advance moves the fake's current time forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
