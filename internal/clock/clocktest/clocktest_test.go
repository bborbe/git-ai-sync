package clocktest

import (
	"testing"
	"time"
)

func TestFakeNowAdvanceAndSetNow(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := NewFake(start)

	if got := fake.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	fake.Advance(90 * time.Second)
	if got, want := fake.Now(), start.Add(90*time.Second); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}

	other := time.Date(2027, 6, 7, 8, 9, 10, 0, time.UTC)
	fake.SetNow(other)
	if got := fake.Now(); !got.Equal(other) {
		t.Fatalf("after SetNow, Now() = %v, want %v", got, other)
	}
}
