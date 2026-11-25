package changetracker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/clock/clocktest"
)

func TestPollRecordsOnlyDirtyTransitions(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		first      string
		second     string
		wantChange bool
	}{
		{name: "empty to empty", first: "", second: "", wantChange: false},
		{name: "empty to dirty", first: "", second: " M note.md\n", wantChange: true},
		{name: "dirty to same dirty", first: " M note.md\n", second: " M note.md\n", wantChange: false},
		{name: "dirty to different dirty", first: " M note.md\n", second: "?? other.md\n", wantChange: true},
		{name: "dirty to clean", first: " M note.md\n", second: "", wantChange: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := clocktest.NewFake(t0)
			tracker := New(t.TempDir(), fake)
			status := tc.first
			tracker.gitStatus = func(context.Context, string) (string, error) {
				return status, nil
			}

			tracker.poll(context.Background())
			fake.Advance(5 * time.Second)
			at5 := t0.Add(5 * time.Second)
			status = tc.second
			tracker.poll(context.Background())

			got := tracker.SecondsSinceLastChange(at5)
			if tc.wantChange {
				if got != 0 {
					t.Fatalf("SecondsSinceLastChange = %v, want 0 (change recorded at poll time)", got)
				}
				return
			}
			if got != NeverChanged {
				t.Fatalf("SecondsSinceLastChange = %v, want NeverChanged", got)
			}
		})
	}
}

func TestPollErrorLeavesLastChangeUntouched(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := clocktest.NewFake(t0)
	tracker := New(t.TempDir(), fake)
	calls := 0
	tracker.gitStatus = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "", nil
		}
		return "", errors.New("git status failed")
	}

	tracker.poll(context.Background())
	fake.Advance(5 * time.Second)
	tracker.poll(context.Background())

	if got := tracker.SecondsSinceLastChange(t0.Add(5 * time.Second)); got != NeverChanged {
		t.Fatalf("SecondsSinceLastChange = %v, want NeverChanged after a status error", got)
	}
}

func TestSecondsSinceLastChangeNeverChanged(t *testing.T) {
	dir := t.TempDir()
	tracker := New(dir, clocktest.NewFake(time.Now()))
	if got := tracker.SecondsSinceLastChange(time.Now()); got != NeverChanged {
		t.Fatalf("SecondsSinceLastChange = %v, want NeverChanged", got)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	tracker := New(t.TempDir(), clock.New())
	tracker.gitStatus = func(context.Context, string) (string, error) {
		return "", nil
	}
	if err := tracker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tracker.Stop()
	tracker.Stop()
}

func TestTrackerDetectsRealGitChange(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	writeFile(t, repo, "seed.txt", "seed\n")
	runGit(t, repo, "add", "seed.txt")
	runGit(t, repo, "commit", "-m", "init")

	tracker := New(repo, clock.New())
	tracker.pollInterval = 50 * time.Millisecond
	if err := tracker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tracker.Stop()

	writeFile(t, repo, "seed.txt", "seed\nedited\n")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tracker.SecondsSinceLastChange(time.Now()) < NeverChanged {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tracked write was not observed, SecondsSinceLastChange = %v", tracker.SecondsSinceLastChange(time.Now()))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
