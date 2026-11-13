---
status: completed
summary: Replaced the three-probe commit heuristic with full-unit bisection under a 256-attempt budget, added HasAutostash and split unmergedError recovery advice, with tests and changelog
execution_id: git-ai-sync-partial-commit-exec-053-partial-commit-no-probe-shortcut
dark-factory-version: v0.198.17
created: "2026-11-13T12:57:04Z"
queued: "2026-11-13T12:59:45Z"
started: "2026-11-13T13:06:30Z"
completed: "2026-11-13T13:11:22Z"
---

# Never let a probe heuristic skip a committable file

<summary>
- A set whose refused units happen to sit at the sampled positions still commits every file that passes.
- A commit that fails for every path costs a bounded number of git invocations instead of one per file.
- A commit that fails for every path still reports the original commit error rather than blaming individual files.
- The unmerged-path error describes both causes and gives the right recovery for each.
- Tests cover the mixed set, the all-refused case, a genuine merge conflict, and the retained-autostash branch.
</summary>

<objective>
Every sync cycle tries every staged unit, so a refused file can no longer hide a committable one behind it, and a commit that fails for every path stops at a fixed attempt budget instead of costing one subprocess per file. The three-probe shortcut cannot be made sound: no constant number of probes distinguishes a global failure from a set whose blocked units sit at the probed positions.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `docs/dod.md` — the repo's Definition of Done; it requires tests for new functions.
Read `internal/sync/sync.go` — `commitDirty`, `commitUnits`, `commitBisect`, `commitHalf`, `flattenUnits`, `unmergedError`, `Result.Blocked`.
Read `internal/sync/sync_test.go` — `newFixture`, `configure`, `runGit`, `writeFile`, `addRemoteCommit`, `isAncestor`, `installRefusalHook`, and the partial-commit tests added by the two previous changes.
Read `internal/git/git.go` — `CommitPaths`, `StagedFiles`, `ConflictedFiles`, `HasUnmergedPaths`, `ErrConflict`, and `runGit` (the only place the package shells out; `exec.CommandContext`, so `ctx` cancels).
Read `internal/cli/cli.go` `resolve` — it calls `ConflictedFiles`, refuses when there are none, otherwise resolves and stages the conflicted files, then continues the merge and pushes.
</context>

