package cli

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/lock"
)

type resolveFixture struct {
	root   string
	remote string
	clone  string
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	root := t.TempDir()
	f := resolveFixture{
		root:   root,
		remote: filepath.Join(root, "remote.git"),
		clone:  filepath.Join(root, "clone"),
	}
	resolveGit(t, root, "init", "--bare", f.remote)
	resolveGit(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/master")
	resolveConfigure(t, f.remote)
	resolveGit(t, root, "clone", f.remote, f.clone)
	resolveConfigure(t, f.clone)
	resolveWriteFile(t, f.clone, "note.txt", "base\n")
	resolveWriteFile(t, f.clone, ".gitignore", "/.git-ai-sync.lock\n")
	resolveGit(t, f.clone, "add", ".")
	resolveGit(t, f.clone, "commit", "-m", "init")
	resolveGit(t, f.clone, "push", "-u", "origin", "master")
	t.Setenv("GIT_EDITOR", "true")
	return f
}

// createConflict makes the clone and the remote diverge from the same base by
// planting from-ours in the clone and from-theirs on the remote, then runs the
// pull args and requires it to stop in a conflict state.
func (f resolveFixture) createConflict(t *testing.T, pullArgs ...string) {
	t.Helper()
	// The second clone starts at the shared base commit, so its commit is not a
	// descendant of the clone's local commit.
	other := filepath.Join(f.root, "other")
	resolveGit(t, f.root, "clone", f.remote, other)
	resolveConfigure(t, other)

	resolveWriteFile(t, f.clone, "note.txt", "from-ours\n")
	resolveGit(t, f.clone, "add", "note.txt")
	resolveGit(t, f.clone, "commit", "-m", "ours")

	resolveWriteFile(t, other, "note.txt", "from-theirs\n")
	resolveGit(t, other, "add", "note.txt")
	resolveGit(t, other, "commit", "-m", "theirs")
	resolveGit(t, other, "push", "origin", "master")

	args := append([]string{"pull"}, pullArgs...)
	if _, err := resolveGitErr(f.clone, args...); err == nil {
		t.Fatalf("git %v succeeded, want a conflict", args)
	}
	if !git.IsInMerge(f.clone) && !git.IsInRebase(f.clone) {
		t.Fatal("pull did not leave the repository in a merge or rebase conflict state")
	}
}

func resolveConfigure(t *testing.T, dir string) {
	t.Helper()
	resolveGit(t, dir, "config", "user.email", "test@example.com")
	resolveGit(t, dir, "config", "user.name", "Test User")
	resolveGit(t, dir, "config", "commit.gpgsign", "false")
	// Pin core.hooksPath to this repository's own hooks directory so a global
	// core.hooksPath on the developer's machine cannot redirect git away from
	// the hooks these tests write.
	hooksPath := filepath.Join(resolveGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	resolveGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
}

func resolveGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := resolveGitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// resolveGitErr returns the combined output and the error instead of failing,
// for commands that are expected to fail.
func resolveGitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func resolveWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func resolveReadFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

type claudeEnvelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
}

// writeClaudeStub writes an executable `claude` script into a fresh temp dir and
// prepends that dir to PATH. The script drains stdin, then prints the response
// file named by CLAUDE_STUB_RESPONSE.
func writeClaudeStub(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\ncat \"$CLAUDE_STUB_RESPONSE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeClaudeResponse serialises a successful envelope and points the stub at it.
func writeClaudeResponse(t *testing.T, result string) {
	t.Helper()
	data, err := json.Marshal(claudeEnvelope{Type: "result", IsError: false, Subtype: "success", Result: result})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	path := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write response: %v", err)
	}
	t.Setenv("CLAUDE_STUB_RESPONSE", path)
}

// runResolve builds the command tree, captures stderr, and runs resolve.
func runResolve(t *testing.T, repo string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := newRootCommand(context.Background())
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"resolve", repo})
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

func assertBothSidesNoMarkers(t *testing.T, dir, name string) {
	t.Helper()
	got := resolveReadFile(t, dir, name)
	if n := strings.Count(got, "from-ours"); n != 1 {
		t.Errorf("%s has %d occurrences of from-ours, want 1: %q", name, n, got)
	}
	if n := strings.Count(got, "from-theirs"); n != 1 {
		t.Errorf("%s has %d occurrences of from-theirs, want 1: %q", name, n, got)
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Errorf("%s still has conflict markers: %q", name, got)
	}
}

func assertTracked(t *testing.T, dir, name string) {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "--error-unmatch", name)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s is not tracked: %v\n%s", name, err, out)
	}
}

