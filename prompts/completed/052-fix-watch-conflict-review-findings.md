---
status: completed
summary: Added a conflict-state guard to sync.Run, made conflict resolution idempotent across attempts in sync.ResolveConflicts and the resolve subcommand, bounded retries after an alert in the watch loop, and added regression tests
execution_id: git-ai-sync-watch-conflict-exec-052-fix-watch-conflict-review-findings
dark-factory-version: v0.198.17
created: "2026-11-13T13:05:00Z"
queued: "2026-11-13T11:51:29Z"
started: "2026-11-13T11:52:30Z"
completed: "2026-11-13T11:55:44Z"
---

# Keep the conflicted repo out of the sync cycle and make resolution retries idempotent

<summary>
- A repo that is still mid-merge or mid-rebase is never staged or committed by the sync cycle, so conflict markers can no longer be committed.
- The one-shot `sync` command reports the conflict instead of committing a half-merged repo.
- A resolution attempt that already staged every file still completes on the next attempt instead of failing with "no conflicted files".
- Once a conflict has alerted, each later cycle makes one resolution attempt instead of three, so a persistent conflict stops burning model time.
- Each behaviour has a regression test.
</summary>

<objective>
Make the automatic conflict path safe to retry: the sync cycle must refuse to touch a repo that is still in a conflict state, the resolver must be idempotent across attempts, and the retry cost must be bounded once an alert has fired. **Why:** after the first change wired resolution into the watch loop, a conflicted repo that exhausted its attempts was handed back to `sync.Run`, which stages every file and can commit conflict markers; and a resolver that had staged all files but failed to continue could never succeed on a retry.
</objective>

<context>
Read `CLAUDE.md` for project conventions.
- `internal/sync/sync.go` — `Run` calls `git.HasChanges`, then `runDirty` (which does `git.StageAll` → `git.Commit` → `git.Pull` → `git.Push`).
- `internal/sync/conflict.go` — `ResolveConflicts` (added by the previous prompt): lists `git.ConflictedFiles`, errors when the list is empty, calls `conflict.ResolveMarkerFlaggedFiles`, continues the merge or rebase, pushes.
- `internal/git/git.go` — `IsInMerge(repo)`, `IsInRebase(repo)`, `ConflictedFiles(ctx, repo)`, `ErrConflict`.
- `internal/watch/watch.go` — the `git.ErrConflict` branch retries `MaxConflictAttempts` times; `alerted` is a loop-local bool set after `ConflictAlert` fires and reset on a successful resolution.
- `go-error-wrapping-guide.md` (coding plugin) — `errors.Wrapf` conventions used in requirement 1.
- Test fixtures: `internal/sync/sync_test.go` (`newFixture`, `configure`, `runGit`, `writeFile`), `internal/sync/conflict_test.go`, `internal/watch/watch_test.go` (`newHarness`, `recordingRunner`, `fakeSleep`).
</context>

<requirements>
1. In `internal/sync/sync.go` `Run`, before `git.HasChanges`, add a conflict-state guard: when `git.IsInMerge(repo) || git.IsInRebase(repo)`, return `errors.Wrapf(ctx, git.ErrConflict, "repository is still in a conflict state")` without staging or committing anything. Guard on the state alone, not on the conflicted-file list — a repo whose conflicts are all resolved and staged is still mid-merge, and `runDirty` would otherwise conclude it with the `auto:` commit message and never reach the resolver's continue step. Import `github.com/bborbe/errors`. This protects every caller, including the one-shot `sync` subcommand, and is what routes a partially resolved merge back to `ResolveConflicts` (requirement 2).

2. In `internal/sync/conflict.go` `ResolveConflicts`, make the empty-list case idempotent: error only when the list is empty AND the repo is not in a merge or rebase (`!git.IsInMerge(repo) && !git.IsInRebase(repo)`). When the list is empty but a merge or rebase is in progress, skip `ResolveMarkerFlaggedFiles` and proceed to the continue-and-push steps, so a retry after a partially completed attempt can finish. The `resolve` subcommand in `internal/cli/cli.go` (`newResolveCommand`, its `if len(files) == 0` block) has an inline copy of this sequence; apply the same rule there — when the list is empty but `inMerge || inRebase` is true, skip the per-file loop and proceed to `ContinueRebase`/`ContinueMerge` and `git.Push` — so `git-ai-sync resolve` and the watch loop behave identically.

3. In `internal/watch/watch.go`, bound the retry cost once an alert has fired: compute the per-cycle attempt cap as `MaxConflictAttempts`, but use `1` when `alerted` is true. A successful resolution still resets `alerted` to false, restoring the full cap. Update the per-attempt log line's `%d/%d` and the `attempt == MaxConflictAttempts` comparison to use the computed cap, so the log and the retry-delay guard follow it.

4. Tests:
   - `internal/sync/sync_test.go`: a test that seeds a real conflicted merge with the existing `seedConflict` helper, calls `Run`, and asserts `stderrors.Is(err, git.ErrConflict)` — the watch loop dispatches on that identity, not on equality — and that `.git/MERGE_HEAD` still exists and `git status --porcelain` still lists the conflicted path (nothing was staged or committed).
   - `internal/sync/conflict_test.go`: a test that a fully staged mid-merge repo (conflicted files resolved and `git add`ed, but the merge not concluded) is completed by `ResolveConflicts` — no `MERGE_HEAD` afterwards and the branch is pushed.
   - `internal/watch/watch_test.go`: extend the exhausted-conflict test to run a second conflict cycle after the alert and assert the resolver is invoked exactly `MaxConflictAttempts + 1` times (three before the alert, one after). The alert callback can no longer cancel the loop — the second cycle posts no alert — so terminate from the runner handler on its third invocation (or from the resolver after its fourth call), and update the recorded-duration assertions from 1 interval / 2 delays to 2 intervals / 2 delays.

5. Before finishing, re-run `<verification>` and confirm it passes; walk each requirement above against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not change the nil-`ConflictResolver` behaviour.
- Do NOT touch `.github/` or anything under `.github/workflows/`.
- Tests must not invoke the real `claude` binary.
- Do not edit `CHANGELOG.md` — the `## Unreleased` bullet for this feature is already present.
</constraints>

<verification>
Run from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/watch ./internal/conflict ./internal/sync ./internal/supervise -count=1
make precommit
```

Expected: `gofmt -l cmd internal` prints nothing; the targeted `go test` exits 0; `make precommit` exits 0.
</verification>
