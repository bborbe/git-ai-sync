---
status: completed
summary: Wired conflict resolution into the watch loop with bounded retries, an attention alert, and regression tests; make precommit exits 0
execution_id: git-ai-sync-watch-conflict-exec-051-fix-watch-conflict-resolution
dark-factory-version: v0.198.17
created: "2026-11-13T12:15:00Z"
queued: "2026-11-13T11:35:21Z"
started: "2026-11-13T11:36:25Z"
completed: "2026-11-13T11:39:48Z"
---

# Resolve merge conflicts inside the watch loop

<summary>
- A vault that hits a merge conflict during a pull is resolved, concluded and pushed by the daemon instead of stopping.
- The conflict resolver the tool already ships is used automatically by both the supervisor and the `watch` subcommand; no operator command is needed.
- A failed resolution is bounded at three attempts one minute apart per cycle, then one attention alert.
- A resolution that leaves conflict markers in a file is refused before it is staged, so the pre-commit hook cannot wedge the loop.
- A regression test drives a real conflicted merge through the watch path and asserts the vault ends clean and pushed.
</summary>

<objective>
The watch loop must attempt conflict resolution on `git.ErrConflict` instead of returning, and a resolution that cannot succeed must fail loudly through the existing attention channel. **Why:** on 2026-11-13 one conflicted vault stopped syncing for about 30 minutes while every other vault stayed healthy, and the only signal was a counter in `status.json` — the resolver existed but was reachable only through the manual `resolve` subcommand.
</objective>

<context>
Read `CLAUDE.md` for project conventions.
- `internal/watch/watch.go` — `Run` builds the loop; `loop` is the poll loop. The `git.ErrConflict` branch currently logs `[%d] conflict:` and `return err`. Reuse its existing `windowTracker`, `clk`, `sleep` and `logger`.
- `internal/conflict/conflict.go` — `ResolveFile` (one file), `ResolveMarkerFlaggedFiles` (batch, stops at the first failure), `parseConflictMarkers`, `stripCodeFence`.
- `internal/git/git.go` — `ConflictedFiles`, `IsInMerge`, `IsInRebase`, `ContinueMerge`, `ContinueRebase`, `Push`, `ErrConflict`.
- `internal/cli/cli.go` `newResolveCommand` — the manual path this mirrors: list conflicted files, resolve each, continue the merge or rebase, push. Note it collects per-file failures and continues; `ResolveMarkerFlaggedFiles` stops at the first failure — mirror the sequence, not that loop.
- `internal/cli/cli.go` `newWatchCommand` — the second entry point that builds `watch.Options`; it must gain the resolver too, or `git-ai-sync watch` keeps the bug.
- `internal/supervise/supervise.go` — `runVault` builds `watch.Options`; `Options.WatchRun` is injectable for tests; `runnerFor` records failures and `notifier.post` posts the stall card. `notifier.enabled()` gates posting; the card contract is in `docs/attention-store.md`.
- `internal/sync/sync.go` — one sync cycle.
- Test helpers to copy, not reinvent (each is unexported, so none can be imported across packages): `internal/watch/watch_test.go` `fakeSleep` (advances a `clocktest.Fake` and cancels after `cancelAfter` calls; it records no durations, so wrap it in a recorder that appends each `d`) and `fakeTracker`; `internal/clock/clocktest`; `internal/conflict/conflict_test.go` `writeStub` (writes an executable `claude` into a temp dir and prepends it to `PATH` via `t.Setenv`) and `writeResponse`; `internal/sync/sync_test.go` `newFixture`, `configure`, `runGit`, `writeFile`.
- The operator's global pre-commit hook (installed via `core.hooksPath` on the host, not present in this container) refuses a staged blob containing a `<<<<<<< ` or `>>>>>>> ` line at column 0, except inside a fenced code block. Its fence rule is exactly: a line matching `^[[:space:]]*(```|~~~)` toggles the fence. That exemption exists because vault pages legitimately quote marker examples. Requirement 1 must reproduce this rule; requirement 6's table test asserts the agreement.
</context>

