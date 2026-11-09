---
status: completed
spec: [007-go-sync-loop-prototype]
summary: Implemented and verified internal/quietwindow (decayed window, tracker, pure decision) and internal/changetracker (fsnotify tracking with the ignore set and injected clock) with deterministic unit tests, and extended the Unreleased changelog.
execution_id: 8557c7c1-90b2-5210-af4e-6b1dcc28efaa-exec-033-spec-007-quiet-window-and-change-tracker
dark-factory-version: dev
created: "2026-11-09T18:54:14Z"
queued: "2026-11-09T19:33:49Z"
started: "2026-11-09T19:33:51Z"
completed: "2026-11-09T19:35:32Z"
branch: dark-factory/go-sync-loop-prototype
---

# Implement the decaying quiet window and the change tracker

<summary>
- The required quiet window starts at the poll interval and halves on each consecutive skipped cycle.
- A successful sync restores the full interval and restarts the maximum-wait clock; a failed cycle leaves both untouched.
- The maximum wait and the window both use "at least", so a boundary value forces a sync.
- The change tracker ignores its own noise: directories, `.git/`, backup, swap and temp files, and dot-prefixed names.
- The tracker's clock is injected, so tests advance time without sleeping.
- The decision unit is pure and reads no clock of its own.
- The filesystem tracker watches recursively and can be driven deterministically in tests.
</summary>

<objective>
Implement `internal/quietwindow` (decayed window, tracker, decision) and `internal/changetracker` (filesystem change tracking with the ignore set and an injected clock), with unit tests. **Why:** these are the timing rules the port must reproduce exactly, and the watch loop in prompt 5 consumes them.
</objective>

<context>
Read the approved spec at `specs/in-progress/007-go-sync-loop-prototype.md` — Desired Behavior 2, 3, 6 and 8, and Acceptance Criteria 2, 3, 4, 5, 6 and 13 are the contract for this prompt.

Read `src/git_ai_sync/quiet_window.py` and `src/git_ai_sync/file_watcher.py` for the exact decay arithmetic and the ignore set. Read `specs/in-progress/006-watch-quiet-window-decay.md` for the settled decay and cap semantics.

Read `internal/clock/clock.go` and `internal/clock/clocktest/clocktest.go` from prompt 1. The module path is `github.com/bborbe/git-ai-sync`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — time is injected; the quiet-window module reads no clock, and the change tracker takes a `clock.Clock`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the watcher goroutine stops on a done channel.

Use stdlib `testing` (not Ginkgo/Gomega). Do not include the word `python` (any case) in any Go source file under `cmd/` or `internal/` — Acceptance Criterion 15 greps for it.
</context>

<requirements>
1. Create `internal/quietwindow/quietwindow.go` with exactly this public surface (exported identifiers documented):

   ```go
   package quietwindow

   import "time"

   // DecayedWindow returns the quiet window required after consecutiveSkips skips.
   // It starts at interval and halves on every consecutive skip. Negative skip
   // counts are treated as zero. The result is not clamped or rounded.
   func DecayedWindow(interval time.Duration, consecutiveSkips int) time.Duration

   // Decision is one watch cycle's debounce verdict.
   type Decision struct {
       Sync     bool
       Window   time.Duration
       CapFired bool
   }

   // Tracker holds the consecutive-skip count and the last successful sync time.
   type Tracker struct {
       consecutiveSkips int
       lastSuccess      time.Time
   }

   // NewTracker starts with a zero skip count and the caller's clock reading.
   func NewTracker(now time.Time) *Tracker

   // ConsecutiveSkips reports the consecutive skipped cycles since the last success.
   func (t *Tracker) ConsecutiveSkips() int

   // Window returns the quiet window in force, in seconds.
   func (t *Tracker) Window(interval time.Duration) time.Duration

   // ElapsedSinceSuccess reports the time since the last successful sync.
   func (t *Tracker) ElapsedSinceSuccess(now time.Time) time.Duration

   // Decide reports whether the cycle may sync. The cycle is accepted when the
   // newest change is at least as old as the window in force, or when the maximum
   // wait has elapsed since the last success — whichever happens first.
   func (t *Tracker) Decide(
       secondsSinceChange time.Duration,
       interval time.Duration,
       maxWait time.Duration,
       now time.Time,
   ) Decision

   // RecordSkip counts one skipped cycle, halving the window for the next one.
   func (t *Tracker) RecordSkip()

   // RecordSuccess resets the decay and restarts the maximum-wait clock.
   func (t *Tracker) RecordSuccess(now time.Time)
   ```

   Implementation rules:
   - `DecayedWindow` returns `time.Duration(float64(interval) * math.Pow(0.5, float64(consecutiveSkips)))` after treating a negative skip count as zero. Do not use `interval / (1 << n)`; the float form underflows to zero rather than overflowing.
   - `Window(interval)` delegates to `DecayedWindow(interval, t.consecutiveSkips)` — one implementation, never a second copy.
   - `Decide` computes `window := t.Window(interval)`, `capFired := t.ElapsedSinceSuccess(now) >= maxWait`, and returns `Decision{Sync: capFired || secondsSinceChange >= window, Window: window, CapFired: capFired}`. Both comparisons are `>=` ("at least").
   - `Decide` is a pure read: it must not mutate the tracker. Only `RecordSkip` and `RecordSuccess` mutate state.
   - The module reads no clock and takes no logger; every input arrives as a parameter.

