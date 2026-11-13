package sync

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// installRefusalHook writes a pre-commit hook that refuses any of the named
// paths. It decides from the index git presents to the hook: under --only that
// is the named subset, not the whole index.
func installRefusalHook(t *testing.T, repo string, blocked ...string) {
	t.Helper()
	hooksDir := filepath.Join(runGit(t, repo, "rev-parse", "--absolute-git-dir"), "hooks")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for _, path := range blocked {
		script.WriteString("if git diff --cached --name-only | grep -qx '" + path + "'; then\n  exit 1\nfi\n")
	}
	script.WriteString("exit 0\n")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(script.String()), 0o755); err != nil {
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

// TestRefusalsAtProbePositionsCommitTheRest stages six units and refuses the
// units at indices 0, 3 and 5 — exactly the three positions the removed probe
// heuristic sampled. Every probe failing must not be read as a global failure:
// the three accepted units still commit.
func TestRefusalsAtProbePositionsCommitTheRest(t *testing.T) {
	f := newFixture(t)
	refused := []string{"unit-0.txt", "unit-3.txt", "unit-5.txt"}
	installRefusalHook(t, f.clone, refused...)
	for i := 0; i < 6; i++ {
		writeFile(t, f.clone, "unit-"+string(rune('0'+i))+".txt", "x\n")
	}

	result, err := Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Committed {
		t.Errorf("Committed = false, want true: %+v", result)
	}
	// Bisection lands the accepted units in more than one commit, so inspect the
	// whole history rather than HEAD alone.
	names := runGit(t, f.clone, "log", "--name-only", "--format=")
	for _, path := range []string{"unit-1.txt", "unit-2.txt", "unit-4.txt"} {
		if !strings.Contains(names, path) {
			t.Errorf("%s was not committed: %q", path, names)
		}
	}
	staged := runGit(t, f.clone, "diff", "--cached", "--name-only")
	for _, path := range refused {
		if !containsLine(staged, path) {
			t.Errorf("%s is no longer staged: %q", path, staged)
		}
		if !containsString(result.Blocked, path) {
			t.Errorf("Blocked = %v, want it to name %s", result.Blocked, path)
		}
	}
	if len(result.Blocked) != len(refused) {
		t.Errorf("Blocked = %v, want %v", result.Blocked, refused)
	}
}

func containsLine(lines, want string) bool {
	for _, line := range strings.Split(lines, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAutostashPopConflictStopsCycle(t *testing.T) {
	f := newFixture(t)
	// Track blocked.txt on both sides first, so the autostash conflict is a
	// modify/modify (UU) rather than an add/add (AA) conflict.
	writeFile(t, f.clone, "blocked.txt", "base\n")
	runGit(t, f.clone, "add", "blocked.txt")
	runGit(t, f.clone, "commit", "-m", "base blocked")
	runGit(t, f.clone, "push")
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

	// The autostash is retained and the worktree still has unmerged entries, so a
	// second cycle reaches unmergedError's autostash branch — the first cycle's
	// error came from autostashConflict in internal/git instead.
	if got := runGit(t, f.clone, "stash", "list"); !strings.Contains(got, "autostash") {
		t.Fatalf("retained autostash is missing from stash list: %q", got)
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); !strings.Contains(got, "UU") {
		t.Fatalf("worktree has no unmerged entries: %q", got)
	}

	result, err = Run(context.Background(), f.clone, Options{
		Strategy:      git.StrategyMerge,
		CommitPrefix:  "auto",
		PullWhenClean: true,
		Clock:         fakeClock(),
	})
	if err == nil {
		t.Fatal("second Run succeeded, want an unmerged-path error")
	}
	if !stderrors.Is(err, git.ErrConflict) {
		t.Errorf("second error does not wrap ErrConflict: %v", err)
	}
	if !strings.Contains(err.Error(), "blocked.txt") {
		t.Errorf("second error does not name the unmerged path: %v", err)
	}
	if !strings.Contains(err.Error(), "stash drop") {
		t.Errorf("second error does not mention stash drop for a retained autostash: %v", err)
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
	// No autostash exists, so the recovery is git-ai-sync resolve, not a stash drop.
	if strings.Contains(err.Error(), "stash drop") {
		t.Errorf("error mentions stash drop without an autostash: %v", err)
	}
	// Run's merge guard returns before unmergedError, so call it directly to cover
	// the no-autostash branch: it must name the path and point at resolve.
	unmerged := unmergedError(context.Background(), f.clone)
	if !stderrors.Is(unmerged, git.ErrConflict) {
		t.Errorf("unmergedError does not wrap ErrConflict: %v", unmerged)
	}
	if !strings.Contains(unmerged.Error(), "note.md") {
		t.Errorf("unmergedError does not name the unmerged path: %v", unmerged)
	}
	if !strings.Contains(unmerged.Error(), "git-ai-sync resolve") {
		t.Errorf("unmergedError does not point at resolve: %v", unmerged)
	}
	if strings.Contains(unmerged.Error(), "stash drop") {
		t.Errorf("unmergedError mentions stash drop without an autostash: %v", unmerged)
	}
	if result.Committed {
		t.Errorf("Committed = true, want false: %+v", result)
	}
	if after := runGit(t, f.clone, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s; a commit was created", before, after)
	}
}

// TestGlobalCommitFailureStopsAtAttemptBudget stages more units than the budget
// can attempt and refuses all of them. An unbudgeted bisection would cost 3n-2
// subprocesses, so only a set this large reaches the budget-exhaustion branch.
func TestGlobalCommitFailureStopsAtAttemptBudget(t *testing.T) {
	f := newFixture(t)
	counter := filepath.Join(t.TempDir(), "hook-count")
	hooksDir := filepath.Join(runGit(t, f.clone, "rev-parse", "--absolute-git-dir"), "hooks")
	script := "#!/bin/sh\necho x >> '" + counter + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}
	const unitCount = 200
	for i := 0; i < unitCount; i++ {
		writeFile(t, f.clone, "unit-"+strconv.Itoa(i)+".txt", "x\n")
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
	staged := runGit(t, f.clone, "diff", "--cached", "--name-only")
	if got := len(strings.Split(strings.TrimSpace(staged), "\n")); got != unitCount {
		t.Errorf("staged units = %d, want %d", got, unitCount)
	}
	data, readErr := os.ReadFile(counter)
	if readErr != nil {
		t.Fatalf("read hook counter: %v", readErr)
	}
	if got := strings.Count(string(data), "\n"); got > maxCommitAttempts+2 {
		t.Errorf("hook ran %d times, want at most %d (budget plus the full commit)", got, maxCommitAttempts+2)
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
