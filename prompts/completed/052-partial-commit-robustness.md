---
status: completed
summary: Hardened the partial-commit path with -z staged-path parsing, an unmerged-path guard, autostash pop-conflict detection, and three-probe global-failure detection, with tests.
execution_id: git-ai-sync-partial-commit-exec-052-partial-commit-robustness
dark-factory-version: v0.198.17
created: "2026-11-13T12:28:42Z"
queued: "2026-11-13T12:30:06Z"
started: "2026-11-13T12:31:13Z"
completed: "2026-11-13T12:36:21Z"
---

# Make the partial-commit path safe for non-ASCII paths and autostash conflicts

<summary>
- Files whose names contain non-ASCII characters are committed instead of being reported as blocked forever.
- A refused path that also changed upstream stops the vault instead of leaving conflict markers the next cycle would commit.
- A pre-existing unmerged path stops the cycle before anything is staged.
- A commit that fails for every path is detected in a few attempts instead of hundreds.
- Tests cover each of those four cases.
</summary>

<objective>
Harden the partial-commit path so it does not fail on non-ASCII vault paths, does not commit conflict markers after an autostash pop conflict, and does not spend hundreds of git subprocesses on a global commit failure.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `docs/dod.md` — the repo's Definition of Done; it requires tests for new functions.
Read `internal/git/git.go` — `StagedFiles`, `CommitPaths`, `PullWithAutostash`, `Pull`, `ErrConflict`, `inConflictState`, `ConflictedFiles`, and the `runGit` wrap style (`go-error-wrapping-guide.md` in the coding plugin docs is the reference).
Read `internal/sync/sync.go` — `runDirty`, `commitDirty`, `commitUnits`, `commitHalf`, `flattenUnits`, `Result.Blocked`.
Read `internal/sync/sync_test.go` — `newFixture`, `configure`, `runGit`, `writeFile`, `addRemoteCommit`, `isAncestor`, and `installRefusalHook` (the hook fixture at the bottom of the file). `configure` here pins `core.hooksPath`; add `core.quotePath` for the non-ASCII test.
Read `internal/git/git_test.go` — its own `configure` pins `core.hooksPath`, and its git runner is `gitCmd`, not `runGit`.
Read `internal/watch/watch.go` around the runner call — an `ErrConflict` makes the loop return, while any other error is logged and retried on the next interval.
Read `internal/cli/cli.go` `resolve` — it refuses a state without `MERGE_HEAD` or a rebase directory, so an autostash pop conflict is not resolvable through the CLI.
</context>

