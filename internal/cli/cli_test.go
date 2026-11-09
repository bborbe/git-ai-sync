package cli

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bborbe/git-ai-sync/internal/lock"
)

func TestVersionPrintsOneLine(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	cmd := newRootCommand(context.Background())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Fatalf("version output has %d newlines, want 1: %q", got, out)
	}
	if !strings.HasPrefix(out, "git-ai-sync ") {
		t.Errorf("version output = %q, want a git-ai-sync prefix", out)
	}
}

func TestSyncCommandRejectsSecondInstance(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	initRepo(t, dir)

	holder, err := lock.Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer func() { _ = holder.Release(context.Background()) }()

	err = Run(context.Background(), []string{"sync", dir})
	if err == nil {
		t.Fatal("sync succeeded while the lock was held, want refusal")
	}
	if !stderrors.Is(err, lock.ErrLocked) {
		t.Errorf("error does not wrap ErrLocked: %v", err)
	}
}

func TestSyncCommandCleanRepo(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	initRepo(t, dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.git-ai-sync.lock\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	if err := Run(context.Background(), []string{"sync", dir}); err != nil {
		t.Fatalf("sync on a clean repository: %v", err)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
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

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GIT_AI_SYNC_INTERVAL",
		"GIT_AI_SYNC_MAX_WAIT",
		"GIT_AI_SYNC_COMMIT_PREFIX",
		"GIT_AI_SYNC_STRATEGY",
		"LOG_LEVEL",
	} {
		t.Setenv(name, "")
	}
}
