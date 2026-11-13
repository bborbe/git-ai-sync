package watch

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/clock/clocktest"
	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/lock"
	"github.com/bborbe/git-ai-sync/internal/logging"
	"github.com/bborbe/git-ai-sync/internal/sync"
)

type fakeTracker struct {
	values  []time.Duration
	index   int
	started bool
	stopped bool
}

func (f *fakeTracker) Start(context.Context) error {
	f.started = true
	return nil
}

func (f *fakeTracker) Stop() {
	f.stopped = true
}

func (f *fakeTracker) SecondsSinceLastChange(time.Time) time.Duration {
	if len(f.values) == 0 {
		return 0
	}
	value := f.values[f.index]
	if f.index < len(f.values)-1 {
		f.index++
	}
	return value
}

type fakeSleep struct {
	clk         *clocktest.Fake
	cancel      context.CancelFunc
	cancelAfter int
	calls       int
}

func (f *fakeSleep) sleep(_ context.Context, d time.Duration) error {
	f.calls++
	f.clk.Advance(d)
	if f.calls >= f.cancelAfter {
		f.cancel()
	}
	return nil
}

type recordingRunner struct {
	calls   int
	options []sync.Options
	handle  func(call int) (sync.Result, error)
}

func (r *recordingRunner) run(_ context.Context, _ string, opts sync.Options) (sync.Result, error) {
	r.calls++
	r.options = append(r.options, opts)
	if r.handle != nil {
		return r.handle(r.calls)
	}
	return sync.Result{}, nil
}

type harness struct {
	ctx     context.Context
	cancel  context.CancelFunc
	fake    *clocktest.Fake
	tracker *fakeTracker
	runner  *recordingRunner
	sleep   SleepFunc
	buf     *bytes.Buffer
}

func newHarness(values []time.Duration, cancelAfter int) *harness {
	base := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	fake := clocktest.NewFake(base)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &recordingRunner{}
	sleeper := &fakeSleep{clk: fake, cancel: cancel, cancelAfter: cancelAfter}
	return &harness{
		ctx:     ctx,
		cancel:  cancel,
		fake:    fake,
		tracker: &fakeTracker{values: values},
		runner:  runner,
		sleep:   sleeper.sleep,
		buf:     &bytes.Buffer{},
	}
}

func (h *harness) runLoop(t *testing.T, interval, maxWait time.Duration) {
	t.Helper()
	h.runLoopWith(t, interval, maxWait, nil, nil, defaultConflictAttemptTimeout)
}

func (h *harness) runLoopWith(
	t *testing.T,
	interval, maxWait time.Duration,
	resolve func(ctx context.Context, repo string) error,
	alert func(ctx context.Context, repo string, cause error),
	attemptTimeout time.Duration,
) {
	t.Helper()
	logger := logging.New(h.buf, slog.LevelInfo)
	if err := loop(
		h.ctx, "/repo", interval, maxWait, "auto", git.StrategyMerge,
		h.fake, h.tracker, h.runner.run, h.sleep, resolve, alert, attemptTimeout, logger,
	); err != nil {
		t.Fatalf("loop returned an error: %v", err)
	}
}

func assertOrder(t *testing.T, out string, substrings []string) {
	t.Helper()
	position := 0
	for _, substring := range substrings {
		index := strings.Index(out[position:], substring)
		if index < 0 {
			t.Fatalf("substring %q not found in order in:\n%s", substring, out)
		}
		position += index + len(substring)
	}
}

func TestLoopLogsDecayingWindow(t *testing.T) {
	h := newHarness([]time.Duration{500 * time.Millisecond}, 3)
	h.runLoop(t, 4*time.Second, 300*time.Second)

	assertOrder(t, h.buf.String(), []string{
		"[1] Skipping - files changed 0.5s ago (still editing; quiet window 4.0s)",
		"[2] Skipping - files changed 0.5s ago (still editing; quiet window 2.0s)",
		"[3] Skipping - files changed 0.5s ago (still editing; quiet window 1.0s)",
	})
	if h.runner.calls != 0 {
		t.Errorf("runner called %d times, want 0", h.runner.calls)
	}
}

func TestLoopCapForcesSync(t *testing.T) {
	h := newHarness([]time.Duration{100 * time.Millisecond}, 1000)
	h.runner.handle = func(int) (sync.Result, error) {
		h.cancel()
		return sync.Result{}, nil
	}
	h.runLoop(t, time.Second, 3*time.Second)

	if !strings.Contains(h.buf.String(), "[3] Maximum wait 3s reached — syncing regardless of quiet") {
		t.Fatalf("cap line missing:\n%s", h.buf.String())
	}
	if h.runner.calls != 1 {
		t.Errorf("runner called %d times, want 1", h.runner.calls)
	}
}

