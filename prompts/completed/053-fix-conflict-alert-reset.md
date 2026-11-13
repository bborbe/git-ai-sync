---
status: completed
summary: Reset conflict-alert state on any healthy cycle, keyed the supervisor's ConflictAlert on the repository path, removed the dead resolve guard, and added regression tests
execution_id: git-ai-sync-watch-conflict-exec-053-fix-conflict-alert-reset
dark-factory-version: v0.198.17
created: "2026-11-13T13:20:00Z"
queued: "2026-11-13T12:17:05Z"
started: "2026-11-13T12:18:25Z"
completed: "2026-11-13T12:20:58Z"
---

# Reset the conflict-alert state on any healthy cycle

<summary>
- A successful sync cycle clears the conflict-alert state, so a conflict that heals without the resolver restores full retries and alerts.
- The attention card is keyed on the same repository path the supervisor's stall tracker uses.
- A dead condition in the `resolve` subcommand is removed.
- A healthy cycle after a conflict restores the full resolution retry budget.
- Regression tests cover the reset and the repository-keyed attention card.
</summary>

<objective>
Clear the loop's conflict-alert state whenever a sync cycle succeeds, not only when the resolver succeeds, so a conflict that clears by any other path does not leave the process permanently at one attempt per cycle with alerts suppressed. **Why:** a review of the conflict-resolution change found that `alerted` is reset only on resolver success, so a conflict resolved by a human or by a later healthy pull would keep the reduced cap and suppress every future card for the life of the daemon.
</objective>

<context>
Read `CLAUDE.md` for project conventions.
- `internal/watch/watch.go` — `loop`: `alerted` is declared next to `windowTracker`; it is set true after `ConflictAlert` fires and currently reset only in the resolver-success branch. The healthy-cycle path ends with `windowTracker.RecordSuccess(now)` after `result.CommitErr` is checked.
- `internal/supervise/supervise.go` — `runVault` builds `ConflictAlert` with `Label(vault)` and `deps.tracker.setOutstanding(vault, itemID)`, while `runnerFor` keys the stall tracker on `repo`. The `ConflictAlert` signature already carries `repo`.
- `internal/cli/cli.go` — `newResolveCommand`: the `if len(files) == 0 && !inMerge && !inRebase` guard sits after an earlier `if !inRebase && !inMerge { return ... }`, so it is unreachable.
- `internal/watch/watch_test.go` — `newHarness`, `runLoopWith`, `recordingRunner`.
</context>

<requirements>
1. In `internal/watch/watch.go` `loop`, reset `alerted = false` on every successful sync cycle — on the path that reaches `windowTracker.RecordSuccess(now)` with no error and no `CommitErr` — in addition to the existing reset on resolver success. Leave the alert-firing path unchanged.

2. In `internal/supervise/supervise.go` `runVault`, make the `ConflictAlert` closure use the `repo` argument it receives rather than the enclosing `vault` for both `Label(...)` and `deps.tracker.setOutstanding(...)`, so the card and the outstanding-item key match the `repo` key `runnerFor` uses. Keep the `vault` argument for the `watch.Options.Path` field and for the second positional argument of `deps.notifier.post` (display text only, not a key); every key the closure derives must come from `repo`. Update the comment above the closure (currently "one card exists per vault regardless of which path posted it") to say the card is keyed per repository, matching `runnerFor`.

3. In `internal/cli/cli.go` `newResolveCommand`, remove the now-unreachable `if len(files) == 0 && !inMerge && !inRebase` guard (the earlier `if !inRebase && !inMerge` already returns), and delete the now-unused `state` variable. Keep the `len(files) > 0` block that resolves the files.

4. Tests:
   - `internal/watch/watch_test.go`: a test that a conflict cycle alerts, a following healthy cycle returns a clean result, and a subsequent conflict cycle invokes the resolver `MaxConflictAttempts` times again and posts a second card — proving the healthy cycle cleared `alerted`. Name it distinctly from the existing `TestLoopSuccessResetsAlerted`, which covers the resolver-success reset.
   - `internal/supervise/supervise_test.go`: a test that the `ConflictAlert` closure keys on the `repo` it receives, not the configured vault. Reuse the helpers in `TestStallCardPostsOnceAndClosesOnRecovery` (`newSuperviseFixture`, `superviseConfig`, `startSupervise`, `storeRecorder`, `waitForInvocations`, `flagValue`): configure `Vaults` with a subdirectory of the fixture clone so `git.FindRepo` resolves a repo root whose last path component differs from the vault's, capture `watch.Options` with a `WatchRun` override, call `opts.ConflictAlert(ctx, repo, cause)` with the resolved repo, and assert the stub invocation's `--dedup-key` is `git-ai-sync-<Label(repo)>-stalled`.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not change the nil-`ConflictResolver` behaviour.
- Do NOT touch `.github/` or anything under `.github/workflows/`.
- Do not edit `CHANGELOG.md` — the `## Unreleased` bullet for this feature is already present.
- Update `docs/attention-store.md` line 9 to say the dedup key's label is derived from the repository path, not the configured vault path.
- Tests must not invoke the real `claude` binary.
</constraints>

<verification>
Run from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/watch ./internal/cli ./internal/supervise ./internal/sync ./internal/conflict -count=1
make precommit
```

Expected: `gofmt -l cmd internal` prints nothing; the targeted `go test` exits 0; `make precommit` exits 0.
</verification>
