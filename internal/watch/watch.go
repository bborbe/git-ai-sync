// Package watch runs the poll, debounce, and sync loop.
package watch

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bborbe/errors"

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

const (
	// MaxConflictAttempts bounds how many times the loop tries to resolve one
	// conflicted pull before it alerts.
	MaxConflictAttempts = 3
	// conflictRetryDelay is the pause between resolution attempts.
	conflictRetryDelay = time.Minute
	// defaultConflictAttemptTimeout bounds one resolver call so a hung model
	// consumes an attempt instead of blocking the loop forever.
	defaultConflictAttemptTimeout = 2 * time.Minute
)

// Options configures the watch command. Clock, Sleep, Tracker and Runner default
// to production implementations when nil; tests inject fakes.
//
// ConflictResolver attempts to resolve a conflicted pull; nil keeps the loop's
// original behaviour of returning the conflict error. ConflictAlert is called
// once per unresolved conflict cycle; nil disables the alert. ConflictAttemptTimeout
// bounds one resolver call and defaults to defaultConflictAttemptTimeout when zero.
type Options struct {
	Path                   string
	Interval               time.Duration
	MaxWait                time.Duration
	CommitPrefix           string
	Strategy               git.Strategy
	Clock                  clock.Clock
	Logger                 *slog.Logger
	Sleep                  SleepFunc
	Tracker                Tracker
	Runner                 Runner
	ConflictResolver       func(ctx context.Context, repo string) error
	ConflictAlert          func(ctx context.Context, repo string, cause error)
	ConflictAttemptTimeout time.Duration
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
	attemptTimeout := opts.ConflictAttemptTimeout
	if attemptTimeout == 0 {
		attemptTimeout = defaultConflictAttemptTimeout
	}

	return loop(
		ctx, repo, opts.Interval, opts.MaxWait, opts.CommitPrefix, opts.Strategy,
		clk, tracker, runner, sleep, opts.ConflictResolver, opts.ConflictAlert,
		attemptTimeout, logger,
	)
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
	resolve func(ctx context.Context, repo string) error,
	alert func(ctx context.Context, repo string, cause error),
	attemptTimeout time.Duration,
	logger *slog.Logger,
) error {
	windowTracker := quietwindow.NewTracker(clk.Now())
	// alerted dedups only this loop's own card. The supervisor's stall path
	// dedups separately on deps.tracker.outstanding, and runnerFor still counts
	// each ErrConflict cycle as a stall failure.
	alerted := false
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
				if resolve == nil {
					return err
				}
				resolved := false
				var lastErr error
				for attempt := 1; attempt <= MaxConflictAttempts; attempt++ {
					select {
					case <-ctx.Done():
						return nil
					default:
					}
					attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
					resolveErr := resolve(attemptCtx, repo)
					cancel()
					if resolveErr == nil {
						logger.Info(fmt.Sprintf("[%d] conflict resolved", iteration))
						windowTracker.RecordSuccess(clk.Now())
						resolved = true
						break
					}
					lastErr = resolveErr
					logger.Error(fmt.Sprintf(
						"[%d] conflict resolution attempt %d/%d failed: %v",
						iteration, attempt, MaxConflictAttempts, resolveErr,
					))
					if attempt == MaxConflictAttempts {
						break
					}
					if sleepErr := sleep(ctx, conflictRetryDelay); sleepErr != nil {
						if ctx.Err() != nil {
							return nil
						}
						return errors.Wrapf(ctx, sleepErr, "sleep before conflict retry")
					}
				}
				if resolved {
					alerted = false
					continue
				}
				files, listErr := git.ConflictedFiles(ctx, repo)
				if listErr != nil {
					logger.Warn(fmt.Sprintf("[%d] list conflicted files failed: %v", iteration, listErr))
				} else if len(files) > 0 {
					logger.Error(fmt.Sprintf("[%d] unresolved conflicted files: %s", iteration, strings.Join(files, ", ")))
				}
				if alert != nil && !alerted {
					alert(ctx, repo, lastErr)
					alerted = true
				}
				continue
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