func TestResolveMergeConflictPushes(t *testing.T) {
	f := newResolveFixture(t)
	f.createConflict(t, "--no-rebase")
	writeClaudeResponse(t, "from-ours\nfrom-theirs\n")
	writeClaudeStub(t)

	output, err := runResolve(t, f.clone)
	if err != nil {
		t.Fatalf("resolve: %v\n%s", err, output)
	}
	assertBothSidesNoMarkers(t, f.clone, "note.txt")
	if !strings.Contains(output, "note.txt") {
		t.Errorf("output does not name note.txt: %q", output)
	}
	assertTracked(t, f.clone, "note.txt")
	if got, want := resolveGit(t, f.clone, "rev-parse", "HEAD"), resolveGit(t, f.remote, "rev-parse", "master"); got != want {
		t.Errorf("clone HEAD = %s, remote master = %s", got, want)
	}
	remoteNote := resolveGit(t, f.remote, "show", "master:note.txt")
	if !strings.Contains(remoteNote, "from-ours") || !strings.Contains(remoteNote, "from-theirs") {
		t.Errorf("remote note.txt does not carry both sides: %q", remoteNote)
	}
}

func TestResolveRebaseConflictPushes(t *testing.T) {
	f := newResolveFixture(t)
	f.createConflict(t, "--rebase")
	writeClaudeResponse(t, "from-ours\nfrom-theirs\n")
	writeClaudeStub(t)

	output, err := runResolve(t, f.clone)
	if err != nil {
		t.Fatalf("resolve: %v\n%s", err, output)
	}
	assertBothSidesNoMarkers(t, f.clone, "note.txt")
	assertTracked(t, f.clone, "note.txt")
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(f.clone, ".git", dir)); !os.IsNotExist(statErr) {
			t.Errorf(".git/%s still exists after resolve (err=%v)", dir, statErr)
		}
	}
	if got, want := resolveGit(t, f.clone, "rev-parse", "HEAD"), resolveGit(t, f.remote, "rev-parse", "master"); got != want {
		t.Errorf("clone HEAD = %s, remote master = %s", got, want)
	}
}

func TestResolveRefusesCleanRepo(t *testing.T) {
	f := newResolveFixture(t)
	before := resolveReadFile(t, f.clone, "note.txt")

	output, err := runResolve(t, f.clone)
	if err == nil {
		t.Fatalf("resolve succeeded on a clean repository\n%s", output)
	}
	if got := resolveReadFile(t, f.clone, "note.txt"); got != before {
		t.Errorf("note.txt changed to %q, want %q", got, before)
	}
}

// TestResolveCompletesFullyStagedMerge proves the command is idempotent: a
// repository whose conflicts are resolved and staged but whose merge was never
// concluded is completed on the next run, without invoking Claude.
func TestResolveCompletesFullyStagedMerge(t *testing.T) {
	f := newResolveFixture(t)
	f.createConflict(t, "--no-rebase")
	resolveWriteFile(t, f.clone, "note.txt", "from-ours\nfrom-theirs\n")
	resolveGit(t, f.clone, "add", "note.txt")
	if files, err := git.ConflictedFiles(context.Background(), f.clone); err != nil {
		t.Fatalf("list conflicted files: %v", err)
	} else if len(files) != 0 {
		t.Fatalf("conflicted files = %v, want none after staging", files)
	}

	output, err := runResolve(t, f.clone)
	if err != nil {
		t.Fatalf("resolve: %v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(f.clone, ".git", "MERGE_HEAD")); !os.IsNotExist(statErr) {
		t.Errorf(".git/MERGE_HEAD still exists after resolve (err=%v)", statErr)
	}
	if got := resolveGit(t, f.clone, "status", "--porcelain"); got != "" {
		t.Errorf("worktree not clean: %q", got)
	}
	if got, want := resolveGit(t, f.clone, "rev-parse", "HEAD"), resolveGit(t, f.remote, "rev-parse", "master"); got != want {
		t.Errorf("clone HEAD = %s, remote master = %s", got, want)
	}
}

func TestResolveRefusesWhenLocked(t *testing.T) {
	f := newResolveFixture(t)
	holder, err := lock.Acquire(context.Background(), f.clone)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer func() { _ = holder.Release(context.Background()) }()

	output, err := runResolve(t, f.clone)
	if err == nil {
		t.Fatalf("resolve succeeded while the lock was held\n%s", output)
	}
	if !stderrors.Is(err, lock.ErrLocked) {
		t.Errorf("error does not wrap ErrLocked: %v", err)
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("error = %q, want it to name the lock", err)
	}
}

func TestResolvePushRejectedKeepsResolution(t *testing.T) {
	f := newResolveFixture(t)
	f.createConflict(t, "--no-rebase")
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
	before := resolveGit(t, f.remote, "rev-parse", "master")
	writeClaudeResponse(t, "from-ours\nfrom-theirs\n")
	writeClaudeStub(t)

	output, err := runResolve(t, f.clone)
	if err == nil {
		t.Fatalf("resolve succeeded, want a push rejection\n%s", output)
	}
	assertBothSidesNoMarkers(t, f.clone, "note.txt")
	if _, headErr := resolveGitErr(f.clone, "rev-parse", "--verify", "HEAD^{commit}"); headErr != nil {
		t.Errorf("local HEAD is not a commit after the continuation: %v", headErr)
	}
	if after := resolveGit(t, f.remote, "rev-parse", "master"); after != before {
		t.Errorf("remote master moved from %s to %s", before, after)
	}
}
