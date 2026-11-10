---
status: draft
spec: [010-multi-vault-supervisor]
created: "2026-11-10T09:21:02Z"
branch: dark-factory/multi-vault-supervisor
---

# Add the multi-vault supervisor

<summary>
- `git-ai-sync supervise --config <file>` runs one sync loop per configured vault, concurrently.
- A slow vault does not stall the others, and a failing vault does not stop them or take the process down.
- Each vault's loop is the existing watch loop, so the quiet-window, conflict and lock behaviour is unchanged.
- A panic or a non-cancellation return restarts only that vault after one interval.
- On cancellation (SIGINT or SIGTERM) every loop stops and `supervise` returns after all goroutines exit.
- Every successful push logs one line naming the absolute vault path and the pushed commit sha.
- The logged sha is the commit that reached the remote, captured at push time, not `HEAD` at log time.
- A vault whose path is not a repository logs a failure every interval and never stops the others.
- `--config` overrides the default `$HOME/.config/git-ai-sync/config.yaml`; a missing config file exits non-zero naming it.
</summary>

<objective>
Add `internal/supervise` and the `git-ai-sync supervise --config <file>` command: one goroutine per configured vault over the existing `internal/watch` loop, with shared shutdown, panic and return recovery, per-vault isolation, and a `push path=… sha=…` line that names the commit that was actually pushed. **Why:** this is the single process that replaces twelve per-vault interpreters, and it is the only way the cutover can start.
</objective>

<context>
Read the approved spec at `specs/in-progress/010-multi-vault-supervisor.md` — Desired Behavior 3-4, Acceptance Criteria 3, 4 and 9, the Failure Modes rows for a vault path that does not exist / one vault's loop panics or returns / a second `supervise` / `SIGTERM` during a cycle, Constraints, and Security. The Go gate is `make precommit-go`, not `make precommit`.

Read `internal/watch/watch.go` in full. `watch.Run(ctx, Options)` validates the repository with `git.FindRepo`, acquires the instance lock, starts the change tracker, logs the startup settings, and runs `loop` until cancellation. `Options` already has injectable `Clock`, `Logger`, `Sleep`, `Tracker` and `Runner` fields; `Runner` is `func(ctx context.Context, repo string, opts sync.Options) (sync.Result, error)` and defaults to `sync.Run`. The loop sleeps `Interval` at the top of each iteration, then decides with the quiet-window tracker. Do NOT change `internal/watch`.

Read `internal/sync/sync.go`: `Result` has `HadChanges`, `Committed`, `CommitErr`, `Pulled`, `Pushed`, `NoChanges`; `Run` returns `(Result, error)`. A clean-worktree pull that pushes sets `Pushed: true`; a dirty cycle that pushes sets `Pushed: true`. A commit refusal sets `CommitErr` and still pulls.

Read `internal/git/git.go` for `runGit`, `FindRepo`, `Strategy`, and the wrapping style. Read `internal/lock/lock.go` for the per-repository lock `watch.Run` already acquires. Read `internal/config/file.go` (added by the previous prompt) for `FileConfig`, `FileOptions` and `LoadFile`.

Read `internal/cli/cli.go` for `newRootCommand`, `newWatchCommand` (the flag-reading pattern), `newSyncCommand`, `commandContext`, and `pathArg`. Read `internal/cli/cli_test.go` for the `Run` / `newRootCommand` test style and `clearEnv`. Read `internal/sync/sync_test.go` for the local bare-remote fixture (`newFixture`, `configure`, `runGit`, `writeFile`) and copy it, including local `user.name` / `user.email`, `commit.gpgsign=false`, `core.hooksPath` pinned to `$(git rev-parse --absolute-git-dir)/hooks`, and `GIT_EDITOR=true`.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every loop and every external command takes the caller's `ctx`; no goroutine outlives the call.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — stdlib `testing`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

The module path is `github.com/bborbe/git-ai-sync`. `internal/supervise` must NOT import `internal/cli`; the CLI wires it.

