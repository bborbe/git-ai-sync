// Package watch runs the poll, debounce, and sync loop.
package watch

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bborbe/git-ai-sync/internal/changetracker"
	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/lock"
	"github.com/bborbe/git-ai-sync/internal/quietwindow"
	"github.com/bborbe/git-ai-sync/internal/sync"
)

// Tracker is the change-age source the loop consumes.
type Tracker interface {
	Start(ctx context.Context) error
	Stop()
	SecondsSinceLastChange(now time.Time) time.Duration
}

// Runner runs one sync cycle. Production passes sync.Run.
type Runner func(ctx context.Context, repo string, opts sync.Options) (sync.Result, error)

// SleepFunc waits for d or until ctx is cancelled.
type SleepFunc func(ctx context.Context, d time.Duration) error

// Options configures the watch command. Clock, Sleep, Tracker and Runner default
// to production implementations when nil; tests inject fakes.
type Options struct {
	Path         string
	Interval     time.Duration
	MaxWait      time.Duration
	CommitPrefix string
	Strategy     git.Strategy
	Clock        clock.Clock
	Logger       *slog.Logger
	Sleep        SleepFunc
	Tracker      Tracker
	Runner       Runner
}

// Run validates the repository, acquires the instance lock, starts the change
// tracker, logs the startup settings, and runs the poll loop until cancellation.
func Run(ctx context.Context, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	repo, err := git.FindRepo(ctx, opts.Path)
	if err != nil {
		return err
	}

	l, err := lock.Acquire(ctx, repo)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(ctx); releaseErr != nil {
			logger.Warn(fmt.Sprintf("failed to release lock: %v", releaseErr))
		}
	}()

	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}

	tracker := opts.Tracker
	if tracker == nil {
		tracker = changetracker.New(repo, clk)
	}
	if err := tracker.Start(ctx); err != nil {
		return err
	}
	defer tracker.Stop()

	logger.Info(fmt.Sprintf(
		"Starting watch mode: path=%s, interval=%s, max_wait=%s",
		repo, opts.Interval, opts.MaxWait,
	))
	if opts.MaxWait < opts.Interval {
		logger.Warn(fmt.Sprintf(
			"GIT_AI_SYNC_MAX_WAIT=%s is below interval=%s: the debounce is effectively disabled — every cycle syncs",
			opts.MaxWait, opts.Interval,
		))
	}

	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	runner := opts.Runner
	if runner == nil {
		runner = sync.Run
	}

	return loop(ctx, repo, opts.Interval, opts.MaxWait, opts.CommitPrefix, opts.Strategy, clk, tracker, runner, sleep, logger)
}

func loop(
	ctx context.Context,
	repo string,
	interval time.Duration,
	maxWait time.Duration,
	commitPrefix string,
	strategy git.Strategy,
	clk clock.Clock,
	tracker Tracker,
	runner Runner,
	sleep SleepFunc,
	logger *slog.Logger,
) error {
	windowTracker := quietwindow.NewTracker(clk.Now())
	iteration := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		iteration++
		if err := sleep(ctx, interval); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		now := clk.Now()
		secondsSinceChange := tracker.SecondsSinceLastChange(now)
		decision := windowTracker.Decide(secondsSinceChange, interval, maxWait, now)
		if !decision.Sync {
			logger.Info(fmt.Sprintf(
				"[%d] Skipping - files changed %.1fs ago (still editing; quiet window %.1fs)",
				iteration, secondsSinceChange.Seconds(), decision.Window.Seconds(),
			))
			windowTracker.RecordSkip()
			continue
		}
		if decision.CapFired && secondsSinceChange < decision.Window {
			logger.Info(fmt.Sprintf(
				"[%d] Maximum wait %.0fs reached — syncing regardless of quiet",
				iteration, maxWait.Seconds(),
			))
		}
		logger.Info(fmt.Sprintf("[%d] Checking...", iteration))
		result, err := runner(ctx, repo, sync.Options{
			Strategy:      strategy,
			CommitPrefix:  commitPrefix,
			PullWhenClean: true,
			Clock:         clk,
		})
		if err != nil {
			if stderrors.Is(err, git.ErrConflict) {
				logger.Error(fmt.Sprintf("[%d] conflict: %v", iteration, err))
				return err
			}
			logger.Error(fmt.Sprintf("[%d] sync failed: %v", iteration, err))
			continue
		}
		if result.CommitErr != nil {
			logger.Error(fmt.Sprintf("[%d] commit refused; pull ran: %v", iteration, result.CommitErr))
			continue
		}
		windowTracker.RecordSuccess(now)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