<requirements>
1. Remove the three-probe heuristic from `commitUnits` and bisect the full unit set instead. No constant number of probes can distinguish a global commit failure from a set whose refused units happen to be the probed ones: reproduced with 6 staged units refused at indices 0, 3 and 5 — every probe fails, the current code returns `(0, nil)`, and the cycle commits nothing even though three other units pass. That is the stall this work exists to remove, so the shortcut is unsound by construction and must not be patched with more probes.
2. Bound the cost with an attempt budget instead: carry a counter through `commitBisect` and `commitHalf`, capped by a constant `maxCommitAttempts` (256). When the budget is exhausted, stop recursing and return every unit not yet **committed** as blocked — not "not yet tried", so a staged path is never dropped from `Blocked`. A bisection of a realistic vault (tens of units, a few refused) stays far under the cap; a commit that fails for every path stops at the cap rather than costing roughly 3n subprocesses on every interval. Keep the existing `ctx.Err()` checks.
3. Keep `commitDirty`'s contract unchanged: when nothing commits, it returns the original commit error with `Blocked` empty.
4. Add `HasAutostash(ctx context.Context, repo string) (bool, error)` to `internal/git/git.go`, built on the existing `runGit` (`git stash list`), returning true only when an entry's subject is `autostash` — not merely any stash, so the operator's own stashes do not misroute the advice. Wrap failures with `errors.Wrapf`. Then fix `unmergedError`: name the unmerged paths and branch on `HasAutostash`. When it is true, say the autostash is retained and the recovery is to resolve the conflict and run `git stash drop`. When it is false, say this is a merge or rebase conflict and the recovery is to run `git-ai-sync resolve`, which resolves and stages the conflicted files, commits the merge and pushes — do not tell the operator to stage them first, because `resolve` refuses a state with no conflicted files. If `HasAutostash` itself fails, keep the `ErrConflict` wrap and use the merge-or-rebase wording, naming the probe error.
5. Tests in `internal/sync/sync_test.go` and `internal/git/git_test.go`:
   - a mixed set at the probe positions: stage 6 units and refuse the units at indices 0, 3 and 5 (index 3 is `len(units)/2`; refusing index 2 instead would let the old probes pass). Assert each of the three accepted paths appears in `git log --name-only` (any commit — bisection lands the accepted units in more than one commit, so `HEAD` alone holds only the last), and that the three refused paths are in `Result.Blocked` and still listed by `git diff --cached --name-only`. Extend `installRefusalHook` to accept a variadic list of refused paths (one grep pattern per path), keeping the single-path behaviour its other callers use;
   - the all-refused case: replace `TestGlobalCommitFailureDoesNotBisect` — its `> 6` counter bound encodes the removed probes and fails once the full set is bisected. Stage at least 100 units (200 is a safe choice), assert the original error is returned, `Blocked` is empty, every unit is still staged (`git diff --cached --name-only` lists all of them), and the hook's invocation counter is at most `maxCommitAttempts` plus 2. An unbudgeted bisection of an all-refused set costs 3n-2 (598 at n=200), so only a set that large reaches the cap and exercises the budget-exhaustion branch. Assert on the counter, never on a timing;
   - a genuine `MERGE_HEAD` unmerged state with no autostash: assert the returned error wraps `ErrConflict` and its message does not contain `stash drop`;
   - keep the existing autostash pop-conflict test, then run `Run` a second time on the same clone: the retained autostash is still in `git stash list` (subject `autostash`) and the worktree still has `UU` entries, so `runDirty`'s guard reaches the new `unmergedError` autostash branch — assert the error wraps `ErrConflict`, names the unmerged path, and mentions `stash drop`. The first cycle alone cannot cover it: that error comes from `autostashConflict` in `internal/git`, not from `unmergedError`;
   - a direct test for `HasAutostash`: false with no stash; true after an `autostash` entry is stored the way `git pull --autostash` stores it (`git stash create`, then `git stash store -m autostash <sha>` — plain `git stash` produces `WIP on master: …` instead); and false for that plain `git stash` entry. Read the plain `git stash list` reflog message: `--format=%s` returns `WIP on master: …` even for the autostash entry, so it can never match.
6. Add one bullet to `CHANGELOG.md` under the existing `## Unreleased` section — never add a second one:
   - fix: bisect every staged unit under an attempt budget instead of sampling three, so a refused unit at a sampled position can no longer leave a committable file behind
   Also drop the now-false clause "and detect a global commit failure without bisecting every path" from the previous `## Unreleased` bullet — this change removes that behaviour, and both bullets ship in the same unreleased version.
7. Self-check before finishing: re-run `<verification>`, then walk requirements 1–6 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Go standard `testing` package only; follow the existing test file's style
- Scratch repos only under `t.TempDir()`; no absolute or home-relative paths
- Never invoke the host's global pre-commit hook (`core.hooksPath`) — the test hook is a fixture written into the scratch clone
- Do not change the pre-commit guard's rule, the alert mechanism, or the stage → commit → pull → push order
- Do not change `Pull`, `PullWithAutostash`, `StagedFiles`, `CommitPaths` or `HasUnmergedPaths` — only `commitUnits`/`commitBisect`/`commitHalf`, `unmergedError`, and their tests are in scope, plus the new `HasAutostash` helper in `internal/git/git.go` built on `runGit` (never a bare `exec` in `internal/sync`)
- Leave `internal/conflict` `ContinueMerge` / `ContinueRebase` unchanged
</constraints>

<verification>
Run `make precommit` — must pass. It runs `gofmt -l cmd internal`, `go vet ./...`, and `go test ./... -count=1`.
</verification>
