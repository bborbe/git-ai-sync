package git

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	gitCmd(t, root, "init", "--bare", f.remote)
	gitCmd(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/master")
	gitCmd(t, root, "clone", f.remote, f.clone)
	configure(t, f.clone)
	writeFile(t, f.clone, "note.md", "hello\n")
	writeFile(t, f.clone, ".gitignore", "/.git-ai-sync.lock\n")
	gitCmd(t, f.clone, "add", ".")
	gitCmd(t, f.clone, "commit", "-m", "init")
	gitCmd(t, f.clone, "push", "-u", "origin", "master")
	return f
}

func (f fixture) addRemoteCommit(t *testing.T, name, content string) string {
	t.Helper()
	other := filepath.Join(f.root, "other-"+strings.ReplaceAll(name, "/", "-"))
	gitCmd(t, f.root, "clone", f.remote, other)
	configure(t, other)
	writeFile(t, other, name, content)
	gitCmd(t, other, "add", ".")
	gitCmd(t, other, "commit", "-m", "remote: "+name)
	gitCmd(t, other, "push", "origin", "master")
	return gitCmd(t, f.remote, "rev-parse", "master")
}

func configure(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test User")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
}

func gitCmd(t *testing.T, dir string, args ...string) string {
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

func TestFindRepo(t *testing.T) {
	f := newFixture(t)
	sub := filepath.Join(f.clone, "nested", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindRepo(context.Background(), sub)
	if err != nil {
		t.Fatalf("FindRepo: %v", err)
	}
	if got != f.clone {
		t.Errorf("FindRepo = %q, want %q", got, f.clone)
	}

	if _, err := FindRepo(context.Background(), t.TempDir()); err == nil {
		t.Error("FindRepo on a non-repository succeeded, want error")
	}
}

func TestHasChangesAndCommit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	dirty, err := HasChanges(ctx, f.clone)
	if err != nil {
		t.Fatalf("HasChanges: %v", err)
	}
	if dirty {
		t.Error("fresh clone reports changes")
	}

	writeFile(t, f.clone, "note.md", "hello\nedited\n")
	dirty, err = HasChanges(ctx, f.clone)
	if err != nil {
		t.Fatalf("HasChanges: %v", err)
	}
	if !dirty {
		t.Error("edited clone reports no changes")
	}

	if err := StageAll(ctx, f.clone); err != nil {
		t.Fatalf("StageAll: %v", err)
	}
	if err := Commit(ctx, f.clone, "auto: test message"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := gitCmd(t, f.clone, "log", "-1", "--format=%s"); got != "auto: test message" {
		t.Errorf("commit subject = %q, want auto: test message", got)
	}

	dirty, err = HasChanges(ctx, f.clone)
	if err != nil {
		t.Fatalf("HasChanges: %v", err)
	}
	if dirty {
		t.Error("committed clone still reports changes")
	}
}

func TestPullBringsRemoteCommit(t *testing.T) {
	for _, strategy := range []Strategy{StrategyMerge, StrategyRebase} {
		t.Run(string(strategy), func(t *testing.T) {
			f := newFixture(t)
			sha := f.addRemoteCommit(t, "remote.txt", "remote\n")

			if err := Pull(context.Background(), f.clone, strategy); err != nil {
				t.Fatalf("Pull: %v", err)
			}
			if !isAncestor(t, f.clone, sha, "HEAD") {
				t.Errorf("remote commit %s is not an ancestor of HEAD", sha)
			}
		})
	}
}

func TestPullConflictWrapsErrConflict(t *testing.T) {
	for _, strategy := range []Strategy{StrategyMerge, StrategyRebase} {
		t.Run(string(strategy), func(t *testing.T) {
			f := newFixture(t)
			writeFile(t, f.clone, "note.md", "local\n")
			gitCmd(t, f.clone, "add", "note.md")
			gitCmd(t, f.clone, "commit", "-m", "local edit")

			other := filepath.Join(f.root, "other-"+string(strategy))
			gitCmd(t, f.root, "clone", f.remote, other)
			configure(t, other)
			writeFile(t, other, "note.md", "remote\n")
			gitCmd(t, other, "add", "note.md")
			gitCmd(t, other, "commit", "-m", "remote edit")
			gitCmd(t, other, "push", "origin", "master")

			err := Pull(context.Background(), f.clone, strategy)
			if err == nil {
				t.Fatal("Pull succeeded, want conflict")
			}
			if !stderrors.Is(err, ErrConflict) {
				t.Errorf("error does not wrap ErrConflict: %v", err)
			}
		})
	}
}

func TestPushLandsCommit(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.clone, "note.md", "hello\npushed\n")
	gitCmd(t, f.clone, "add", ".")
	gitCmd(t, f.clone, "commit", "-m", "auto: push me")

	if err := Push(context.Background(), f.clone); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got, want := gitCmd(t, f.remote, "rev-parse", "master"), gitCmd(t, f.clone, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote HEAD = %s, want %s", got, want)
	}
}

func TestIsAheadOfRemote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	ahead, err := IsAheadOfRemote(ctx, f.clone)
	if err != nil {
		t.Fatalf("IsAheadOfRemote: %v", err)
	}
	if ahead {
		t.Error("fresh clone is ahead of remote")
	}

	writeFile(t, f.clone, "note.md", "hello\nahead\n")
	gitCmd(t, f.clone, "add", ".")
	gitCmd(t, f.clone, "commit", "-m", "auto: ahead")

	ahead, err = IsAheadOfRemote(ctx, f.clone)
	if err != nil {
		t.Fatalf("IsAheadOfRemote: %v", err)
	}
	if !ahead {
		t.Error("local commit is not reported ahead")
	}

	if err := Push(ctx, f.clone); err != nil {
		t.Fatalf("Push: %v", err)
	}
	ahead, err = IsAheadOfRemote(ctx, f.clone)
	if err != nil {
		t.Fatalf("IsAheadOfRemote: %v", err)
	}
	if ahead {
		t.Error("pushed clone still reports ahead")
	}

	plain := t.TempDir()
	gitCmd(t, plain, "init")
	configure(t, plain)
	writeFile(t, plain, "file.txt", "x")
	gitCmd(t, plain, "add", ".")
	gitCmd(t, plain, "commit", "-m", "init")
	ahead, err = IsAheadOfRemote(ctx, plain)
	if err != nil {
		t.Fatalf("IsAheadOfRemote without upstream: %v", err)
	}
	if ahead {
		t.Error("repository without upstream reports ahead")
	}
}

func TestGenerateCommitMessage(t *testing.T) {
	fixed := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if got, want := GenerateCommitMessage("auto", fixed), "auto: 2026-02-03T04:05:06Z"; got != want {
		t.Errorf("GenerateCommitMessage = %q, want %q", got, want)
	}
}
