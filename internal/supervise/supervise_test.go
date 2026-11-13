package supervise

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/config"
	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/logging"
	"github.com/bborbe/git-ai-sync/internal/watch"
)

const supervisePushTimeout = 20 * time.Second

type superviseFixture struct {
	root   string
	remote string
	clone  string
}

func newSuperviseFixture(t *testing.T) superviseFixture {
	t.Helper()
	root := t.TempDir()
	f := superviseFixture{
		root:   root,
		remote: filepath.Join(root, "remote.git"),
		clone:  filepath.Join(root, "clone"),
	}
	superviseRunGit(t, root, "init", "--bare", f.remote)
	superviseRunGit(t, f.remote, "symbolic-ref", "HEAD", "refs/heads/master")
	superviseConfigure(t, f.remote)
	superviseRunGit(t, root, "clone", f.remote, f.clone)
	superviseConfigure(t, f.clone)
	superviseWriteFile(t, f.clone, "note.md", "hello\n")
	superviseWriteFile(t, f.clone, ".gitignore", "/.git-ai-sync.lock\n")
	superviseRunGit(t, f.clone, "add", ".")
	superviseRunGit(t, f.clone, "commit", "-m", "init")
	superviseRunGit(t, f.clone, "push", "-u", "origin", "master")
	return f
}

func superviseConfigure(t *testing.T, dir string) {
	t.Helper()
	superviseRunGit(t, dir, "config", "user.email", "test@example.com")
	superviseRunGit(t, dir, "config", "user.name", "Test User")
	superviseRunGit(t, dir, "config", "commit.gpgsign", "false")
	// Pin core.hooksPath to this repository's own hooks directory so a global
	// core.hooksPath on the developer's machine cannot redirect git away from
	// the hooks these tests write.
	hooksPath := filepath.Join(superviseRunGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	superviseRunGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
}

func superviseRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func superviseWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// superviseLogBuffer is a bytes.Buffer safe for concurrent writes from the vault
// goroutines and reads from the test goroutine.
type superviseLogBuffer struct {
	mutex  stdsync.Mutex
	buffer bytes.Buffer
}

func (b *superviseLogBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(data)
}

func (b *superviseLogBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// waitForLine polls until a log line contains substr and returns that line.
func waitForLine(t *testing.T, buffer *superviseLogBuffer, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buffer.String(), "\n") {
			if strings.Contains(line, substr) {
				return line
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in the log:\n%s", substr, buffer.String())
	return ""
}

func superviseConfig(vaults []string) config.FileConfig {
	return config.FileConfig{
		Vaults:       vaults,
		Interval:     time.Second,
		MaxWait:      time.Minute,
		CommitPrefix: "auto",
		Strategy:     "merge",
	}
}

func startSupervise(t *testing.T, cfg config.FileConfig) (context.CancelFunc, <-chan error, *superviseLogBuffer) {
	t.Helper()
	buffer := &superviseLogBuffer{}
	logger := logging.New(buffer, slog.LevelDebug)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Config: cfg, Logger: logger, Clock: clock.New()})
	}()
	return cancel, done, buffer
}

func requireRunReturns(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancellation")
	}
}

// TestSuperviseRunsVaultsConcurrently covers Acceptance Criterion 3: both vaults
// run, both push, and cancellation returns within 5s.
func TestSuperviseRunsVaultsConcurrently(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	first := newSuperviseFixture(t)
	second := newSuperviseFixture(t)
	superviseWriteFile(t, first.clone, "note.md", "hello\nfirst edit\n")
	superviseWriteFile(t, second.clone, "note.md", "hello\nsecond edit\n")
	firstBefore := superviseRunGit(t, first.remote, "rev-parse", "master")
	secondBefore := superviseRunGit(t, second.remote, "rev-parse", "master")

	cancel, done, buffer := startSupervise(t, superviseConfig([]string{first.clone, second.clone}))
	waitForLine(t, buffer, "push path="+first.clone, supervisePushTimeout)
	waitForLine(t, buffer, "push path="+second.clone, supervisePushTimeout)
	cancel()
	requireRunReturns(t, done)

	if got := superviseRunGit(t, first.remote, "rev-parse", "master"); got == firstBefore {
		t.Errorf("first remote master did not move from %s", firstBefore)
	}
	if got := superviseRunGit(t, second.remote, "rev-parse", "master"); got == secondBefore {
		t.Errorf("second remote master did not move from %s", secondBefore)
	}
	for _, f := range []superviseFixture{first, second} {
		if got, want := superviseRunGit(t, f.clone, "rev-parse", "HEAD"), superviseRunGit(t, f.remote, "rev-parse", "master"); got != want {
			t.Errorf("clone HEAD %s != remote master %s", got, want)
		}
	}
}

