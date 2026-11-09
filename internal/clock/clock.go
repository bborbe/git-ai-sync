// Package clock supplies the current time to timing-sensitive code.
package clock

import "time"

// Clock supplies the current time. Production uses New(); tests use clocktest.NewFake.
type Clock interface {
	Now() time.Time
}

// New returns a Clock backed by time.Now, which carries a monotonic reading.
func New() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