<!-- OPEN QUESTION for the reviewer: the spec's Failure Modes table says a SIGTERM lets the in-flight sync finish before the loops stop within 5 s. `internal/watch` passes the caller's context to `sync.Run`, and `internal/git.runGit` uses `exec.CommandContext`, so cancellation interrupts an in-flight git command rather than draining it. Making the in-flight cycle finish without changing `internal/watch` would require a detached per-cycle context plus a bounded shutdown drain in `supervise`; the acceptance criteria do not test it. This prompt propagates cancellation (the loop returns within 5 s and no goroutine survives) and does not add a per-cycle timeout that would kill a legitimate large-vault sync. Confirm whether a graceful drain is required. -->
<!-- OPEN QUESTION for the reviewer: `internal/watch` runs the runner only on cycles that pass the quiet-window gate, so a skipped (debouncing) cycle cannot push a metrics heartbeat through this seam. Acceptance Criterion 5 uses two cycles that both sync, so the heartbeat is emitted per sync cycle. If the spec's "every cycle" is meant to include skipped cycles, that requires an `OnTick` hook on `watch.Options`, which would change the watch signature the spec says to keep. Confirm the per-sync-cycle reading. -->
</context>

<requirements>
1. Add this helper to `internal/git/git.go`, keeping every existing function unchanged:

   ```go
   // RevParse resolves ref to its commit sha (git rev-parse <ref>).
   func RevParse(ctx context.Context, repo string, ref string) (string, error)
   ```

   Run `runGit(ctx, repo, "rev-parse", ref)` and return `strings.TrimSpace(out)`. Wrap a command error with `errors.Wrapf(ctx, err, "rev-parse %s", ref)`.

2. Create `internal/supervise/supervise.go` (package `supervise`) with exactly this surface:

   ```go
   // Package supervise runs one sync loop per configured vault.
   package supervise

   // Options configures the supervisor.
   type Options struct {
       Config   config.FileConfig
       Logger   *slog.Logger
       Clock    clock.Clock
       WatchRun func(ctx context.Context, opts watch.Options) error // defaults to watch.Run
       Runner   watch.Runner                                       // defaults to sync.Run
       Sleep    func(ctx context.Context, d time.Duration) error   // defaults to a context-aware sleep
   }

   // Run starts one loop per configured vault and returns after every loop has
   // stopped. It returns nil on context cancellation.
   func Run(ctx context.Context, opts Options) error

   // Label derives the vault label: the directory's last path component,
   // lowercased, every run of non-alphanumeric characters collapsed to one
   // hyphen, leading and trailing hyphens stripped.
   func Label(vault string) string
   ```

   Implementation rules:
   - Import the standard library's `sync` package aliased as `stdsync "sync"` and the internal cycle package as `"github.com/bborbe/git-ai-sync/internal/sync"`; both are needed.
   - `Run`:
     1. Default `Logger` to `slog.Default()`, `Clock` to `clock.New()`, `WatchRun` to `watch.Run`, `Runner` to `sync.Run`, and `Sleep` to a local `sleepContext`.
     2. Log one startup line `logger.Info("supervise started", "vaults", len(opts.Config.Vaults))` before starting the goroutines.
     3. Create a `stdsync.WaitGroup`, add one for each `opts.Config.Vaults` entry, start `go func(vault string){ defer wg.Done(); runVault(ctx, opts, vault) }(vault)` for each, then `wg.Wait()` and `return nil`. `Run` returning is the observable proof that zero goroutines remain.
   - `runVault(ctx, opts, vault)`:
     1. Loop until `ctx.Err() != nil`.
     2. Run one `watch.Run` call inside a closure with a deferred `recover()`. On a panic, log `logger.Error("vault panicked", "path", vault, "panic", r)` and let the closure return normally; the loop must continue (restart after one interval), never re-panic.
     3. After the call, if `ctx.Err() != nil`, return. If the call returned a non-nil error, log `logger.Warn("vault loop returned", "path", vault, "error", err)` (a nil return without cancellation is logged the same way with a nil error string is acceptable only if you log a distinct message; do not crash).
     4. Sleep one interval with `opts.Sleep(ctx, opts.Config.Interval)`; return when the sleep reports an error (which is the cancellation path).
     - A vault whose path is a regular file makes `watch.Run` fail in `git.FindRepo` before the lock; this path must log once per interval and never stop the other vaults.
   - `runCycle`/runner wrapper: build the runner passed to `watch.Options` from `opts.Runner`:
     ```go
     func (o Options) runnerFor(logger *slog.Logger) watch.Runner {
         base := o.Runner
         return func(ctx context.Context, repo string, sopts sync.Options) (sync.Result, error) {
             res, err := base(ctx, repo, sopts)
             if err == nil && res.Pushed {
                 sha, shaErr := git.RevParse(ctx, repo, "HEAD")
                 if shaErr != nil {
                     logger.Warn("resolve pushed sha failed", "path", repo, "error", shaErr)
                 } else {
                     logger.Info("push", "path", repo, "sha", sha)
                 }
             }
             return res, err
         }
     }
     ```
     The `sha` is captured immediately after the push returns, before anything else runs in that goroutine. Do not defer it to a later log site.
   - Pass `WatchRun` `watch.Options` with `Path: vault`, `Interval`, `MaxWait`, `CommitPrefix` and `Strategy: git.Strategy(opts.Config.Strategy)` from `opts.Config`, `Clock: opts.Clock`, `Logger: opts.Logger`, `Sleep: opts.Sleep`, and `Runner: opts.runnerFor(opts.Logger)`. Leave `Tracker` nil so the production change tracker is used.
   - `Label`:
     ```go
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
     ```
     This is the derivation `docs/launchd-service.md` documents: `Personal` → `personal`, `My Vault` → `my-vault`, `Work-Notes` → `work-notes`.
   - `sleepContext(ctx, d)` uses `time.NewTimer(d)` with a `defer timer.Stop()` and selects on `ctx.Done()` and `timer.C`; it returns `ctx.Err()` on cancellation and nil when the timer fires.

