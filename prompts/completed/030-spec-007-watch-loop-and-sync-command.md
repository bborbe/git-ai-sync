---
status: completed
spec: [007-go-sync-loop-prototype]
summary: Prompts 1–4 were absent, so I created the full Go module (scaffold, config, logging, git, sync, quietwindow, changetracker, lock) and wired the watch loop and sync/watch CLI commands; all decay, cap, reset, clean-pull, conflict and lock tests pass.
execution_id: 8557c7c1-90b2-5210-af4e-6b1dcc28efaa-exec-030-spec-007-watch-loop-and-sync-command
dark-factory-version: dev
created: "2026-11-09T18:54:14Z"
queued: "2026-11-09T19:15:50Z"
started: "2026-11-09T19:15:52Z"
completed: "2026-11-09T19:25:10Z"
branch: dark-factory/go-sync-loop-prototype
---

<!--
Open question surfaced at generation time: the spec does not say whether a commit refusal
resets the decay and the maximum-wait clock. This prompt treats a commit refusal as a failed
cycle (no reset), because the local commit did not happen and resetting the cap clock would
postpone the next forced attempt. The pull still runs, matching Desired Behavior 5.
-->

# Wire the watch loop and the sync command

<summary>
- The `watch` command polls at the interval, skips while files change, and logs the decaying window.
- A cycle forced by the maximum wait says so, and a cycle the quiet window would have accepted anyway does not.
- A successful cycle resets the decay and the cap clock; a failed cycle leaves both untouched.
- The watch loop pulls on a clean worktree; the one-shot `sync` reports "no changes to sync" and does not pull.
- A pull conflict stops the process; other failures are logged and the loop continues.
- The watch loop stops on SIGINT/SIGTERM and releases the lock.
- A second `watch` on the same repository exits non-zero.
- The binary runs git itself; it never invokes an interpreter.
- The one-shot `sync` stages, commits, pulls, and pushes a dirty worktree.
</summary>

<objective>
Wire the pieces into `internal/watch` and the `sync` / `watch` CLI commands: the poll loop, the decaying gate with the exact log strings and cap suppression, success/failure classification, signal handling, lock acquisition and release, and the one-shot sync semantics. **Why:** this is the observable behaviour the port exists to prove.
</objective>

<context>
Read the approved spec at `specs/in-progress/007-go-sync-loop-prototype.md` — Desired Behavior 1, 4, 5, 6 and 8, the Failure Modes table, and Acceptance Criteria 10, 11, 12, 14, 15 and 16 are the contract for this prompt.

Read `src/git_ai_sync/__main__.py` (`cmd_watch` and `cmd_sync`) for the log strings, the one-based iteration counter, the cap-suppression condition (`decision.cap_fired and seconds_since_change < decision.window`), the success reset, and the one-shot difference.

