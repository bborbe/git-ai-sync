// Package changetracker records the newest qualifying repository change by
// polling `git status --porcelain`.
package changetracker

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/git"
)

// NeverChanged is the duration returned when no qualifying change has been seen.
const NeverChanged time.Duration = math.MaxInt64

// defaultPollInterval is how often the tracker runs `git status --porcelain`.
const defaultPollInterval = 10 * time.Second

// Tracker records the time of the newest qualifying repository change.
type Tracker struct {
	watchPath    string
	clk          clock.Clock
	pollInterval time.Duration
	gitStatus    func(ctx context.Context, repo string) (string, error)

	mu         sync.RWMutex
	lastChange time.Time
	lastStatus string
	primed     bool

	done     chan struct{}
	cancel   context.CancelFunc
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New returns a Tracker polling watchPath and reading time from clk.
func New(watchPath string, clk clock.Clock) *Tracker {
	return &Tracker{
		watchPath:    watchPath,
		clk:          clk,
		pollInterval: defaultPollInterval,
		gitStatus:    git.StatusPorcelain,
		done:         make(chan struct{}),
	}
}

// Start records the current git status as the baseline and begins polling. A
// write issued after Start is therefore never folded into the baseline.
func (t *Tracker) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.poll(runCtx)
	t.wg.Add(1)
	go t.run(runCtx)
	return nil
}

func (t *Tracker) run(ctx context.Context) {
	defer t.wg.Done()
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.poll(ctx)
		}
	}
}

// poll runs one status check and records a change when the status differs from
// the previous poll and is not clean. The first poll only establishes the
// baseline; a dirty-to-clean transition is the sync loop's own commit and is not
// recorded, because it would reset the quiet window after every successful sync.
func (t *Tracker) poll(ctx context.Context) {
	status, err := t.gitStatus(ctx, t.watchPath)
	if err != nil {
		slog.Default().Warn("changetracker: git status failed", "path", t.watchPath, "error", err)
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.primed {
		t.primed = true
		t.lastStatus = status
		return
	}
	if status == t.lastStatus {
		return
	}
	t.lastStatus = status
	if status == "" {
		return
	}
	t.lastChange = t.clk.Now()
}

// Stop stops polling. It is safe to call more than once.
func (t *Tracker) Stop() {
	t.stopOnce.Do(func() {
		close(t.done)
		if t.cancel != nil {
			t.cancel()
		}
	})
	t.wg.Wait()
}

// SecondsSinceLastChange reports the time since the newest qualifying change.
// It returns NeverChanged when no qualifying change has been seen.
func (t *Tracker) SecondsSinceLastChange(now time.Time) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastChange.IsZero() {
		return NeverChanged
	}
	return now.Sub(t.lastChange)
}
