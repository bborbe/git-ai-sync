package sync

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bborbe/git-ai-sync/internal/git"
)

// claudeEnvelope is the JSON result envelope the Claude CLI emits.
type claudeEnvelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
}

// writeStub writes an executable `claude` script into a fresh temp dir,
// prepends that dir to PATH, and returns the dir. The script drains stdin first
// so the CLI never blocks on a closed pipe, then runs body.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n" + body
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// writeResponse serialises env to a temp file and points the stub at it.
func writeResponse(t *testing.T, env claudeEnvelope) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "response.json")
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write response: %v", err)
	}
	t.Setenv("CLAUDE_STUB_RESPONSE", path)
	return path
}

// stubResponse returns a stub body that prints the response file.
func stubResponse() string {
	return "cat \"$CLAUDE_STUB_RESPONSE\"\n"
}

func seedConflict(t *testing.T, f fixture, pullArgs ...string) {
	t.Helper()
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\n")
	runGit(t, f.clone, "add", ".")
	runGit(t, f.clone, "commit", "-m", "local")
	f.addRemoteCommit(t, "note.md", "hello\nremote edit\n")

	cmd := exec.Command("git", pullArgs...)
	cmd.Dir = f.clone
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("git %v succeeded, want a conflict:\n%s", pullArgs, out)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func assertCleanAndPushed(t *testing.T, f fixture) {
	t.Helper()
	if fileExists(filepath.Join(f.clone, ".git", "MERGE_HEAD")) {
		t.Error(".git/MERGE_HEAD still exists")
	}
	if git.IsInRebase(f.clone) {
		t.Error("repository is still in a rebase state")
	}
	if got := runGit(t, f.clone, "status", "--porcelain"); got != "" {
		t.Errorf("worktree not clean: %q", got)
	}
	if got, want := runGit(t, f.clone, "rev-parse", "HEAD"), runGit(t, f.remote, "rev-parse", "master"); got != want {
		t.Errorf("clone HEAD = %s, remote master = %s", got, want)
	}
}

func TestResolveConflictsResolvesMergeAndPushes(t *testing.T) {
	f := newFixture(t)
	seedConflict(t, f, "pull", "--no-rebase")
	if !git.IsInMerge(f.clone) {
		t.Fatal("fixture is not in a merge state")
	}
	writeResponse(t, claudeEnvelope{Type: "result", Subtype: "success", Result: "hello\nlocal edit\nremote edit\n"})
	writeStub(t, stubResponse())

	if err := ResolveConflicts(context.Background(), f.clone); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	assertCleanAndPushed(t, f)
	if got := runGit(t, f.remote, "show", "master:note.md"); !strings.Contains(got, "remote edit") {
		t.Errorf("remote note.md = %q, want the resolved content", got)
	}
}

func TestResolveConflictsResolvesRebaseAndPushes(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	f := newFixture(t)
	seedConflict(t, f, "pull", "--rebase")
	if !git.IsInRebase(f.clone) {
		t.Fatal("fixture is not in a rebase state")
	}
	writeResponse(t, claudeEnvelope{Type: "result", Subtype: "success", Result: "hello\nlocal edit\nremote edit\n"})
	writeStub(t, stubResponse())

	if err := ResolveConflicts(context.Background(), f.clone); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	assertCleanAndPushed(t, f)
}

// TestResolveConflictsCompletesFullyStagedMerge proves the resolver is
// idempotent: a repo whose conflicts are resolved and staged but whose merge was
// never concluded is completed on the next attempt, without calling Claude.
func TestResolveConflictsCompletesFullyStagedMerge(t *testing.T) {
	f := newFixture(t)
	seedConflict(t, f, "pull", "--no-rebase")
	if !git.IsInMerge(f.clone) {
		t.Fatal("fixture is not in a merge state")
	}
	writeFile(t, f.clone, "note.md", "hello\nlocal edit\nremote edit\n")
	runGit(t, f.clone, "add", "note.md")
	files, err := git.ConflictedFiles(context.Background(), f.clone)
	if err != nil {
		t.Fatalf("list conflicted files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("conflicted files = %v, want none after staging", files)
	}

	if err := ResolveConflicts(context.Background(), f.clone); err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	assertCleanAndPushed(t, f)
	if got := runGit(t, f.remote, "show", "master:note.md"); !strings.Contains(got, "remote edit") {
		t.Errorf("remote note.md = %q, want the staged content", got)
	}
}

func TestResolveConflictsNoConflictedFiles(t *testing.T) {
	f := newFixture(t)
	if err := ResolveConflicts(context.Background(), f.clone); err == nil {
		t.Fatal("ResolveConflicts succeeded on a clean fixture, want an error")
	}
}
