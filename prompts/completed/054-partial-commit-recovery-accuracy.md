---
status: completed
summary: unmergedError now names the recovery matching the actual unmerged state, blocked-path messages are capped at 10 paths, the HasAutostash comment matches git's rendering, and tests cover every branch
execution_id: git-ai-sync-partial-commit-exec-054-partial-commit-recovery-accuracy
dark-factory-version: v0.198.17
created: "2026-11-13T13:32:11Z"
queued: "2026-11-13T13:32:11Z"
started: "2026-11-13T13:33:27Z"
completed: "2026-11-13T13:37:19Z"
---

# Give each unmerged state its own recovery instruction

<summary>
- A manual stash-pop conflict no longer points at a command that refuses outside a merge or rebase.
- A merge or rebase conflict still points at `git-ai-sync resolve`; the manual stash-pop conflict now says that command does not apply.
- The blocked-paths field describes what it actually holds when the attempt budget runs out or the cycle is cancelled.
- The autostash matcher's comment matches what git prints.
- Tests cover each recovery branch and the capped message.
</summary>

<objective>
Make the unmerged-state error name the recovery that actually applies to the state in front of the operator, and make the blocked-paths field honest about units it could not attribute.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `docs/dod.md` — the repo's Definition of Done; it requires tests for new functions.
Read `internal/sync/sync.go` — `Run`'s merge/rebase guard (it returns before `runDirty` when `MERGE_HEAD` or a rebase directory exists), `runDirty`, `unmergedError`, `commitDirty`, `commitBisect`, `commitUnits`, `Result.Blocked` and its doc comment.
Read `internal/git/git.go` — `HasAutostash`, `IsInMerge`, `IsInRebase`, `ConflictedFiles`, `HasUnmergedPaths`, `ErrConflict`.
Read `internal/cli/cli.go` `resolve` — it refuses only when the repo has neither `MERGE_HEAD` nor a rebase directory; with a merge state it continues the merge and pushes, and it stages the conflicted files itself.
Read `internal/sync/sync_test.go` and `internal/git/git_test.go` — the fixtures and the partial-commit tests, including `TestPreexistingUnmergedPathStopsCycle`.
Read `go-error-wrapping-guide.md` and `go-testing-guide.md` in the coding plugin docs — the project's `errors.Wrapf` and standard-`testing` conventions.
</context>

<requirements>
1. In `unmergedError`, choose the recovery from the state. Branch in this order:
   - `git.IsInMerge(repo)` or `git.IsInRebase(repo)` → this is a merge or rebase conflict; the recovery is to run `git-ai-sync resolve`, which resolves and stages the conflicted files, continues the merge or rebase and pushes — do not tell the operator to stage them first, because `resolve` does the staging itself (it refuses only when the repo has neither `MERGE_HEAD` nor a rebase directory, not when the conflicted files are already staged). Note in a comment that `Run` returns before `unmergedError` whenever a `MERGE_HEAD` or rebase directory exists, so this branch is reachable only from a direct call and is kept for that test, not as a production path;
   - otherwise, `git.HasAutostash(repo)` → the autostash is retained; resolve the conflict and run `git stash drop`;
   - otherwise (a manual `git stash pop` conflict: unmerged paths, no merge or rebase state) → say the recovery is to resolve the conflict and drop the stash entry that produced it, and state explicitly that `git-ai-sync resolve` does not apply here because it refuses outside a merge or rebase state. Reproduced: a manual `git stash pop` conflict leaves unmerged paths, a retained `WIP on master: …` stash and no `MERGE_HEAD`, and `resolve` refuses it.
   Name the unmerged paths in every branch. If a probe fails, keep the `ErrConflict` wrap and use wording that names neither recovery — tell the operator to inspect `git stash list` — and name the probe error.
2. Make `Result.Blocked` honest about the budget. On budget exhaustion `commitBisect` reports every pending unit as blocked, so a large mixed set over-reports paths the hook never refused. Reword the `Blocked` doc comment to say it holds the paths that could not be committed in this cycle — refused by the hook, or left untried because the attempt budget was exhausted or the cycle was cancelled — and cap the message built in `commitDirty`: list at most 10 paths and append `and N more` when there are more. The field keeps every path so the next cycle still sees them staged; only the message is capped.
3. Fix the `HasAutostash` comment in `internal/git/git.go`: git renders the entry as `stash@{0}: autostash`, not `stash@{0}: On master: autostash`. Keep matching on the subject, and note that an operator stash created with `-m autostash` matches too — both are autostash-shaped, and dropping is the right recovery for either.
4. Tests:
   - extend `TestPreexistingUnmergedPathStopsCycle` rather than adding a duplicate — it already builds a `MERGE_HEAD` state, calls `unmergedError` directly, and asserts `ErrConflict`, the path name, `git-ai-sync resolve` and no `stash drop`; keep those assertions and add that the message does not instruct the operator to stage the files first;
   - a retained autostash with no merge state: the message contains `stash drop`;
   - a manual stash-pop conflict (unmerged paths, a `WIP on master: …` stash, no `MERGE_HEAD`): the message says `git-ai-sync resolve` does not apply (it refuses outside a merge or rebase state), does not present `git-ai-sync resolve` as the recovery, and names the retained stash entry (assert `stash@{0}` or the `WIP on` subject appears);
   - a budget-exhaustion case: with a mixed set large enough to exhaust the budget, assert the message lists at most 10 paths plus `and N more`, while `Result.Blocked` still holds every path.
5. Add one bullet to `CHANGELOG.md` under the existing `## Unreleased` section — never add a second one:
   - fix: name the recovery that matches the actual unmerged state, and describe blocked paths honestly when the attempt budget runs out
6. Self-check before finishing: re-run `<verification>`, then walk requirements 1–5 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Go standard `testing` package only; follow the existing test file's style
- Scratch repos only under `t.TempDir()`; no absolute or home-relative paths
- Never invoke the host's global pre-commit hook (`core.hooksPath`) — the test hook is a fixture written into the scratch clone
- Do not change the pre-commit guard's rule, the alert mechanism, or the stage → commit → pull → push order
- Do not change the bisection algorithm or `maxCommitAttempts` — only `unmergedError`, the `Blocked` doc and message, the `HasAutostash` comment, and tests are in scope
- Leave `internal/conflict` `ContinueMerge` / `ContinueRebase` unchanged
</constraints>

<verification>
Run `make precommit` — must pass. It runs `gofmt -l cmd internal`, `go vet ./...`, and `go test ./... -count=1`.
</verification>
