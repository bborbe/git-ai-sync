package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock/clocktest"
	"github.com/bborbe/git-ai-sync/internal/git"
)

type fixture struct {
	root   string
	remote string
	clone  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		root:   root,
		remote: filepath.Join(root, "remote.git"),
		clone:  filepath.Join(root, "clone"),
	}
	runGit(t, root, "init", "--bare", f.remote)
	runGit(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/master")
	runGit(t, root, "clone", f.remote, f.clone)
	configure(t, f.clone)
	writeFile(t, f.clone, "note.md", "hello\n")
	writeFile(t, f.clone, ".gitignore", "/.git-ai-sync.lock\n")
	runGit(t, f.clone, "add", ".")
	runGit(t, f.clone, "commit", "-m", "init")
	runGit(t, f.clone, "push", "-u", "origin", "master")
	return f
}

func (f fixture) addRemoteCommit(t *testing.T, name, content string) string {
	t.Helper()
	other := filepath.Join(f.root, "other-"+strings.ReplaceAll(name, "/", "-"))
	runGit(t, f.root, "clone", f.remote, other)
	configure(t, other)
	writeFile(t, other, name, content)
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote: "+name)
	runGit(t, other, "push", "origin", "master")
	return runGit(t, f.remote, "rev-parse", "master")
}

func configure(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func isAncestor(t *testing.T, dir, ancestor, descendant string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func countAutoCommits(t *testing.T, dir string) int {
	t.Helper()
	subjects := runGit(t, dir, "log", "--format=%s")
	count := 0
	for _, line := range strings.Split(subjects, "\n") {
		if strings.HasPrefix(line, "auto:") {
			count++
		}
	}
	return count
}

func fakeClock() *clocktest.Fake {
	return clocktest.NewFake(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
}

func TestDirtyCycleLandsEditAndRemoteCommit(t *testing.T) {
	for _, strategy := range []git.Strategy{git.StrategyMerge, git.StrategyRebase} {
		t.Run(string(strategy), func(t *testing.T) {
			f := newFixture(t)
			writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")
			remoteSHA := f.addRemoteCommit(t, "remote.txt", "remote\n")

			result, err := Run(context.Background(), f.clone, Options{
				Strategy:      strategy,
				CommitPrefix:  "auto",
				PullWhenClean: true,
				Clock:         fakeClock(),
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !result.HadChanges || !result.Committed || !result.Pulled || !result.Pushed {
				t.Errorf("unexpected result: %+v", result)
			}

			if got := runGit(t, f.clone, "status", "--porcelain"); got != "" {
				t.Errorf("(a) worktree not clean: %q", got)
			}
			if got := runGit(t, f.clone, "rev-list", "--count", "origin/master..HEAD"); got != "0" {
				t.Errorf("(b) unpushed commits = %s, want 0", got)
			}
			if got, want := runGit(t, f.clone, "rev-parse", "HEAD"), runGit(t, f.remote, "rev-parse", "master"); got != want {
				t.Errorf("(c) clone HEAD = %s, remote master = %s", got, want)
			}
			if got := runGit(t, f.remote, "show", "master:note.md"); !strings.Contains(got, "local edit") {
				t.Errorf("(d) remote note.md = %q, want the local edit", got)
			}
			if got := countAutoCommits(t, f.clone); got != 1 {
				t.Errorf("(e) auto commits = %d, want 1", got)
			}
			if !isAncestor(t, f.clone, remoteSHA, "HEAD") {
				t.Errorf("(f) remote commit %s is not an ancestor of HEAD", remoteSHA)
			}
		})
	}
}

func TestPullOnlyDoesNotSatisfy(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")
	f.addRemoteCommit(t, "remote.txt", "remote\n")

	cmd := exec.Command("git", "pull", "--no-rebase")
	cmd.Dir = f.clone
	_ = cmd.Run()

	if got := runGit(t, f.remote, "show", "master:note.md"); strings.Contains(got, "local edit") {
		t.Errorf("(d) the edit reached the remote without a commit: %q", got)
	}
	if got := countAutoCommits(t, f.clone); got != 0 {
		t.Errorf("(e) auto commits = %d, want 0", got)
	}
}

func TestCleanWorktreePullsInWatchMode(t *testing.T) {
	f := newFixture(t)
	remoteSHA := f.addRemoteCommit(t, "remote.txt", "remote\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Pulled || result.HadChanges {
		t.Errorf("unexpected result: %+v", result)
	}
	if got := runGit(t, f.clone, "rev-list", "--count", "HEAD..origin/master"); got != "0" {
		t.Errorf("HEAD..origin/master = %s, want 0", got)
	}
	if !isAncestor(t, f.clone, remoteSHA, "HEAD") {
		t.Errorf("remote commit %s is not an ancestor of HEAD", remoteSHA)
	}
}

func TestCleanWorktreeOneShotDoesNotPull(t *testing.T) {
	f := newFixture(t)
	remoteSHA := f.addRemoteCommit(t, "remote.txt", "remote\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: false,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.NoChanges {
		t.Errorf("NoChanges = false, want true: %+v", result)
	}
	if isAncestor(t, f.clone, remoteSHA, "HEAD") {
		t.Error("one-shot sync pulled on a clean worktree")
	}
	if got, want := runGit(t, f.clone, "rev-parse", "HEAD"), runGit(t, f.remote, "rev-parse", "master"); got == want {
		t.Errorf("clone HEAD equals the remote master %s; the one-shot pulled", got)
	}
}

func TestCommitRefusalStillPulls(t *testing.T) {
	f := newFixture(t)
	hook := filepath.Join(f.clone, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")
	remoteSHA := f.addRemoteCommit(t, "remote.txt", "remote\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.CommitErr == nil {
		t.Fatal("CommitErr is nil, want the refusal")
	}
	if !isAncestor(t, f.clone, remoteSHA, "HEAD") {
		t.Errorf("remote commit %s is not an ancestor of HEAD; the pull did not run", remoteSHA)
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); !strings.Contains(got, "note.md") {
		t.Errorf("the refused edit is no longer in the worktree: %q", got)
	}
}

func TestPushRejectedKeepsLocalCommit(t *testing.T) {
	f := newFixture(t)
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
	before := runGit(t, f.remote, "rev-parse", "master")
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("Run succeeded, want a push error")
	}
	if result.CommitErr != nil {
		t.Errorf("CommitErr = %v, want nil", result.CommitErr)
	}
	if after := runGit(t, f.remote, "rev-parse", "master"); after != before {
		t.Errorf("remote HEAD moved from %s to %s", before, after)
	}
	if got := runGit(t, f.clone, "log", "-1", "--format=%s"); !strings.HasPrefix(got, "auto:") {
		t.Errorf("local commit subject = %q, want an auto commit", got)
	}
}

func TestRemoteUnreachableKeepsLocalCommit(t *testing.T) {
	f := newFixture(t)
	runGit(t, f.clone, "remote", "set-url", "origin", filepath.Join(f.root, "missing.git"))
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("Run succeeded, want a pull error")
	}
	if result.CommitErr != nil {
		t.Errorf("CommitErr = %v, want nil", result.CommitErr)
	}
	if got := runGit(t, f.clone, "log", "-1", "--format=%s"); !strings.HasPrefix(got, "auto:") {
		t.Errorf("local commit subject = %q, want an auto commit", got)
	}
}
