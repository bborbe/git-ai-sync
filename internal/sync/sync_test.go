package sync

import (
	"context"
	stderrors "errors"
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
	configure(t, f.remote)
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
	// Pin core.hooksPath to this repository's own hooks directory so a global
	// core.hooksPath on the developer's machine cannot redirect git away from
	// the hooks these tests write. --absolute-git-dir yields <clone>/.git for a
	// clone and <bare> for a bare repository; --git-path hooks would return the
	// effective (global) hooks path and reproduce the bug.
	hooksPath := filepath.Join(runGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	runGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
	// Pin core.quotePath so a non-ASCII path cannot be silently unquoted by a
	// global false; StagedFiles must unquote via -z regardless.
	runGit(t, dir, "config", "--local", "core.quotePath", "true")
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

// TestRunRefusesConflictedRepository proves the cycle never stages or commits a
// repository that is still mid-merge, and routes it back to the resolver by
// wrapping git.ErrConflict.
func TestRunRefusesConflictedRepository(t *testing.T) {
	f := newFixture(t)
	seedConflict(t, f, "pull", "--no-rebase")
	before := runGit(t, f.clone, "rev-parse", "HEAD")

	_, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("Run succeeded on a conflicted repository, want an error")
	}
	if !stderrors.Is(err, git.ErrConflict) {
		t.Errorf("error %v does not wrap git.ErrConflict", err)
	}
	if !fileExists(filepath.Join(f.clone, ".git", "MERGE_HEAD")) {
		t.Error(".git/MERGE_HEAD was removed; Run staged or committed the conflict")
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); !strings.Contains(got, "note.md") {
		t.Errorf("status does not list the conflicted path: %q", got)
	}
	if after := runGit(t, f.clone, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s; Run committed the conflict", before, after)
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

// installRefusalHook writes a pre-commit hook that refuses the named path. It
// decides from the index git presents to the hook: under --only that is the
// named subset, not the whole index.
func installRefusalHook(t *testing.T, repo, blocked string) {
	t.Helper()
	hooksDir := filepath.Join(runGit(t, repo, "rev-parse", "--absolute-git-dir"), "hooks")
	script := "#!/bin/sh\nif git diff --cached --name-only | grep -qx '" + blocked +
		"'; then\n  exit 1\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}
}

func TestHookRefusalCommitsTheRest(t *testing.T) {
	f := newFixture(t)
	installRefusalHook(t, f.clone, "blocked.txt")
	writeFile(t, f.clone, "blocked.txt", "blocked\n")
	writeFile(t, f.clone, "normal.txt", "normal\n")
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
	names := runGit(t, f.clone, "log", "--name-only", "HEAD")
	if !strings.Contains(names, "normal.txt") {
		t.Errorf("normal.txt was not committed: %q", names)
	}
	if strings.Contains(names, "blocked.txt") {
		t.Errorf("blocked.txt was committed: %q", names)
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); !strings.Contains(got, "blocked.txt") {
		t.Errorf("blocked.txt is no longer staged: %q", got)
	}
	if !result.Committed {
		t.Errorf("Committed = false, want true: %+v", result)
	}
	if len(result.Blocked) != 1 || result.Blocked[0] != "blocked.txt" {
		t.Errorf("Blocked = %v, want [blocked.txt]", result.Blocked)
	}
	if result.CommitErr == nil {
		t.Fatal("CommitErr is nil, want the refusal")
	}
	if !strings.Contains(result.CommitErr.Error(), "blocked.txt") {
		t.Errorf("CommitErr = %q, want it to name blocked.txt", result.CommitErr)
	}
	// The autostash pull merged the remote commit, and the accepted commit left
	// the machine.
	if !isAncestor(t, f.clone, remoteSHA, "HEAD") {
		t.Errorf("remote commit %s is not an ancestor of HEAD", remoteSHA)
	}
	if !result.Pushed {
		t.Errorf("Pushed = false, want true: %+v", result)
	}
	remoteNames := runGit(t, f.remote, "log", "--name-only", "master")
	if !strings.Contains(remoteNames, "normal.txt") {
		t.Errorf("normal.txt is not on the remote: %q", remoteNames)
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); !strings.Contains(got, "blocked.txt") {
		t.Errorf("blocked.txt is no longer staged after the pull: %q", got)
	}
	if got := runGit(t, f.clone, "diff", "--cached", "--name-only"); !strings.Contains(got, "blocked.txt") {
		t.Errorf("blocked.txt is no longer in the index after the pull: %q", got)
	}
}

func TestAutostashPopConflictStopsCycle(t *testing.T) {
	f := newFixture(t)
	installRefusalHook(t, f.clone, "blocked.txt")
	// The accepted path keeps Blocked non-empty, which is what makes runDirty
	// choose PullWithAutostash; with no accepted path it would pick plain Pull.
	writeFile(t, f.clone, "blocked.txt", "local\n")
	writeFile(t, f.clone, "normal.txt", "normal\n")
	// The blocked path also changes upstream, so reapplying the autostash
	// conflicts and leaves an unmerged entry with markers in the worktree.
	f.addRemoteCommit(t, "blocked.txt", "remote\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("Run succeeded, want an autostash pop conflict")
	}
	if !stderrors.Is(err, git.ErrConflict) {
		t.Errorf("error does not wrap ErrConflict: %v", err)
	}
	if len(result.Blocked) != 1 || result.Blocked[0] != "blocked.txt" {
		t.Errorf("Blocked = %v, want [blocked.txt]", result.Blocked)
	}
	if got := runGit(t, f.clone, "log", "--all", "-S", "<<<<<<<"); got != "" {
		t.Errorf("a commit contains conflict markers:\n%s", got)
	}
}

func TestPreexistingUnmergedPathStopsCycle(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.clone, "note.md", "local\n")
	runGit(t, f.clone, "add", "note.md")
	runGit(t, f.clone, "commit", "-m", "local edit")
	f.addRemoteCommit(t, "note.md", "remote\n")

	// Leave the clone with an unmerged UU entry and a MERGE_HEAD in place.
	cmd := exec.Command("git", "pull", "--no-rebase")
	cmd.Dir = f.clone
	_ = cmd.Run()

	before := runGit(t, f.clone, "rev-parse", "HEAD")
	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("Run succeeded, want an unmerged-path error")
	}
	if !stderrors.Is(err, git.ErrConflict) {
		t.Errorf("error does not wrap ErrConflict: %v", err)
	}
	if result.Committed {
		t.Errorf("Committed = true, want false: %+v", result)
	}
	if after := runGit(t, f.clone, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s; a commit was created", before, after)
	}
}

func TestGlobalCommitFailureDoesNotBisect(t *testing.T) {
	f := newFixture(t)
	counter := filepath.Join(t.TempDir(), "hook-count")
	hooksDir := filepath.Join(runGit(t, f.clone, "rev-parse", "--absolute-git-dir"), "hooks")
	script := "#!/bin/sh\necho x >> '" + counter + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}
	for i := 0; i < 20; i++ {
		writeFile(t, f.clone, "unit-"+string(rune('a'+i))+".txt", "x\n")
	}

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run returned an error, want the refusal in CommitErr: %v", err)
	}
	if result.CommitErr == nil {
		t.Fatal("CommitErr is nil, want the original commit error")
	}
	if len(result.Blocked) != 0 {
		t.Errorf("Blocked = %v, want empty for a global failure", result.Blocked)
	}
	data, readErr := os.ReadFile(counter)
	if readErr != nil {
		t.Fatalf("read hook counter: %v", readErr)
	}
	if got := strings.Count(string(data), "\n"); got > 6 {
		t.Errorf("hook ran %d times, want at most 6 (full commit plus three probes)", got)
	}
}

func TestHookRefusalAllPathsBlocked(t *testing.T) {
	f := newFixture(t)
	installRefusalHook(t, f.clone, "blocked.txt")
	writeFile(t, f.clone, "blocked.txt", "blocked\n")

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.Committed {
		t.Errorf("Committed = true, want false: %+v", result)
	}
	if result.CommitErr == nil {
		t.Fatal("CommitErr is nil, want the refusal")
	}
	if len(result.Blocked) != 0 {
		t.Errorf("Blocked = %v, want empty", result.Blocked)
	}
}