func TestLoopSuppressesCapWhenWindowAccepts(t *testing.T) {
	h := newHarness([]time.Duration{2 * time.Second}, 1000)
	h.runner.handle = func(int) (sync.Result, error) {
		h.cancel()
		return sync.Result{}, nil
	}
	h.runLoop(t, time.Second, time.Second)

	out := h.buf.String()
	if !strings.Contains(out, "[1] Checking...") {
		t.Fatalf("checking line missing:\n%s", out)
	}
	if strings.Contains(out, "Maximum wait") {
		t.Fatalf("cap line should be suppressed:\n%s", out)
	}
}

func TestSuccessResetsDecay(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second, 500 * time.Millisecond}, 2)
	h.runLoop(t, time.Second, 300*time.Second)

	if !strings.Contains(h.buf.String(), "[2] Skipping - files changed 0.5s ago (still editing; quiet window 1.0s)") {
		t.Fatalf("second cycle did not use the reset window:\n%s", h.buf.String())
	}
	if h.runner.calls != 1 {
		t.Errorf("runner called %d times, want 1", h.runner.calls)
	}
}

func TestFailedCycleKeepsCapClock(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second, 500 * time.Millisecond}, 1000)
	h.runner.handle = func(call int) (sync.Result, error) {
		if call == 1 {
			return sync.Result{}, stderrors.New("boom")
		}
		h.cancel()
		return sync.Result{}, nil
	}
	h.runLoop(t, time.Second, 2*time.Second)

	if !strings.Contains(h.buf.String(), "[2] Maximum wait 2s reached — syncing regardless of quiet") {
		t.Fatalf("cap did not fire at cycle 2:\n%s", h.buf.String())
	}
	if h.runner.calls != 2 {
		t.Errorf("runner called %d times, want 2", h.runner.calls)
	}
}

func TestLoopPassesPullWhenClean(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second}, 1000)
	h.runner.handle = func(int) (sync.Result, error) {
		h.cancel()
		return sync.Result{}, nil
	}
	h.runLoop(t, time.Second, 300*time.Second)

	if len(h.runner.options) != 1 {
		t.Fatalf("runner received %d option sets, want 1", len(h.runner.options))
	}
	if !h.runner.options[0].PullWhenClean {
		t.Error("watch loop did not pass PullWhenClean: true")
	}
}

func TestSecondWatchRefuses(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	ctx := context.Background()

	holder, err := lock.Acquire(ctx, dir)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer func() { _ = holder.Release(ctx) }()

	var buf bytes.Buffer
	err = Run(ctx, Options{
		Path:         dir,
		Interval:     time.Second,
		MaxWait:      time.Minute,
		CommitPrefix: "auto",
		Strategy:     git.StrategyMerge,
		Clock:        clocktest.NewFake(time.Now()),
		Logger:       logging.New(&buf, slog.LevelInfo),
		Tracker:      &fakeTracker{},
		Sleep:        (&fakeSleep{clk: clocktest.NewFake(time.Now()), cancel: func() {}, cancelAfter: 1}).sleep,
		Runner:       (&recordingRunner{}).run,
	})
	if err == nil {
		t.Fatal("second watch succeeded, want refusal")
	}
	if !stderrors.Is(err, lock.ErrLocked) {
		t.Errorf("error does not wrap ErrLocked: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the holder PID %s", err, want)
	}
}

