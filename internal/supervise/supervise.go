// Package supervise runs one sync loop per configured vault.
package supervise

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/config"
	"github.com/bborbe/git-ai-sync/internal/git"
	"github.com/bborbe/git-ai-sync/internal/sync"
	"github.com/bborbe/git-ai-sync/internal/watch"
)

// Options configures the supervisor.
type Options struct {
	Config   config.FileConfig
	Logger   *slog.Logger
	Clock    clock.Clock
	WatchRun func(ctx context.Context, opts watch.Options) error // defaults to watch.Run
	Runner   watch.Runner                                        // defaults to sync.Run
	Sleep    func(ctx context.Context, d time.Duration) error    // defaults to a context-aware sleep
}

// Run starts one loop per configured vault and returns after every loop has
// stopped. It returns nil on context cancellation.
func Run(ctx context.Context, opts Options) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.New()
	}
	if opts.WatchRun == nil {
		opts.WatchRun = watch.Run
	}
	if opts.Runner == nil {
		opts.Runner = sync.Run
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}

	collector, err := newStateCollector(ctx, opts.Config.StateFile, opts.Logger)
	if err != nil {
		return errors.Wrapf(ctx, err, "create state collector")
	}
	deps := runnerDeps{
		metrics:   newMetrics(opts.Config.Pushgateway, opts.Logger),
		collector: collector,
		failures:  &failureCounter{},
	}

	opts.Logger.Info("supervise started", "vaults", len(opts.Config.Vaults))

	var wg stdsync.WaitGroup
	for _, vault := range opts.Config.Vaults {
		wg.Add(1)
		go func(vault string) {
			defer wg.Done()
			runVault(ctx, opts, vault, deps)
		}(vault)
	}
	wg.Wait()
	return nil
}

func runVault(ctx context.Context, opts Options, vault string, deps runnerDeps) {
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					opts.Logger.Error("vault panicked", "path", vault, "panic", r)
				}
			}()
			err := opts.WatchRun(ctx, watch.Options{
				Path:         vault,
				Interval:     opts.Config.Interval,
				MaxWait:      opts.Config.MaxWait,
				CommitPrefix: opts.Config.CommitPrefix,
				Strategy:     git.Strategy(opts.Config.Strategy),
				Clock:        opts.Clock,
				Logger:       opts.Logger,
				Sleep:        opts.Sleep,
				Runner:       opts.runnerFor(opts.Logger, deps),
			})
			if ctx.Err() != nil {
				return
			}
			opts.Logger.Warn("vault loop returned", "path", vault, "error", err)
		}()

		if err := opts.Sleep(ctx, opts.Config.Interval); err != nil {
			return
		}
	}
}

type runnerDeps struct {
	metrics   *metricsClient
	collector *stateCollector
	failures  *failureCounter
}

// failureCounter tracks each vault's consecutive failed cycles for the state
// file. The vault goroutines share it.
type failureCounter struct {
	mutex  stdsync.Mutex
	counts map[string]int
}

// record increments the vault's failure count on failure, resets it on success,
// and returns the current count.
func (c *failureCounter) record(repo string, success bool) int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	if success {
		c.counts[repo] = 0
		return 0
	}
	c.counts[repo]++
	return c.counts[repo]
}

func (o Options) runnerFor(logger *slog.Logger, deps runnerDeps) watch.Runner {
	base := o.Runner
	return func(ctx context.Context, repo string, sopts sync.Options) (sync.Result, error) {
		res, err := base(ctx, repo, sopts)
		now := o.Clock.Now()
		success := err == nil && res.CommitErr == nil

		if err == nil && res.Pushed {
			sha, shaErr := git.RevParse(ctx, repo, "HEAD")
			if shaErr != nil {
				logger.Warn("resolve pushed sha failed", "path", repo, "error", shaErr)
			} else {
				logger.Info("push", "path", repo, "sha", sha)
			}
		}

		if deps.metrics.enabled() && deps.metrics.push(ctx, Label(repo), now, success) {
			deps.collector.RecordMetricsPush(ctx, now)
		}

		if success {
			deps.failures.record(repo, true)
			deps.collector.RecordSuccess(ctx, repo, now)
		} else {
			lastError := ""
			if err != nil {
				lastError = err.Error()
			} else if res.CommitErr != nil {
				lastError = res.CommitErr.Error()
			}
			count := deps.failures.record(repo, false)
			deps.collector.RecordFailure(ctx, repo, now, count, lastError)
		}
		return res, err
	}
}

// Label derives the vault label: the directory's last path component,
// lowercased, every run of non-alphanumeric characters collapsed to one
// hyphen, leading and trailing hyphens stripped.
func Label(vault string) string {
	base := strings.ToLower(filepath.Base(vault))
	var b strings.Builder
	lastHyphen := false
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteRune('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
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
