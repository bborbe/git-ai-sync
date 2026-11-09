package changetracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/clock/clocktest"
)

func TestShouldIgnore(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		isDir bool
		want  bool
	}{
		{name: "directory", path: "/repo/sub", isDir: true, want: true},
		{name: "git head", path: "/repo/.git/HEAD", want: true},
		{name: "git dir", path: "/repo/.git", want: true},
		{name: "backup", path: "/repo/note.md~", want: true},
		{name: "swap", path: "/repo/note.swp", want: true},
		{name: "temp", path: "/repo/note.tmp", want: true},
		{name: "hidden", path: "/repo/.DS_Store", want: true},
		{name: "ordinary", path: "/repo/note.md", want: false},
		{name: "ordinary nested", path: "/repo/sub/note.md", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldIgnore(tc.path, tc.isDir); got != tc.want {
				t.Errorf("ShouldIgnore(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
			}
		})
	}
}

func TestHandleEventResetsOnlyForOrdinaryFiles(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := clocktest.NewFake(t0)
	dir := t.TempDir()
	tracker := New(dir, fake)

	ordinary := writeFile(t, dir, "note.md")
	tracker.HandleEvent(ordinary, false)
	if got := tracker.SecondsSinceLastChange(t0); got != 0 {
		t.Fatalf("after ordinary write, SecondsSinceLastChange = %v, want 0", got)
	}

	fake.Advance(5 * time.Second)
	at5 := t0.Add(5 * time.Second)

	ignored := []string{
		writeFile(t, dir, "note.tmp"),
		writeFile(t, dir, "note.md~"),
		writeFile(t, dir, "note.swp"),
		writeFile(t, dir, ".DS_Store"),
		writeFile(t, filepath.Join(dir, ".git"), "HEAD"),
	}
	for _, path := range ignored {
		tracker.HandleEvent(path, false)
	}
	tracker.HandleEvent(filepath.Join(dir, "sub"), true)

	if got := tracker.SecondsSinceLastChange(at5); got != 5*time.Second {
		t.Fatalf("after ignored events, SecondsSinceLastChange = %v, want 5s", got)
	}

	fake.Advance(5 * time.Second)
	at10 := t0.Add(10 * time.Second)
	tracker.HandleEvent(writeFile(t, dir, "second.md"), false)
	if got := tracker.SecondsSinceLastChange(at10); got != 0 {
		t.Fatalf("after second ordinary write, SecondsSinceLastChange = %v, want 0", got)
	}
}

func TestSecondsSinceLastChangeNeverChanged(t *testing.T) {
	dir := t.TempDir()
	tracker := New(dir, clocktest.NewFake(time.Now()))
	if got := tracker.SecondsSinceLastChange(time.Now()); got != NeverChanged {
		t.Fatalf("SecondsSinceLastChange = %v, want NeverChanged", got)
	}
}

func TestStartStopDeliversOrdinaryEvents(t *testing.T) {
	dir := t.TempDir()
	tracker := New(dir, clock.New())
	if err := tracker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tracker.Stop()

	writeFile(t, dir, "note.md")

	var seen time.Duration
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		seen = tracker.SecondsSinceLastChange(time.Now())
		if seen < time.Second {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seen >= time.Second {
		t.Fatalf("ordinary write was not observed, SecondsSinceLastChange = %v", seen)
	}

	time.Sleep(20 * time.Millisecond)
	before := tracker.SecondsSinceLastChange(time.Now())
	writeFile(t, dir, "noise.tmp")
	time.Sleep(100 * time.Millisecond)
	after := tracker.SecondsSinceLastChange(time.Now())
	if after < before {
		t.Fatalf("ignored .tmp write reset the tracker: before=%v after=%v", before, after)
	}
}

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