<requirements>
1. Change `StagedFiles` to parse `git diff --cached --name-status -z`. Without `-z`, git C-quotes any non-ASCII path (`"\303\244rger.txt"`), and `CommitPaths` hands that quoted literal to git as a pathspec, which fails — so every umlaut-named file is reported as blocked forever and a vault whose staged set is all non-ASCII re-stalls with the original error. With `-z` the records are NUL-separated and unquoted: `<status>\0<path>\0` for add/modify/delete, and `<status>\0<old>\0<new>\0` for a rename or copy. Keep one entry per record, with both paths for a status starting with `R` or `C`.
2. Add `HasUnmergedPaths(ctx context.Context, repo string) (bool, error)` to `internal/git/git.go`, parsing `git status --porcelain -z` as records — a two-character code field followed by its path field, consuming one extra path field when the code starts with `R` or `C` — and returning true when any code is one of `DD`, `AU`, `UD`, `UA`, `DU`, `AA`, `UU`. Wrap failures with `errors.Wrapf`. Do not scan the first two characters of every NUL field: a rename's old path would false-positive. (Reusing `ConflictedFiles`, which already asks git with `--diff-filter=U`, is acceptable instead of a new parser.)
3. `runDirty` must call `HasUnmergedPaths` **before** `git.StageAll`, and when it returns true return `errors.Wrapf(ctx, git.ErrConflict, ...)` naming the unmerged paths (use the existing `git.ConflictedFiles` for the list), without staging or committing. This is the guard that stops a conflicted autostash from having its conflict markers swept into the next commit by `git add .`. Name the leftover `autostash` stash in the message and say that `git-ai-sync resolve` cannot clear this state (it requires `MERGE_HEAD` or a rebase directory), so the operator knows to resolve the conflict and then `git stash drop` — the autostash entry is retained, and popping it again re-applies the change.
4. `PullWithAutostash` must return `errors.Wrapf(ctx, git.ErrConflict, ...)` when the pull itself succeeds but leaves unmerged paths behind. `git pull --autostash` stashes the dirty index, merges, then reapplies the stash; when the blocked path was also changed upstream the reapply conflicts, the path is left as an unmerged `UU` entry with markers in the worktree, and no `MERGE_HEAD` exists, so `inConflictState` does not fire and the failure would otherwise be invisible.
5. In `commitUnits`, probe three spread units (first, middle, last) individually before bisecting, and commit each probe that succeeds. When **all three** probes fail, treat the failure as global — a hook rejecting everything, `index.lock`, a signing failure — and return the original commit error with `Blocked` empty, so a 300-unit index does not burn roughly 600 `git commit` invocations on every interval. Three rather than two: a vault can hold exactly two blocked files, and two failing probes would misclassify that as global and stall the whole vault — the bug the previous change fixed. Only when at least one probe succeeds is the remaining set a mix worth bisecting; the bisection then proceeds over the units not already committed by a probe.
6. Tests in `internal/sync/sync_test.go` and `internal/git/git_test.go`:
   - `StagedFiles` with a non-ASCII path (`ärger.md`): pin `core.quotePath=true` in the fixture (a global `false` would make the test pass without `-z`), assert it returns the unquoted path, and commit that unit through `CommitPaths` (a plain full commit never reaches `StagedFiles`); this fails without `-z`;
   - an autostash pop conflict: stage the blocked path **and** a second path the hook accepts (without an accepted path `Blocked` is empty and `runDirty` selects plain `Pull`, so `PullWithAutostash` is never reached); arrange for the blocked path to also change on the remote before `Run`, then assert `Run` returns an error satisfying `errors.Is(err, git.ErrConflict)` and that no commit containing conflict markers exists (`git log --all -S '<<<<<<<'` is empty);
   - a pre-existing unmerged path: leave a `UU` entry before `Run`, assert `Run` returns an `ErrConflict` error and that no new commit was created;
   - a global commit failure: install a fixture pre-commit hook that always exits 1 and appends one line to a counter file written under `t.TempDir()` (outside the clone, so `git add .` cannot stage it); stage 20 distinct units; assert `Run` returns the original error, `Blocked` is empty, and the counter holds at most 6 lines — the full commit plus three probes invoke the hook a constant number of times, while the old bisection invoked it roughly 40 times; assert on the counter, never on a timing;
   - a direct test for `HasUnmergedPaths`: clean repo → false, conflicted repo → true, and a rename present → not a false positive. If you reuse `ConflictedFiles` instead of adding a parser, skip this direct test and assert the guard through `Run`.
7. Add one bullet to `CHANGELOG.md` under the existing `## Unreleased` section — never add a second one:
   - fix: parse staged paths with git diff --cached --name-status -z so non-ASCII names commit, stop on an autostash pop conflict instead of committing conflict markers, and detect a global commit failure without bisecting every path
8. Self-check before finishing: re-run `<verification>`, then walk requirements 1–7 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Go standard `testing` package only; follow the existing test file's style
- Scratch repos only under `t.TempDir()`; no absolute or home-relative paths
- Never invoke the host's global pre-commit hook (`core.hooksPath`) — the test hook is a fixture written into the scratch clone
- Do not change the pre-commit guard's rule, the alert mechanism, or the stage → commit → pull → push order
- Do not change `Pull` — only `PullWithAutostash` and the unmerged-path guard are in scope
- Leave `internal/conflict` `ContinueMerge` / `ContinueRebase` unchanged — a merge or rebase continuation cannot be split
</constraints>

<verification>
Run `make precommit` — must pass. It runs `gofmt -l cmd internal`, `go vet ./...`, and `go test ./... -count=1`.
</verification>
