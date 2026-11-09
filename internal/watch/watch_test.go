package watch

import (
	"bytes"
	"context"
	stderrors "errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

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
	logger := logging.New(h.buf, slog.LevelInfo)
	if err := loop(h.ctx, "/repo", interval, maxWait, "auto", git.StrategyMerge, h.fake, h.tracker, h.runner.run, h.sleep, logger); err != nil {
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