// TestSuperviseSlowVaultDoesNotStallOthers covers Acceptance Criterion 3: a
// blocked push in one vault does not delay another vault's push.
func TestSuperviseSlowVaultDoesNotStallOthers(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	slow := newSuperviseFixture(t)
	fast := newSuperviseFixture(t)
	hook := filepath.Join(slow.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 3\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
	superviseWriteFile(t, slow.clone, "note.md", "hello\nslow edit\n")
	superviseWriteFile(t, fast.clone, "note.md", "hello\nfast edit\n")

	cancel, done, buffer := startSupervise(t, superviseConfig([]string{slow.clone, fast.clone}))
	waitForLine(t, buffer, "push path="+fast.clone, 2500*time.Millisecond)
	cancel()
	requireRunReturns(t, done)
}

var superviseSHAPattern = regexp.MustCompile(`\bsha=([0-9a-f]+)`)

// TestPushLineNamesThePushedCommit covers Acceptance Criterion 4: the logged sha
// is the commit that reached the remote, not HEAD at log time.
func TestPushLineNamesThePushedCommit(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	f := newSuperviseFixture(t)
	superviseWriteFile(t, f.clone, "note.md", "hello\nlocal edit\n")

	cancel, done, buffer := startSupervise(t, superviseConfig([]string{f.clone}))
	line := waitForLine(t, buffer, "push path="+f.clone, supervisePushTimeout)
	match := superviseSHAPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("push line has no sha: %q", line)
	}
	sha := match[1]
	cancel()
	requireRunReturns(t, done)

	if want := superviseRunGit(t, f.remote, "rev-parse", "refs/heads/master"); sha != want {
		t.Errorf("logged sha = %s, want the remote ref %s", sha, want)
	}
	if subject := superviseRunGit(t, f.remote, "log", "-1", "--format=%s", sha); !strings.HasPrefix(subject, "auto") {
		t.Errorf("pushed commit subject = %q, want the configured prefix", subject)
	}

	// A later worktree commit must not change what the already-emitted line says.
	superviseWriteFile(t, f.clone, "later.md", "later\n")
	superviseRunGit(t, f.clone, "add", ".")
	superviseRunGit(t, f.clone, "commit", "-m", "later")
	if head := superviseRunGit(t, f.clone, "rev-parse", "HEAD"); head == sha {
		t.Fatalf("the later commit did not move HEAD off the pushed sha %s", sha)
	}
	if !strings.Contains(buffer.String(), line) {
		t.Errorf("the emitted push line %q is no longer in the log:\n%s", line, buffer.String())
	}
}

// TestFailingVaultDoesNotStopOthers covers Acceptance Criterion 9: a vault whose
// path is not a repository fails every interval while the others keep pushing.
func TestFailingVaultDoesNotStopOthers(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	first := newSuperviseFixture(t)
	second := newSuperviseFixture(t)
	badPath := filepath.Join(t.TempDir(), "not-a-repository")
	if err := os.WriteFile(badPath, []byte("not a repository\n"), 0o644); err != nil {
		t.Fatalf("write non-repository file: %v", err)
	}

	cfg := superviseConfig([]string{first.clone, second.clone, badPath})
	cfg.MaxWait = time.Millisecond
	cancel, done, buffer := startSupervise(t, cfg)

	writerCtx, stopWriter := context.WithCancel(context.Background())
	defer stopWriter()
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for index := 0; ; index++ {
			select {
			case <-writerCtx.Done():
				return
			case <-ticker.C:
				name := fmt.Sprintf("change-%d.md", index)
				_ = os.WriteFile(filepath.Join(first.clone, name), []byte(name), 0o644)
				_ = os.WriteFile(filepath.Join(second.clone, name), []byte(name), 0o644)
			}
		}
	}()

	for _, f := range []superviseFixture{first, second} {
		waitForOccurrences(t, buffer, "push path="+f.clone, 2, supervisePushTimeout)
	}
	waitForOccurrences(t, buffer, "path="+badPath, 3, supervisePushTimeout)
	stopWriter()

	if strings.Contains(buffer.String(), "vault panicked") {
		t.Errorf("a vault goroutine panicked:\n%s", buffer.String())
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned before cancellation: %v", err)
	default:
	}

	cancel()
	requireRunReturns(t, done)
}

