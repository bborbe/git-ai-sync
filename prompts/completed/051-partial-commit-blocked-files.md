---
status: completed
summary: 'Completed partial-commit handling: added PullWithAutostash, wired it into runDirty when paths are blocked, and updated tests so accepted changes commit, pull, and push while refused paths stay staged and reported.'
execution_id: git-ai-sync-partial-commit-exec-051-partial-commit-blocked-files
dark-factory-version: v0.198.17
created: "2026-11-13T11:32:54Z"
queued: "2026-11-13T11:32:54Z"
started: "2026-11-13T11:47:26Z"
completed: "2026-11-13T11:49:48Z"
---

# Commit the files that pass when one staged file is refused

<summary>
- One refused file no longer holds back every other staged change in a vault.
- Files the hook accepts are committed in the same cycle, and pushed once the cycle's pull succeeds.
- Files the hook refuses stay staged and are named in the sync result and the log.
- The cycle still reports a commit error, so the existing stall alert keeps firing for a stuck file.
- A scratch repo with a pre-commit hook that rejects one path proves both halves.
</summary>

<objective>
Make one sync cycle commit the files the pre-commit hook accepts while leaving the refused ones staged and reported, so a single bad file cannot stop a whole vault from syncing — the failure observed on 2026-10-20 (one hook-blocked task file) and again on 2026-11-13.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `docs/dod.md` — the repo's Definition of Done; it requires tests for new functions.
Read `internal/sync/sync.go` — `runDirty` is the function under change: it calls `git.StageAll`, then one `git.Commit`, then `git.Pull`, then `git.Push`, and has four `Result` literals (StageAll failure, pull failure, push failure, success).
Read `internal/git/git.go` — `runGit` wraps every failure with `errors.Wrapf`; `StageFile` and `ConflictedFiles` show the per-call wrap style to follow (`go-error-wrapping-guide.md` in the coding plugin docs is the reference).
Read `internal/sync/sync_test.go` — `newFixture`, `configure`, `runGit`, `writeFile`, `addRemoteCommit` and `isAncestor` build a scratch bare remote plus clone. `configure` already pins `core.hooksPath` to the repo's own hooks directory, which is what lets a test install a fixture hook and have it run.
Read `internal/git/git_test.go` — its own `configure` helper does **not** pin `core.hooksPath`; requirement 8 fixes that.
Read `internal/supervise/supervise.go` — `runnerFor` treats `res.CommitErr != nil` as a failed cycle, records it in the state file, and drives the stall counter and the attention card.
Read `specs/completed/004-bug-unsafe-path-refusal-skips-pull.md` (Desired Behavior 4–5) — the Python-era fix for this same failure class. It unstaged the refused paths so the pull could run. This prompt deliberately keeps them staged instead, because a staged refusal is recoverable by editing the file and retrying; the deviation is recorded here and in the CHANGELOG bullet.
</context>