<requirements>
1. In `internal/conflict/conflict.go`, add `func HasConflictMarkers(content []byte) bool`. It returns true when a line beginning `<<<<<<< ` or `>>>>>>> ` appears outside a fenced code block. Track fences exactly as the pre-commit hook does: toggle on a line matching `^[[:space:]]*(```|~~~)` — optional leading whitespace then three backticks or three tildes; both fence characters toggle the same boolean, and `=======` is not a marker for this check. In `ResolveFile`, inside the `len(parseConflictMarkers(...)) == 0` branch — not before it — check `HasConflictMarkers(original)`; when it is true, return a wrapped error naming the path instead of calling `git.StageFile`. A file with a parseable conflict block must still reach `resolveContent`, so the check must never short-circuit a genuine conflict. Also switch the existing post-resolution check in `resolveContent` from `parseConflictMarkers(resolved)` to `HasConflictMarkers([]byte(resolved))`, so a file that legitimately quotes a marker inside a fence is not rejected forever.

2. In a new file `internal/sync/conflict.go`, add `func ResolveConflicts(ctx context.Context, repo string) error` mirroring `newResolveCommand`'s sequence, stopping at the first failure: list `git.ConflictedFiles`, return an error when the list is empty, call `conflict.ResolveMarkerFlaggedFiles`, then `git.ContinueRebase` when `git.IsInRebase(repo)` else `git.ContinueMerge`, then `git.Push`. Wrap every error with `errors.Wrapf(ctx, ...)`. Do not acquire the lock — the watch loop already holds it.

3. In `internal/watch/watch.go`, add to `Options`:
   - `ConflictResolver func(ctx context.Context, repo string) error` — nil keeps today's behaviour.
   - `ConflictAlert func(ctx context.Context, repo string, cause error)` — called once per unresolved conflict; nil disables the alert.
   - `ConflictAttemptTimeout time.Duration` — zero defaults to `defaultConflictAttemptTimeout`, applied in `Run` alongside the `sleep` default.
   Update `Options`' doc comment to mention the three new fields.
   Add `const MaxConflictAttempts = 3` (exported, because `supervise` reuses it), `const conflictRetryDelay = time.Minute`, and `const defaultConflictAttemptTimeout = 2 * time.Minute`. Thread the resolver, the alert and the attempt timeout into `loop` as parameters, mirroring `sleep`, and update the single existing `loop(...)` call in `internal/watch/watch_test.go` (the test harness) to pass them.
   In the `git.ErrConflict` branch: when `ConflictResolver` is nil, keep the current log and `return err`. Otherwise declare a loop-local `alerted` bool next to `windowTracker` and attempt up to `MaxConflictAttempts` calls. At the top of every retry iteration add the non-blocking `select { case <-ctx.Done(): return nil; default: }` check (project rule `go-context/cancel-check-in-loop`), then wrap each call in `context.WithTimeout(ctx, attemptTimeout)` and cancel immediately after it, so a hung resolver consumes one attempt instead of blocking the loop forever. On success log `[%d] conflict resolved`, call `windowTracker.RecordSuccess(clk.Now())`, reset `alerted` to false, and continue the outer loop. On failure log the attempt number; when attempts remain, sleep `conflictRetryDelay` through `sleep` (which already respects `ctx` cancellation) and on a non-nil sleep error return nil when `ctx.Err() != nil`, else `errors.Wrapf(ctx, err, "sleep before conflict retry")`. After the final failure read `git.ConflictedFiles` best-effort and log the returned paths (the list cannot reach `ConflictAlert`, whose signature carries only `cause`), then call `ConflictAlert(ctx, repo, lastErr)` — `lastErr` is the final resolver error — when it is non-nil and `alerted` is false, then set `alerted` true and `continue` the outer loop — a later cycle re-attempts. Add a comment stating that `alerted` dedups only the loop's own card, while the supervisor's stall path dedups separately on `deps.tracker.outstanding` and `runnerFor` still counts each `ErrConflict` cycle as a stall failure.

4. In `internal/supervise/supervise.go` `runVault`, set `ConflictResolver: sync.ResolveConflicts` and a `ConflictAlert` closure that, guarded by `deps.notifier.enabled()`, posts one card through `deps.notifier.post(ctx, Label(vault), vault, 1, opts.Clock.Now(), fmt.Sprintf("conflict resolution failed after %d attempts: %v", watch.MaxConflictAttempts, cause))` — `post` renders its payload as `<label>: sync has failed <count> consecutive cycles`, so pass `1` (the cycle count) and put the attempt bound in the last-error text, which also names the conflicted files — then records the returned item id with `deps.tracker.setOutstanding(vault, itemID)` when posting succeeds. Add a comment that the card reuses the stall dedup key `git-ai-sync-<label>-stalled`, so one card exists per vault regardless of which path posted it.

5. In `internal/cli/cli.go` `newWatchCommand`, set `ConflictResolver: sync.ResolveConflicts` on the `watch.Options` literal, and set `ConflictAlert: nil` with a comment that the CLI has no attention notifier. The `watch` subcommand must get the same resolution behaviour as the supervisor.

6. Tests:
   - `internal/conflict/conflict_test.go`: a table test asserting `HasConflictMarkers` agrees with the real pre-commit hook for the same input. The hook body is not readable from the container — copy its two rules verbatim into the test as the oracle (`^[[:space:]]*(```|~~~)` toggles the fence; `!fence && /^(<<<<<<< |>>>>>>> )/` sets found). Cases: a bare `<<<<<<< ` line; a `<<<<<<< ` inside a ``` fence; a `<<<<<<< ` inside a ~~~ fence; a `>>>>>>> ` at column 0; a `<<<<<<< ` preceded by spaces (must NOT match); a line containing `<<<<<<< ` mid-line (must NOT match). Assert `HasConflictMarkers` equals the oracle for every case.
   - `internal/conflict/conflict_test.go`: a test that `ResolveFile` refuses to stage a file whose content has a marker the block regex does not parse (e.g. `<<<<<<< HEAD` followed by `from-ours` with no `=======` and no `>>>>>>> `) outside a fence, leaves it byte-identical, and never invokes the stub model — do not reuse `markerContent`, whose full block takes the resolution path instead.
   - `internal/conflict/conflict_test.go`: a test that a resolved answer which quotes a marker inside a fenced block is accepted and staged (the behaviour the `resolveContent` switch exists to allow), while the same marker outside a fence is still refused.
   - `internal/sync/conflict_test.go`: a test that `ResolveConflicts` resolves a seeded conflicted merge using a stubbed `claude` on `PATH` (write the stub the way `writeStub` does), then asserts no `.git/MERGE_HEAD`, empty `git status --porcelain`, and `HEAD` equal to the remote ref.
   - `internal/sync/conflict_test.go`: a test that `ResolveConflicts` returns an error on a clean fixture with no conflicted files, and a test that seeds a rebase conflict (diverge, then `git rebase` against the bare remote) and resolves it through `git.ContinueRebase`.
   - `internal/watch/watch_test.go`: the watch-path regression test. Seed a real conflicted merge between a temp repo and a bare remote, drive `loop` with a `ConflictResolver` that calls `sync.ResolveConflicts` against a stubbed `claude` on `PATH`, and a `Runner` (the harness's `recordingRunner`) that returns `errors.Wrapf(ctx, git.ErrConflict, "seeded conflict")` on its first call — do not use `sync.Run` as the runner, because it stages and commits the unmerged files before the pull and the resolver then finds no conflicted files. Assert the repo ends with no `MERGE_HEAD`, empty `git status --porcelain`, and `HEAD` equal to the remote ref. Terminate the loop explicitly — cancel from the resolver's success path — because the loop continues after a resolved conflict. The test must never reach the real `claude` binary. Set `ConflictAttemptTimeout: defaultConflictAttemptTimeout` (or any non-zero duration) on the loop invocation — a zero value makes `context.WithTimeout(ctx, 0)` expire before the resolver starts.
   - `internal/watch/watch_test.go`: a test where the resolver always fails. Wrap `fakeSleep` in a recorder that appends each duration, set `cancelAfter` high enough (e.g. 10) that the sleeper's own cancel cannot pre-empt the alert, and cancel from inside `ConflictAlert` so the loop exits after the alert. Assert: the resolver is invoked exactly 3 times, the recorded durations for that cycle are `interval` once and `conflictRetryDelay` twice (filter by value — `opts.Sleep` also serves the loop's interval sleep), and `ConflictAlert` is called exactly once.
   - `internal/watch/watch_test.go`: a test that a successful resolution resets `alerted`, so a later conflict posts a second card.
   - `internal/watch/watch_test.go`: a test that a resolver which blocks until its context is done is bounded by `ConflictAttemptTimeout` — set the field to a short duration, have the resolver block on `<-ctx.Done()` and return `ctx.Err()`, cancel from inside `ConflictAlert`, and assert the resolver was invoked 3 times and each invocation observed `context.DeadlineExceeded`.
   - `internal/supervise/supervise_test.go`: a wiring test that calls `Run` with `WatchRun` capturing the `watch.Options` it receives and cancelling; assert the captured `ConflictResolver` and `ConflictAlert` are both non-nil, so the production supervisor path cannot silently lose the resolver again.

7. Update `CHANGELOG.md`: insert a `## Unreleased` section between the frozen preamble (ending `* PATCH version when you make backwards-compatible bug fixes.`) and `## v0.19.3` — there is currently no `## Unreleased`, create it — with one flat bullet:
   ```
   - fix: resolve a merge conflict inside the watch loop instead of stopping, so one conflicted vault no longer halts syncing while every other vault stays healthy
   ```
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not change what the resolver does when it succeeds; only wire it into the loop.
- Do NOT touch `.github/` or anything under `.github/workflows/`.
- Keep the nil-`ConflictResolver` behaviour byte-identical to today so existing callers and tests are unaffected.
- The one-shot `sync` subcommand deliberately keeps returning `ErrConflict` to its caller — it is user-visible and out of scope here.
- Tests must not invoke the real `claude` binary, make network calls, or depend on authentication.
</constraints>

<verification>
Run from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/watch ./internal/conflict ./internal/sync ./internal/supervise -count=1
make precommit
grep -q '^## Unreleased' CHANGELOG.md
```

Expected: `gofmt -l cmd internal` prints nothing; the targeted `go test` command exits 0; `make precommit` exits 0 (it runs gofmt + go vet + the full test suite); the `grep` exits 0, proving the CHANGELOG section exists.

Before finishing, re-run `<verification>` and confirm each command's expected result, and walk each requirement against the change.
</verification>