// TestRunWiresConflictResolver proves the production supervisor path cannot
// silently lose the conflict resolver or its alert.
func TestRunWiresConflictResolver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	captured := make(chan watch.Options, 1)
	err := Run(ctx, Options{
		Config: config.FileConfig{
			Vaults:       []string{"/tmp/vault"},
			Interval:     time.Second,
			MaxWait:      time.Minute,
			CommitPrefix: "auto",
			Strategy:     "merge",
		},
		WatchRun: func(_ context.Context, opts watch.Options) error {
			captured <- opts
			cancel()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case opts := <-captured:
		if opts.ConflictResolver == nil {
			t.Error("ConflictResolver is nil")
		}
		if opts.ConflictAlert == nil {
			t.Error("ConflictAlert is nil")
		}
	default:
		t.Fatal("WatchRun was never called")
	}
}

// TestConflictAlertKeysOnRepo proves the ConflictAlert closure derives the card
// key from the repository it receives, matching runnerFor's stall tracker, not
// from the configured vault path.
func TestConflictAlertKeysOnRepo(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	fixture := newSuperviseFixture(t)
	subdir := filepath.Join(fixture.clone, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	stubDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "attention.log")
	script := filepath.Join(stubDir, "attention-ask.py")
	const stub = `import json, os, sys
with open(os.environ["ATTENTION_STUB_LOG"], "a") as f:
    f.write(json.dumps({"argv": sys.argv[1:], "store": os.environ.get("ATTENTION_STORE_URL")}) + "\n")
print("ITEM_ID: " + "a" * 32)
`
	if err := os.WriteFile(script, []byte(stub), 0o644); err != nil {
		t.Fatalf("write attention stub: %v", err)
	}
	t.Setenv("ATTENTION_STUB_LOG", stubLog)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store := &storeRecorder{}
	storeServer := httptest.NewServer(store)
	defer storeServer.Close()

	cfg := superviseConfig([]string{subdir})
	cfg.StateFile = filepath.Join(t.TempDir(), "status.json")
	cfg.AttentionScript = script
	cfg.AttentionStoreURL = storeServer.URL

	repo, err := git.FindRepo(context.Background(), subdir)
	if err != nil {
		t.Fatalf("find repo for %s: %v", subdir, err)
	}
	if Label(repo) == Label(subdir) {
		t.Fatalf("fixture does not distinguish repo %q from vault %q", repo, subdir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	captured := make(chan watch.Options, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config: cfg,
			Logger: logging.New(&superviseLogBuffer{}, slog.LevelDebug),
			Clock:  clock.New(),
			WatchRun: func(_ context.Context, opts watch.Options) error {
				select {
				case captured <- opts:
				default:
				}
				return nil
			},
		})
	}()

	var opts watch.Options
	select {
	case opts = <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchRun was never called")
	}

	opts.ConflictAlert(ctx, repo, stderrors.New("boom"))

	invocations := waitForInvocations(t, stubLog, 1, 10*time.Second)
	dedupKey, ok := flagValue(invocations[0].Argv, "--dedup-key")
	want := "git-ai-sync-" + Label(repo) + "-stalled"
	if !ok || dedupKey != want {
		t.Errorf("--dedup-key = %q (present %t), want %q (repo label, not vault label %q)", dedupKey, ok, want, Label(subdir))
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancellation")
	}
}

func TestLabel(t *testing.T) {
	cases := []struct {
		vault string
		want  string
	}{
		{"Personal", "personal"},
		{"My Vault", "my-vault"},
		{"Work-Notes", "work-notes"},
		{"/a/b/Personal", "personal"},
		{"a__b", "a-b"},
	}
	for _, c := range cases {
		t.Run(c.vault, func(t *testing.T) {
			if got := Label(c.vault); got != c.want {
				t.Errorf("Label(%q) = %q, want %q", c.vault, got, c.want)
			}
		})
	}
}