<requirements>
1. Add `StagedFiles(ctx context.Context, repo string) ([][]string, error)` to `internal/git/git.go`, returning one entry per non-empty line of `git diff --cached --name-status`, where each entry is one commit unit: a single path for `A`/`M`/`D`, and **both** paths for a rename (any status starting with `R`, e.g. `R100<TAB>old<TAB>new`), so a partial commit cannot split a rename into an add plus a stale original. Wrap failures with `errors.Wrapf`, matching `ConflictedFiles`.
2. Add `CommitPaths(ctx context.Context, repo, message string, paths []string) error` to `internal/git/git.go`, running `git commit --only -m <message> -- <paths...>`. Verified on 2026-11-13: `--only` presents only the named paths to the pre-commit hook, so a refused path elsewhere in the index does not block the commit, and the refused path stays staged. Wrap failures with `errors.Wrapf`, matching `StageFile`.
3. In `runDirty`, keep today's behaviour when the single commit succeeds. When `git.Commit` fails, isolate the refused units and commit the rest:
   - when the first commit fails, read the staged units then; if that read fails, return the original commit error unchanged rather than guessing (a hook refusal leaves the index untouched, so reading after the failure is equivalent and costs no extra subprocess on a healthy cycle);
   - on failure, split the pending units at unit boundaries and try `CommitPaths` on each half (flattening a half's units to paths), recursing into a half that fails and treating a single failing unit as blocked — never split a unit, so a rename's two paths always travel in the same call;
   - remove every unit that commits from the pending set;
   - leave a blocked unit's paths staged — never unstage, restore, or discard them;
   - stop recursing on an empty set, and stop when `ctx.Err() != nil`;
   - when every subset fails, return the original commit error instead of looping;
   - a commit failure is assumed to be a path refusal; a global failure is only distinguishable when nothing commits, which requirement 4 handles.
4. Add `Blocked []string` to `sync.Result`, naming the paths (units flattened) that could not be committed. Set `Committed: true` when at least one unit committed. Populate `Blocked` and wrap `CommitErr` with the blocked paths **only when at least one unit committed and another did not**; when nothing commits, return the original commit error unchanged and leave `Blocked` empty, because a failure that hits every path is not attributable to a path. Carry `Committed` and `Blocked` into **every** `Result` returned by `runDirty` — including the existing pull-failure and push-failure returns, whose literals today set `Committed: commitErr == nil` and would otherwise report a successful partial commit as uncommitted and drop `Blocked`. The supervisor keys its stall counter and attention card off `CommitErr`, and `internal/cli/cli.go`'s `sync` command logs and returns it, so a partial commit still exits non-zero; a stuck file must keep alerting even though the rest of the vault syncs, and neither consumer is changed.
5. In `runDirty`, when the commit left blocked paths staged, run the pull with `--autostash`. Without it a true-merge pull is refused by the dirty index (`error: Your local changes to the following files would be overwritten by merge: <blocked>`) and the accepted commits are never pushed, so the vault still stalls. Add `PullWithAutostash(ctx context.Context, repo string, strategy Strategy) error` to `internal/git/git.go`, running `git pull --no-rebase --autostash` (or `--rebase --autostash` for `StrategyRebase`), and call it from `runDirty` when `blocked` is non-empty; otherwise keep the existing `git.Pull`. Verified on 2026-11-13 in a scratch repo holding a local accepted commit and a staged blocked path: plain `git pull --no-rebase` fails with that error and the push is rejected, while `git pull --no-rebase --autostash` merges, restores the blocked path to the index, and the push succeeds. The blocked path must be staged again once the pull returns — autostash's temporary unstaging is acceptable only because the path is restaged by the time the cycle ends.
6. Do not add a separate log line for the blocked paths. In supervise mode `watch.Run` already logs `result.CommitErr` at ERROR on every refused cycle, and `supervise.go` records it in the state file; requirement 4 makes that message name the blocked paths, so a second WARN would repeat the same fact each interval.
7. Tests in `internal/sync/sync_test.go`, using the existing fixture helpers:
   - write an executable `.git/hooks/pre-commit` into the clone that decides from `git diff --cached --name-only` — the index git presents to the hook, which under `--only` is the named subset, not the whole index — and exits 1 when the chosen path appears; do not decide from `git status --porcelain`, which shows the blocked path regardless and would make the partial commit impossible;
   - stage one blocked file and one normal file, run `Run`, then assert: the normal file appears in `git log --name-only HEAD`; the blocked file does not; the blocked file is still staged (`git status --porcelain` still lists it); `Result.Blocked` names it; `Result.CommitErr` is non-nil and its message names the blocked path; `Result.Committed` is true;
   - the same cycle asserts the merge landed and the accepted commit left the machine: use `git.StrategyMerge`, advance the remote with `addRemoteCommit` before `Run`, then assert the remote commit is an ancestor of HEAD (`isAncestor`), `Result.Pushed` is true, the accepted commit is on the remote (`git -C <remote> log --name-only` names the normal file), and the blocked file is still staged;
   - a second case where every staged path is refused: assert `Committed` is false, `CommitErr` is non-nil, and `Blocked` is empty.
8. Pin `core.hooksPath` in `internal/git/git_test.go`'s `configure` helper exactly as `internal/sync/sync_test.go` does — `git config --local core.hooksPath <git rev-parse --absolute-git-dir>/hooks` — because a developer machine's global config can point `core.hooksPath` at its own hooks directory, so without the pin a fixture hook in `<clone>/.git/hooks` never runs (spec 008 / prompt 035 fixed this same bug in the sync fixture). Then add direct tests in `internal/git/git_test.go` for `StagedFiles` (mixed add/modify/rename, and the rename returning both paths as one unit) and `CommitPaths` (commits only the named paths, leaves the others staged, and surfaces an executable fixture `pre-commit` hook's refusal), following the existing per-function test style.
9. Add one bullet to `CHANGELOG.md` under `## Unreleased` — create that section directly above `## v0.19.3` (there is none today); never add a second one:
   `- fix: commit the files the pre-commit hook accepts when one staged path is refused, leaving the refused path staged and named, so a single bad file no longer stalls a whole vault`
10. Self-check before finishing: re-run `<verification>`, then walk requirements 1–9 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Go standard `testing` package only; follow the existing test file's style
- Scratch repos only under `t.TempDir()`; no absolute or home-relative paths
- Never invoke the host's global pre-commit hook (`core.hooksPath`) — the test hook is a fixture written into the scratch clone
- Do not change the pre-commit guard's rule, the alert mechanism, or the stage → commit → pull → push order
- A commit that fails for a reason other than a path-specific refusal must surface the original error, not be swallowed
- `internal/conflict` `ContinueMerge` / `ContinueRebase` also run `git commit` and can be hook-refused, but a merge or rebase continuation cannot be split — leave those paths unchanged and out of scope
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass. (The run uses `hideGit=true`, which masks `.git` and leaves `ROOTDIR` empty, so `make` dies before any target runs unless `ROOTDIR` is passed explicitly.)
</verification>