2. Create `internal/quietwindow/quietwindow_test.go` covering:
   - `TestDecayedWindowPinned`: `DecayedWindow(30*time.Second, k)` equals `30s`, `15s`, `7.5s`, `3.75s` for `k = 0, 1, 2, 3`, compared with `==`.
   - `TestDecayedWindowHalvesEveryStep`: for `k = 1..10`, `DecayedWindow(30*time.Second, k) == DecayedWindow(30*time.Second, k-1)/2`, and `k = 0` equals `30s`. This is the property assertion that a three-case lookup table cannot satisfy.
   - `TestDecayResetsAfterSuccess`: `NewTracker(t0)`, `RecordSkip` twice (`Window(30s) == 7.5s`, `ConsecutiveSkips() == 2`), then `RecordSuccess(t0.Add(90*time.Second))`; assert `Window(30s) == 30s`, `ConsecutiveSkips() == 0`, and `ElapsedSinceSuccess(t0.Add(90*time.Second)) == 0`.
   - `TestFailedCycleLeavesDecayAndClockUntouched`: `NewTracker(t0)`, `RecordSkip` twice; at `t0+10s` snapshot `Window(30s) == 7.5s` and `ElapsedSinceSuccess == 10s`; simulate a failure by not calling `RecordSuccess`; at `t0+20s` assert `Window(30s)` is still `7.5s` and `ElapsedSinceSuccess == 20s` (it grew).
   - `TestCapFiresAtExactMaxWait`: `NewTracker(t0)`; `Decide(1*time.Second, 30*time.Second, 300*time.Second, t0.Add(299*time.Second))` has `CapFired == false`; at `t0.Add(300*time.Second)` it has `CapFired == true` and `Sync == true`.
   - `TestWindowBoundaryUsesAtLeast`: `NewTracker(t0)`, `RecordSkip` once (window `15s`); `Decide(15*time.Second, 30*time.Second, 300*time.Second, t0)` has `Sync == true`; `Decide(14*time.Second+999*time.Millisecond, ...)` has `Sync == false`.
   - `TestDecideIsPure`: calling `Decide` twice with the same inputs leaves `ConsecutiveSkips`, `Window` and `ElapsedSinceSuccess` unchanged.

3. Create `internal/changetracker/changetracker.go`:

   ```go
   package changetracker

   import (
       "context"
       "math"
       "time"

       "github.com/bborbe/git-ai-sync/internal/clock"
   )

   // NeverChanged is the duration returned when no qualifying change has been seen.
   const NeverChanged time.Duration = math.MaxInt64

   // ShouldIgnore reports whether a filesystem event is the watcher's own noise.
   // It ignores directories, anything under .git/, *~ backups, *.swp, *.tmp, and
   // dot-prefixed names.
   func ShouldIgnore(path string, isDir bool) bool

   // Tracker records the time of the newest qualifying filesystem change.
   type Tracker struct {
       // unexported fields
   }

   // New returns a Tracker watching watchPath and reading time from clk.
   func New(watchPath string, clk clock.Clock) *Tracker

   // Start begins recursive filesystem watching.
   func (t *Tracker) Start(ctx context.Context) error

   // Stop stops watching. It is safe to call more than once.
   func (t *Tracker) Stop()

   // HandleEvent records a change for path unless ShouldIgnore rejects it. It is
   // exported so tests can drive the ignore and reset rules deterministically.
   func (t *Tracker) HandleEvent(path string, isDir bool)

   // SecondsSinceLastChange reports the time since the newest qualifying change.
   // It returns NeverChanged when no qualifying change has been seen.
   func (t *Tracker) SecondsSinceLastChange(now time.Time) time.Duration
   ```

   Implementation rules:
   - Add `github.com/fsnotify/fsnotify` to `go.mod` and run `go mod tidy`. Use `fsnotify.NewWatcher()`.
   - `ShouldIgnore`: return true for `isDir`; true when `strings.Contains(filepath.ToSlash(path), "/.git/")` or `strings.HasSuffix(filepath.ToSlash(path), "/.git")`; true when the path ends with `~`, `.swp`, or `.tmp`; true when `filepath.Base(path)` starts with `.`. Otherwise false.
   - `Start`: create the watcher, walk `watchPath` with `filepath.WalkDir`, and `Add` every directory except one named `.git` (return `filepath.SkipDir` for `.git`). Start one goroutine that selects on a done channel, `watcher.Events` and `watcher.Errors`; for each event, determine `isDir` with `os.Stat(event.Name)` (a stat error means "not a directory"), and when `event.Has(fsnotify.Create)` and the path is a directory, recursively add it. Call `t.HandleEvent(event.Name, isDir)`. Ignore watcher errors (`_ =`). Wrap a watcher creation or add error with `errors.Wrapf(ctx, err, "watch %s", path)`.
   - `Stop`: close the done channel exactly once (use a `sync.Once`), close the watcher, and wait for the goroutine with a `sync.WaitGroup`. Safe to call twice.
   - `HandleEvent`: if `ShouldIgnore(path, isDir)`, return. Otherwise set `lastChange = t.clk.Now()` under a mutex.
   - `SecondsSinceLastChange`: under a read lock, return `NeverChanged` when `lastChange.IsZero()`, otherwise `now.Sub(lastChange)`.
   - The tracker reads no clock of its own; `time.Now()` must not appear in this package.