3. Wire the command in `internal/cli/cli.go`:
   - Add two shared helpers used by this and later commands:
     ```go
     func addConfigFlag(cmd *cobra.Command) {
         cmd.Flags().String("config", "", "Path to the config file")
     }

     func fileOptions(ctx context.Context, cmd *cobra.Command) (config.FileOptions, error) {
         var opts config.FileOptions
         if cmd.Flags().Changed("config") {
             value, err := cmd.Flags().GetString("config")
             if err != nil {
                 return opts, errors.Wrapf(ctx, err, "read --config")
             }
             opts.ConfigPath = &value
         }
         if cmd.Flags().Changed("interval") {
             value, err := cmd.Flags().GetInt("interval")
             if err != nil {
                 return opts, errors.Wrapf(ctx, err, "read --interval")
             }
             opts.Interval = &value
         }
         if cmd.Flags().Changed("strategy") {
             value, err := cmd.Flags().GetString("strategy")
             if err != nil {
                 return opts, errors.Wrapf(ctx, err, "read --strategy")
             }
             opts.Strategy = &value
         }
         if cmd.Flags().Changed("log-level") {
             value, err := cmd.Flags().GetString("log-level")
             if err != nil {
                 return opts, errors.Wrapf(ctx, err, "read --log-level")
             }
             opts.LogLevel = &value
         }
         return opts, nil
     }
     ```
     `cmd.Flags().Changed(name)` returns false when the flag does not exist, so the same helper works for commands that define only some of the flags.
   - Add `newSuperviseCommand(ctx context.Context) *cobra.Command`:
     - `Use: "supervise"`, `Short: "Run one sync loop per configured vault"`, `Args: cobra.NoArgs`.
     - Register `--config`, `--interval` (int, default -1), `--strategy` (string), `--log-level` (string).
     - `RunE`: `runCtx := commandContext(ctx, cmd)`; `opts, err := fileOptions(runCtx, cmd)`; `cfg, err := config.LoadFile(runCtx, opts)`; `logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)`; `return supervise.Run(runCtx, supervise.Options{Config: cfg, Logger: logger, Clock: clock.New()})`.
     - A missing config file therefore returns the `LoadFile` error, which names the resolved path, before any loop starts.
   - Register it in `newRootCommand` with `rootCmd.AddCommand(newSuperviseCommand(ctx))`.
   - Add imports for `internal/supervise` and the existing `internal/clock`. `log/slog` is already imported.