Read the packages from prompts 1–4: `internal/quietwindow/quietwindow.go`, `internal/changetracker/changetracker.go`, `internal/git/git.go`, `internal/sync/sync.go`, `internal/lock/lock.go`, `internal/config/config.go`, `internal/clock/clock.go`, `internal/logging/logging.go`, and `internal/cli/cli.go`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the loop checks cancellation every iteration and exits gracefully.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `log/slog` to stderr with structured key-value pairs; the per-cycle messages are passed as the message string.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the test conventions, but use stdlib `testing` (not Ginkgo/Gomega).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md` — keep the `Execute()` / `Run(ctx, args)` split and cobra's `RunE`.

The module path is `github.com/bborbe/git-ai-sync`. Do not include the word `python` (any case) in any Go source file under `cmd/` or `internal/` — Acceptance Criterion 15 greps for it. The only subprocess the binary may run is `git`.
</context>

<requirements>
1. Create `internal/watch/watch.go`:

   ```go
   package watch

   import (
       "context"
       "log/slog"
       "time"

       "github.com/bborbe/git-ai-sync/internal/clock"
       "github.com/bborbe/git-ai-sync/internal/git"
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
   func Run(ctx context.Context, opts Options) error
   ```

   Implement `Run` in this order:

   a. Resolve `logger`: `opts.Logger` or `slog.Default()`.

   b. `repo, err := git.FindRepo(ctx, opts.Path)`; return the error on failure. The repository is validated before the lock is acquired.

   c. `l, err := lock.Acquire(ctx, repo)`; return the error on failure (it wraps `lock.ErrLocked` and names the holder's PID). `defer` a release that logs a warning if `l.Release(ctx)` returns an error.

   d. Resolve `clk := opts.Clock` or `clock.New()`.

   e. Resolve `tracker := opts.Tracker` or `changetracker.New(repo, clk)`; call `tracker.Start(ctx)` and return the error on failure; `defer tracker.Stop()`.

   f. Log startup at info level: the repository, the interval and the maximum wait. When `opts.MaxWait < opts.Interval`, log a warning whose message contains `debounce is effectively disabled` and `every cycle syncs`, naming both values. Do not rewrite or clamp either value.

   g. Resolve `sleep := opts.Sleep` or `sleepContext`, and `runner := opts.Runner` or `sync.Run`.

   h. Return `loop(ctx, repo, opts.Interval, opts.MaxWait, opts.CommitPrefix, opts.Strategy, clk, tracker, runner, sleep, logger)`.

2. Implement the unexported loop exactly as follows. The log strings are frozen from the iteration prefix onward; do not reword them.

   ```go
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
   ```

   Invariants:
   - The iteration counter is one-based; the first emitted cycle is `[1]`.
   - The skip line uses one decimal place on both numbers (`%.1fs`).
   - The cap line uses the em dash (`—`), no decimal place on the maximum wait (`%.0fs`), and is emitted only when `decision.CapFired && secondsSinceChange < decision.Window`. When the window would have accepted the cycle anyway, no cap line is emitted.
   - A skip calls `RecordSkip` and does not call `RecordSuccess`.
   - A runner error other than a conflict is logged and the loop continues without `RecordSuccess`. A conflict returns the error so the process exits non-zero.
   - A commit refusal (`result.CommitErr != nil`) is logged and the loop continues without `RecordSuccess`; the pull already ran inside the cycle.
   - Only a cycle that returns no error and has no `CommitErr` calls `RecordSuccess(now)`.
   - The loop returns nil when the context is cancelled, so SIGINT/SIGTERM is a graceful exit with the lock released by the deferred call.

3. Implement `sleepContext`:

   ```go
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
   ```

4. Modify `internal/cli/cli.go` to replace the two stubs from prompt 1 with the real wiring. Keep `Execute`, `Run` and `newRootCommand` and keep `TestVersionPrintsOneLine` passing.

   a. `watch` subcommand `RunE`: build `config.Flags` from `cmd.Flags().Changed("interval")`, `cmd.Flags().Changed("strategy")` and `cmd.Flags().Changed("log-level")`; call `config.Load(ctx, flags)`; build the logger with `logging.New(os.Stderr, cfg.LogLevel)`; take the optional positional `path` (default `"."`); return `watch.Run(ctx, watch.Options{Path: path, Interval: cfg.Interval, MaxWait: cfg.MaxWait, CommitPrefix: cfg.CommitPrefix, Strategy: git.Strategy(cfg.Strategy), Logger: logger})`.

   b. `sync` subcommand `RunE`: build `config.Flags` from `cmd.Flags().Changed("strategy")` and `cmd.Flags().Changed("log-level")`; call `config.Load(ctx, flags)`; build the logger; take the optional positional `path` (default `"."`); `repo, err := git.FindRepo(ctx, path)`; `l, err := lock.Acquire(ctx, repo)` and defer a release that logs a warning on error; `result, err := sync.Run(ctx, repo, sync.Options{Strategy: git.Strategy(cfg.Strategy), CommitPrefix: cfg.CommitPrefix, PullWhenClean: false, Clock: clock.New()})`.
      - On a non-nil error, log it and return it (exit non-zero). A conflict is reported by `git.ErrConflict`.
      - When `result.NoChanges` is true, log `no changes to sync` and return nil without pulling (the one-shot behaviour).
      - When `result.CommitErr` is non-nil, log it and return it (the cycle already ran the pull and push; the one-shot exits non-zero for a refused commit).
      - Otherwise log success and return nil.

   c. Do not add `resolve`, `status`, `doctor`, `config` or any other subcommand.

5. Keep `cmd/git-ai-sync/main.go` calling `cli.Execute()`. `Execute` must use `signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)`, map a nil result to exit 0, map a `context.Canceled` error to a graceful exit 0, and map any other error to a message on stderr plus `os.Exit(1)`.

6. Add `internal/watch/watch_test.go` (package `watch`, so it can call the unexported `loop`). Provide a fake tracker (`Start`/`Stop` no-ops and a `SecondsSinceLastChange` that returns the next value from a slice, repeating the last), a fake sleep that advances a `clocktest.Fake` and cancels the context after N calls, a recording runner, and a `bytes.Buffer` logger built with `logging.New(&buf, slog.LevelInfo)`. Cover:

   a. **Decay sequence (AC10).** interval `4s`, maxWait `300s`, tracker always `500ms`, runner records calls. Run `loop` for three cycles, then cancel. Assert the buffer contains, in order, `[1] Skipping - files changed 0.5s ago (still editing; quiet window 4.0s)`, `[2] ... quiet window 2.0s`, `[3] ... quiet window 1.0s`, and that the runner was never called.

   b. **Cap forced (AC11).** interval `1s`, maxWait `3s`, tracker always `100ms`. The runner records one call, cancels the context, and returns an empty result. Assert the buffer contains `[3] Maximum wait 3s reached — syncing regardless of quiet` and the runner was called once.

   c. **Cap suppressed (AC12).** interval `1s`, maxWait `1s`, tracker always `2s` (the change is older than the window). The runner cancels and returns an empty result. Assert the buffer contains `[1] Checking...` and does not contain `Maximum wait`.

   d. **Success resets the decay (AC4).** interval `1s`, maxWait `300s`, tracker returns `10s` then `500ms`. The first cycle is accepted and succeeds; the second must skip and report `quiet window 1.0s` (a stale 0.5s window would report `0.5s`). Cancel after the second cycle.

   e. **A failed cycle keeps the cap clock (AC5).** interval `1s`, maxWait `2s`, tracker returns `10s` then `500ms`. The first cycle is accepted and the runner returns an error; the second cycle must emit `[2] Maximum wait 2s reached — syncing regardless of quiet`. If the failure had reset the clock, the cap would not fire at cycle 2. The runner returns nil and cancels on the second call.

   f. **The loop passes `PullWhenClean: true`** to the runner: capture the `sync.Options` argument and assert `PullWhenClean` is true.

   g. **Second watch refuses (AC16).** Create a temp directory, run `git init`, hold the lock with `lock.Acquire(ctx, repo)`, then call `watch.Run(ctx, watch.Options{Path: repo, Interval: time.Second, MaxWait: time.Minute, CommitPrefix: "auto", Strategy: git.StrategyMerge, Clock: fakeClock, Logger: bufferLogger, Tracker: fakeTracker, Sleep: fakeSleep, Runner: fakeRunner})`. Assert the error is non-nil, `stderrors.Is(err, lock.ErrLocked)` is true, and `err.Error()` contains the current PID. Release the lock.

   h. **The lock is released on exit.** Create a temp directory, run `git init`, call `watch.Run` with an already-cancelled context and a fake tracker. Assert the call returns nil, then assert `lock.Acquire(ctx, repo)` succeeds and release it.

7. Add `internal/cli/cli_test.go` (package `cli`) covering:
   - `TestVersionPrintsOneLine` (from prompt 1) still passes.
   - `TestSyncCommandRejectsSecondInstance`: create a temp directory, run `git init`, hold the lock, call `cli.Run(ctx, []string{"sync", repo})`, and assert the error satisfies `stderrors.Is(err, lock.ErrLocked)`.
   - `TestSyncCommandCleanRepo`: create a temp directory, run `git init`, set `user.email` and `user.name`, write a file, `git add .` and `git commit -m init`, then call `cli.Run(ctx, []string{"sync", repo})` and assert it returns nil (the one-shot reports no changes without pulling).

   The tests may run `git` through `exec.Command`; they must not use the network.
</requirements>

<constraints>
- Do NOT modify `internal/quietwindow`, `internal/changetracker`, `internal/git`, `internal/sync`, `internal/lock`, `internal/config`, `internal/logging`, `internal/clock`, `internal/clock/clocktest`, `internal/version`, `Makefile`, `CLAUDE.md`, or any Python file. If a dependency's API is missing, extend it minimally in this prompt and say so; do not reimplement its logic.
- The log strings are frozen from the iteration prefix onward: `Skipping - files changed <N>s ago (still editing; quiet window <W>s)` with one decimal place, and `Maximum wait <M>s reached — syncing regardless of quiet` with the em dash and no decimal place. Do not reword them.
- The iteration counter is one-based.
- The cap line is suppressed when the window would have accepted the cycle.
- A successful cycle resets the decay and the cap clock; a failed cycle and a commit refusal do not.
- The watch loop pulls on a clean worktree (`PullWhenClean: true`); the one-shot `sync` does not (`PullWhenClean: false`).
- A pull conflict stops the process non-zero; other failures log and continue.
- The watch loop stops on SIGINT/SIGTERM and releases the lock.
- Do NOT invoke an interpreter or a shell; the only subprocess is `git`, run from `internal/git`.
- Do NOT add metrics, notifier, conflict resolution, launchd, `resolve`/`status`/`doctor`/`config` subcommands, a configurable decay factor, or an opt-out flag.
- Do NOT include the word `python` (any case) in any Go source file under `cmd/` or `internal/`.
- `gofmt`, `go vet ./...` and `go test ./... -count=1` must pass. Run `make precommit-go`, not `make precommit`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
go test ./internal/watch/... ./internal/cli/... -count=1 -v
go test ./... -count=1
make test-go
make precommit-go
go build -o /tmp/git-ai-sync ./cmd/git-ai-sync
/tmp/git-ai-sync --version | wc -l
grep -n 'Skipping - files changed' internal/watch/watch.go
grep -n 'Maximum wait' internal/watch/watch.go
grep -n 'syncing regardless of quiet' internal/watch/watch.go
grep -n 'PullWhenClean: true' internal/watch/watch.go
grep -n 'PullWhenClean: false' internal/cli/cli.go
grep -n 'no changes to sync' internal/cli/cli.go
grep -rn 'python' --include='*.go' cmd/ internal/ | wc -l
grep -rn 'exec.Command' internal/ | grep -v '"git"' | wc -l
grep -n 'Use:.*"resolve"\|Use:.*"status"\|Use:.*"doctor"\|Use:.*"config"' internal/cli/cli.go
git diff origin/master -- src/ tests/ pyproject.toml uv.lock
git diff origin/master -- Makefile | grep -cE '^-[^-]' || true
grep -c 'never merge' CLAUDE.md || true
grep -c 'pull --rebase' CLAUDE.md || true
grep -n 'pull --no-rebase' CLAUDE.md
```

Expected: the build, vet, tests and both Make targets exit 0; `--version` prints one line; the skip and cap strings appear; `PullWhenClean: true` appears in the watch loop and `PullWhenClean: false` in the one-shot command; `no changes to sync` appears; the Go-source `python` grep prints `0`; the non-`git` exec grep prints `0`; the extra-subcommand grep prints nothing; the Python diff is empty; the Makefile removed-line count is `0`; the stale CLAUDE.md greps print `0` and `pull --no-rebase` appears.

The first `go test`/`make precommit-go` may fetch Go modules over the network; that is expected. The tests themselves use only local repositories and injected fakes.

Before finishing, walk Acceptance Criteria 10, 11, 12, 14, 15 and 16 against the tests and the wiring: three skip lines with `4.0s`, `2.0s`, `1.0s` and the first prefixed `[1]`; the cap line at the third 1-second cycle and the runner called after it; no cap line when the window accepted the cycle; the loop passing `PullWhenClean: true` while the one-shot passes `false`; no `python` in the Go tree and no interpreter exec; and a second watch refused with the holder's PID while the lock is held.
</verification>