4. Create `internal/changetracker/changetracker_test.go`:
   - `TestShouldIgnore`: table cases for a directory (`true`), `repo/.git/HEAD` (`true`), `repo/.git` (`true`), `repo/note.md~` (`true`), `repo/note.swp` (`true`), `repo/note.tmp` (`true`), `repo/.DS_Store` (`true`), `repo/note.md` (`false`), `repo/sub/note.md` (`false`).
   - `TestHandleEventResetsOnlyForOrdinaryFiles`: use `clocktest.NewFake(t0)` and `New(dir, fake)`. Actually write each file to `t.TempDir()` before passing its path to `HandleEvent` so the test exercises real paths. Assert:
     - an ordinary file write at `t0` makes `SecondsSinceLastChange(t0) == 0`;
     - after advancing the fake clock to `t0+5s`, writes to a `.tmp` file, a `~` backup, a `.swp` file, a dot-prefixed file, a path under `.git/`, and a directory each leave `SecondsSinceLastChange(t0+5s)` at `5s` (unchanged);
     - a second ordinary file write at `t0+10s` resets `SecondsSinceLastChange(t0+10s)` to `0`.
   - `TestSecondsSinceLastChangeNeverChanged`: `New(dir, fake)` with no events returns `NeverChanged`.
   - `TestStartStopDeliversOrdinaryEvents`: create a temp dir, `New(dir, clock.New())`, `Start`, write an ordinary file, poll `SecondsSinceLastChange(time.Now())` every 10 ms for up to 2 s until it is below one second, then write a `.tmp` file, wait 100 ms, and assert the value did not reset (it is greater than the value recorded before the `.tmp` write). `Stop`. This proves the fsnotify wiring; the deterministic `HandleEvent` test is the primary assertion.
</requirements>

<constraints>
- Do NOT modify `internal/config`, `internal/logging`, `internal/clock` (the interface and fake are from prompt 1), `internal/version`, `internal/cli`, `cmd/`, `internal/git`, `internal/sync`, `Makefile`, `CLAUDE.md`, or any Python file. This prompt adds `internal/quietwindow` and `internal/changetracker` and their tests.
- Do NOT add a configurable decay factor, an opt-out flag, a tunable threshold, or a metric. The decay is fixed at one half per skip.
- The quiet-window module must not read a clock and must not log. The change tracker must not read a clock of its own; it uses the injected `clock.Clock`.
- Do NOT include the word `python` (any case) in any Go source file under `cmd/` or `internal/`.
- Do NOT poll the filesystem on a timer; use the filesystem notification library.
- Use `github.com/bborbe/errors` for wrapping; never `fmt.Errorf`.
- `gofmt`, `go vet ./...` and `go test ./... -count=1` must pass. Run `make precommit-go`, not `make precommit`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
go test ./internal/quietwindow/... ./internal/changetracker/... -count=1
go test ./... -count=1
make test-go
make precommit-go
grep -n 'DecayedWindow' internal/quietwindow/quietwindow.go
grep -n '0.5' internal/quietwindow/quietwindow.go
grep -n '>= maxWait' internal/quietwindow/quietwindow.go
grep -n 'secondsSinceChange >= window' internal/quietwindow/quietwindow.go
grep -n '\.tmp\|\.swp\|\.git' internal/changetracker/changetracker.go
grep -rn 'time.Now()' --include='*.go' internal/quietwindow/ internal/changetracker/ | grep -v '_test.go' | wc -l
grep -rn 'python' --include='*.go' cmd/ internal/ | wc -l
go test ./internal/quietwindow/ -run 'TestDecayedWindow|TestCap|TestWindow|TestDecay|TestFailed' -count=1
```

Expected: the build, vet, tests and both Make targets exit 0; the decay expression and both `>=` comparisons appear; the ignore set appears; the non-test `time.Now()` count in the two packages is `0`; the Go-source `python` grep prints `0`; the named quiet-window tests pass.

The first `go test`/`make precommit-go` may fetch Go modules over the network; that is expected.

Before finishing, walk Acceptance Criteria 2, 3, 4, 5, 6 and 13 against the tests: the four pinned decay points plus the k = 0..10 halving property; the cap at exactly 300 s and not at 299 s; the reset after two skips and a success; the unchanged window and growing elapsed after a failure; the exact-boundary acceptance; and the five ignore cases plus the ordinary-file reset.
</verification>