func TestLockReleasedOnExit(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	tracker := &fakeTracker{}
	err := Run(ctx, Options{
		Path:         dir,
		Interval:     time.Second,
		MaxWait:      time.Minute,
		CommitPrefix: "auto",
		Strategy:     git.StrategyMerge,
		Clock:        clocktest.NewFake(time.Now()),
		Logger:       logging.New(&buf, slog.LevelInfo),
		Tracker:      tracker,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tracker.started || !tracker.stopped {
		t.Errorf("tracker lifecycle: started=%v stopped=%v", tracker.started, tracker.stopped)
	}

	l, err := lock.Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	_ = l.Release(context.Background())
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

type recordingSleep struct {
	inner     SleepFunc
	durations []time.Duration
}

func (r *recordingSleep) sleep(ctx context.Context, d time.Duration) error {
	r.durations = append(r.durations, d)
	return r.inner(ctx, d)
}

// claudeEnvelope is the JSON result envelope the Claude CLI emits.
type claudeEnvelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
}

// writeClaudeStub writes an executable `claude` script into a fresh temp dir and
// prepends that dir to PATH.
func writeClaudeStub(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n" + body
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeClaudeResponse(t *testing.T, env claudeEnvelope) {
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
}

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func configureWatch(t *testing.T, dir string) {
	t.Helper()
	runGitIn(t, dir, "config", "user.email", "test@example.com")
	runGitIn(t, dir, "config", "user.name", "Test User")
	runGitIn(t, dir, "config", "commit.gpgsign", "false")
	hooksPath := filepath.Join(runGitIn(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	runGitIn(t, dir, "config", "--local", "core.hooksPath", hooksPath)
	t.Setenv("GIT_EDITOR", "true")
}

func writeWatchFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestLoopResolvesConflictAndContinues is the watch-path regression: a real
// conflicted merge is resolved, concluded and pushed by the loop.
func TestLoopResolvesConflictAndContinues(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	clone := filepath.Join(root, "clone")
	runGitIn(t, root, "init", "--bare", remote)
	runGitIn(t, remote, "symbolic-ref", "HEAD", "refs/heads/master")
	configureWatch(t, remote)
	runGitIn(t, root, "clone", remote, clone)
	configureWatch(t, clone)
	writeWatchFile(t, clone, "note.md", "hello\n")
	writeWatchFile(t, clone, ".gitignore", "/.git-ai-sync.lock\n")
	runGitIn(t, clone, "add", ".")
	runGitIn(t, clone, "commit", "-m", "init")
	runGitIn(t, clone, "push", "-u", "origin", "master")

	writeWatchFile(t, clone, "note.md", "hello\nlocal edit\n")
	runGitIn(t, clone, "add", ".")
	runGitIn(t, clone, "commit", "-m", "local")

	other := filepath.Join(root, "other")
	runGitIn(t, root, "clone", remote, other)
	configureWatch(t, other)
	writeWatchFile(t, other, "note.md", "hello\nremote edit\n")
	runGitIn(t, other, "add", ".")
	runGitIn(t, other, "commit", "-m", "remote")
	runGitIn(t, other, "push", "origin", "master")

	pull := exec.Command("git", "pull", "--no-rebase")
	pull.Dir = clone
	if out, err := pull.CombinedOutput(); err == nil {
		t.Fatalf("git pull succeeded, want a conflict:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("fixture is not in a merge state: %v", err)
	}

	writeClaudeResponse(t, claudeEnvelope{Type: "result", Subtype: "success", Result: "hello\nlocal edit\nremote edit\n"})
	writeClaudeStub(t, "cat \"$CLAUDE_STUB_RESPONSE\"\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := clocktest.NewFake(time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC))
	sleeper := &fakeSleep{clk: fake, cancel: cancel, cancelAfter: 1000}
	runner := &recordingRunner{handle: func(int) (sync.Result, error) {
		return sync.Result{}, errors.Wrapf(ctx, git.ErrConflict, "seeded conflict")
	}}
	resolver := func(rctx context.Context, repo string) error {
		err := sync.ResolveConflicts(rctx, repo)
		if err == nil {
			cancel()
		}
		return err
	}
	logger := logging.New(&bytes.Buffer{}, slog.LevelInfo)
	if err := loop(
		ctx, clone, time.Second, time.Minute, "auto", git.StrategyMerge,
		fake, &fakeTracker{values: []time.Duration{10 * time.Second}}, runner.run,
		sleeper.sleep, resolver, nil, defaultConflictAttemptTimeout, logger,
	); err != nil {
		t.Fatalf("loop returned an error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(clone, ".git", "MERGE_HEAD")); err == nil {
		t.Error(".git/MERGE_HEAD still exists")
	}
	if got := runGitIn(t, clone, "status", "--porcelain"); got != "" {
		t.Errorf("worktree not clean: %q", got)
	}
	if got, want := runGitIn(t, clone, "rev-parse", "HEAD"), runGitIn(t, remote, "rev-parse", "master"); got != want {
		t.Errorf("clone HEAD = %s, remote master = %s", got, want)
	}
}

// TestLoopConflictAlertAfterMaxAttempts asserts the attempt bound, the retry
// delay, and a single alert.
func TestLoopConflictAlertAfterMaxAttempts(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second}, 10)
	recorder := &recordingSleep{inner: h.sleep}
	h.sleep = recorder.sleep
	h.runner.handle = func(int) (sync.Result, error) {
		return sync.Result{}, errors.Wrapf(h.ctx, git.ErrConflict, "boom")
	}

	resolverCalls := 0
	resolve := func(context.Context, string) error {
		resolverCalls++
		// The alert no longer cancels the loop: let the second cycle run with the
		// reduced cap, then stop after the fourth call.
		if resolverCalls == MaxConflictAttempts+1 {
			h.cancel()
		}
		return stderrors.New("resolve failed")
	}
	alerts := 0
	alert := func(context.Context, string, error) {
		alerts++
	}
	h.runLoopWith(t, time.Second, time.Minute, resolve, alert, defaultConflictAttemptTimeout)

	if resolverCalls != MaxConflictAttempts+1 {
		t.Errorf("resolver invoked %d times, want %d", resolverCalls, MaxConflictAttempts+1)
	}
	intervalCount, delayCount := 0, 0
	for _, d := range recorder.durations {
		switch d {
		case time.Second:
			intervalCount++
		case conflictRetryDelay:
			delayCount++
		}
	}
	if intervalCount != 2 || delayCount != 2 {
		t.Errorf("durations = %v, want 2 intervals and 2 conflictRetryDelay", recorder.durations)
	}
	if alerts != 1 {
		t.Errorf("ConflictAlert called %d times, want 1", alerts)
	}
}

// TestLoopSuccessResetsAlerted proves a successful resolution clears the dedup,
// so a later conflict posts a second card.
func TestLoopSuccessResetsAlerted(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second}, 1000)
	h.runner.handle = func(int) (sync.Result, error) {
		return sync.Result{}, errors.Wrapf(h.ctx, git.ErrConflict, "boom")
	}

	resolverCalls := 0
	resolve := func(context.Context, string) error {
		resolverCalls++
		if resolverCalls == 4 {
			return nil
		}
		return stderrors.New("resolve failed")
	}
	alerts := 0
	alert := func(context.Context, string, error) {
		alerts++
		if alerts == 2 {
			h.cancel()
		}
	}
	h.runLoopWith(t, time.Second, time.Minute, resolve, alert, defaultConflictAttemptTimeout)

	if alerts != 2 {
		t.Errorf("ConflictAlert called %d times, want 2", alerts)
	}
}

// TestLoopHealthyCycleResetsAlerted proves a successful sync cycle — not only a
// successful resolution — clears the dedup, restoring the full retry budget and
// letting a later conflict post a second card.
func TestLoopHealthyCycleResetsAlerted(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second}, 1000)
	h.runner.handle = func(call int) (sync.Result, error) {
		if call == 2 {
			return sync.Result{}, nil
		}
		return sync.Result{}, errors.Wrapf(h.ctx, git.ErrConflict, "boom")
	}

	resolverCalls := 0
	resolve := func(context.Context, string) error {
		resolverCalls++
		return stderrors.New("resolve failed")
	}
	alerts := 0
	alert := func(context.Context, string, error) {
		alerts++
		if alerts == 2 {
			h.cancel()
		}
	}
	h.runLoopWith(t, time.Second, time.Minute, resolve, alert, defaultConflictAttemptTimeout)

	if resolverCalls != 2*MaxConflictAttempts {
		t.Errorf(
			"resolver invoked %d times, want %d (full cap again after the healthy cycle)",
			resolverCalls, 2*MaxConflictAttempts,
		)
	}
	if alerts != 2 {
		t.Errorf("ConflictAlert called %d times, want 2", alerts)
	}
}

// TestLoopConflictAttemptTimeoutBoundsHungResolver proves a resolver blocked
// until its context is done consumes one attempt per timeout.
func TestLoopConflictAttemptTimeoutBoundsHungResolver(t *testing.T) {
	h := newHarness([]time.Duration{10 * time.Second}, 1000)
	h.runner.handle = func(int) (sync.Result, error) {
		return sync.Result{}, errors.Wrapf(h.ctx, git.ErrConflict, "boom")
	}

	var observed []error
	resolve := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		observed = append(observed, ctx.Err())
		return ctx.Err()
	}
	alert := func(context.Context, string, error) {
		h.cancel()
	}
	h.runLoopWith(t, time.Second, time.Minute, resolve, alert, 20*time.Millisecond)

	if len(observed) != MaxConflictAttempts {
		t.Fatalf("resolver invoked %d times, want %d", len(observed), MaxConflictAttempts)
	}
	for i, err := range observed {
		if !stderrors.Is(err, context.DeadlineExceeded) {
			t.Errorf("attempt %d error = %v, want context.DeadlineExceeded", i+1, err)
		}
	}
}