4. Create `internal/supervise/supervise_test.go` (package `supervise`) with a local bare-remote fixture modelled on `internal/sync/sync_test.go`:
   - A helper that creates a bare remote (`git init --bare`, `symbolic-ref HEAD refs/heads/master`), configures it and the clone with local `user.name` / `user.email`, `commit.gpgsign=false`, and `core.hooksPath` pinned to the repository's own `$(git rev-parse --absolute-git-dir)/hooks`; writes `note.md` and `.gitignore` (`/.git-ai-sync.lock\n`), commits, and `git push -u origin master`.
   - A synchronized log buffer (a `bytes.Buffer` guarded by a `stdsync.Mutex`) and a `waitForLine(t, buf, substr, timeout)` helper that polls until the substring appears or the timeout expires.
   - `t.Setenv("GIT_EDITOR", "true")`; every test uses `t.TempDir()` and is not parallel. The suite must pass with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.
   - `TestSuperviseRunsVaultsConcurrently` (Acceptance Criterion 3, first clause): two repositories, `Config{Vaults: [a, b], Interval: time.Second, MaxWait: time.Minute, CommitPrefix: "auto", Strategy: "merge"}`. Write a change into each clone, start `Run` in a goroutine with a cancellable context and a `done` channel, wait for one `push path=<a>` and one `push path=<b>` line, cancel, and require `Run` to return within 5 s. Assert both bare remotes moved and each clone's `HEAD` equals its remote's `master`.
   - `TestSuperviseSlowVaultDoesNotStallOthers` (Acceptance Criterion 3, second clause): two repositories; write a `pre-receive` hook into vault A's bare remote that runs `sleep 3`, then `exit 0`; write changes into both clones; start `Run` with a 1 s interval; require vault B's `push path=<b>` line within 2.5 s of starting while A is blocked; cancel.
   - `TestPushLineNamesThePushedCommit` (Acceptance Criterion 4): one repository; write a change; start `Run`, wait for the `push path=<repo> sha=<sha>` line, cancel. Extract the sha from the line. Assert it equals `git -C <bare> rev-parse refs/heads/master`, and that `git -C <bare> log -1 --format=%s <sha>` starts with the configured commit prefix. Then commit another change in the clone's worktree and assert the already-captured line still names the pushed sha (the line is immutable; do not re-read `HEAD` and compare it to the captured sha).
   - `TestFailingVaultDoesNotStopOthers` (Acceptance Criterion 9): three vaults — two real repositories and one path that is a regular file written with `os.WriteFile`. Start `Run` with a 1 s interval; after 3 s assert: the log names the failing path at least three times (one per interval), each real vault produced at least two `push path=` lines, `Run` has not returned, and no goroutine panicked. Then cancel and require `Run` to return within 5 s.
   - `TestLabel` (table): `Personal` → `personal`, `My Vault` → `my-vault`, `Work-Notes` → `work-notes`, `/a/b/Personal` → `personal`, `a__b` → `a-b`.

5. Append one bullet under the existing `## Unreleased` section in `CHANGELOG.md` (created by the previous prompt; create it if absent, never a second one):

   ```markdown
   - feat: add `internal/supervise` and `git-ai-sync supervise --config`, one goroutine per configured vault over the existing watch loop, with shared shutdown, panic and return recovery, per-vault isolation, and a `push path=… sha=…` line naming the pushed commit
   ```
</requirements>

<constraints>
- `internal/watch` keeps its current signature and behaviour. `supervise` calls `watch.Run` per vault rather than reimplementing the loop.
- Cancellation stops every loop; `Run` returns within 5 s of cancellation and no goroutine survives. Do not add a per-cycle timeout that would kill a legitimate large-vault sync.
- A second `supervise` over the same vault is refused by the existing per-repository lock inside `watch.Run`; do not add a second lock.
- One goroutine per configured vault; a slow or failing vault must not stall or stop the others.
- A panic in one vault's loop is recovered and logged; a non-cancellation return is logged and the loop restarts after one interval. Neither takes the process down.
- On context cancellation (including SIGINT/SIGTERM, which `cli.Execute` already routes into the context) every loop stops and `Run` returns after all goroutines exit.
- The `push` line names the commit that was pushed, captured at push time, not `HEAD` at log time.
- `supervise` requires a config file; a missing or unreadable file exits non-zero naming the resolved path and starts no loop.
- `internal/supervise` must NOT import `internal/cli`.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code.
- No test may touch the real launchd domain, the operator's `~/.config`, or a real pushgateway; this prompt needs none of them.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT modify `internal/config`, `internal/watch`, `internal/sync` (only `internal/git` gains `RevParse`), `internal/lock`, `internal/quietwindow`, `internal/changetracker`, `internal/logging`, or `internal/clock`; the CLI gains only the supervise command and the two shared helpers.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/supervise/... -count=1 -v
go test ./internal/git/... -count=1 -v
go test ./... -count=1
make precommit-go
```

Expected: the build, vet, both package test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing.

Before finishing, walk the assigned criteria against the tests:
- two vaults run concurrently, both push, the call returns within 5 s after cancellation, and a blocked push in one vault does not delay the other's `push` line (Acceptance Criterion 3);
- the `sha=` in the `push path=` line equals the bare remote's `refs/heads/<branch>` and keeps naming that commit after a later worktree commit (Acceptance Criterion 4);
- a vault whose path is a regular file logs a failure every interval while the other vaults keep pushing and the process stays alive (Acceptance Criterion 9).

The full command-level "supervise exits non-zero with no config file" assertion is exercised in the lifecycle prompt, which owns Acceptance Criterion 2; `LoadFile`'s error already names the path. The signal-to-context wiring is `cli.Execute`'s existing `signal.NotifyContext`; the test cancels the context directly, which is the same seam.

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
