---
status: draft
spec: [009-port-conflict-resolution]
created: "2026-11-09T23:20:11Z"
branch: feat/port-conflict-resolution
---

# Add the `resolve` subcommand

<summary>
- `git-ai-sync resolve [repo-path]` resolves every conflicted file, continues the merge or rebase, and pushes.
- It defaults to the current directory, matching the Python CLI and `entrypoint.sh`.
- It takes the same instance lock as `sync` and releases it on every exit path.
- It refuses outside a merge or rebase conflict state, and refuses when the state has no conflicted files.
- A failed file leaves that file untouched; the command exits non-zero naming the paths that failed, while files that did resolve stay resolved and staged.
- A continuation that leaves new conflicts is reported with the still-conflicted paths.
- A rejected push is reported after the resolution is already committed, so the resolution is not lost.
- The command names each resolved path in its output.
- It works in both merge and rebase states.
- The Python source and `.github/workflows/` are untouched.
</summary>

<objective>
Add the `git-ai-sync resolve [repo-path]` subcommand: acquire the instance lock, refuse outside a conflict state, list the conflicted paths, resolve each through `internal/conflict`, continue the merge (`git commit --no-edit`) or rebase (`git rebase --continue`), and push. **Why:** this is the contract `entrypoint.sh:72` already calls, and without it a conflicted pull stops the Go watcher permanently.
</objective>

<context>
Read the approved spec at `specs/in-progress/009-port-conflict-resolution.md` — Desired Behavior 1-9, Acceptance Criteria 1 and 11-14, the Failure Modes table, and Constraints are the contract for this prompt. The Go gate is `make precommit-go`, not `make precommit`.

Read `internal/cli/cli.go` — `newRootCommand`, `newSyncCommand` (the `FindRepo` → `lock.Acquire` → `defer Release` pattern), `commandContext`, and `pathArg`. Read `internal/git/git.go` for `FindRepo`, `Push`, and the helpers prompt 1 added (`IsInMerge`, `IsInRebase`, `ConflictedFiles`, `StageFile`, `ContinueMerge`, `ContinueRebase`). Read `internal/conflict/conflict.go` from prompt 1 for `ResolveFile`. Read `internal/lock/lock.go` for `Acquire`, `Release`, and `ErrLocked`.

Read `src/git_ai_sync/__main__.py` (`cmd_resolve`, lines 533-608) for the reference order: validate the repository, acquire the lock, refuse when not in a conflict state, resolve all files, fail on any failed file, fail when zero files resolved, continue the rebase or merge, then push. Read `src/git_ai_sync/conflict_resolver.py` (`resolve_all_conflicts`, lines 226-281) for the per-file continue-on-failure behaviour and `do_continue_rebase` (lines 444-467) for the continuation dispatch.

Read `internal/sync/sync_test.go` for the local bare-remote fixture pattern (`newFixture`, `configure`, `runGit`, `writeFile`) and copy it, including pinning `core.hooksPath` to the fixture's own hooks directory and setting `GIT_EDITOR=true`. Read `internal/cli/cli_test.go` for the existing `Run` / `newRootCommand` test style.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors`; stdlib `errors` is already imported in `cli.go` as `stderrors`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — use stdlib `testing` (not Ginkgo/Gomega), as this repo does.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every external command takes the caller's `ctx`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — append to the existing `## Unreleased` section; do not create a second one.

The module path is `github.com/bborbe/git-ai-sync`.

<!-- OPEN QUESTION for the reviewer: Acceptance Criterion 11 says `git diff --cached --name-only` lists the resolved file after `resolve`, but the command then continues the merge/rebase, which commits the file and empties the staged diff. This prompt's tests assert the stronger end state the spec's Desired Behavior demands: the file is tracked (`git ls-files --error-unmatch` exits 0), carries both sides with no markers, and the pushed remote commit carries both sides. Confirm that reading; if the staged-diff assertion is literal, the command would have to stop before continuing, contradicting Desired Behavior 5-6. -->
<!-- OPEN QUESTION for the reviewer: the resolve command deliberately has no `--model`, `--strategy`, `--interval`, or `--log-level` flag. The spec forbids a model knob (declared divergence) and the command needs neither strategy nor interval; the logger is fixed at INFO. Confirm this is acceptable. -->
</context>

<requirements>
1. Add `newResolveCommand(ctx context.Context) *cobra.Command` to `internal/cli/cli.go` and register it in `newRootCommand` with `rootCmd.AddCommand(newResolveCommand(ctx))`. Use `Use: "resolve [path]"`, `Args: cobra.MaximumNArgs(1)`, and a short description such as "Resolve merge or rebase conflicts with Claude and push".

2. Implement the `RunE` flow in exactly this order:

   ```go
   func newResolveCommand(ctx context.Context) *cobra.Command {
       cmd := &cobra.Command{
           Use:   "resolve [path]",
           Short: "Resolve merge or rebase conflicts with Claude and push",
           Args:  cobra.MaximumNArgs(1),
           RunE: func(cmd *cobra.Command, args []string) error {
               runCtx := commandContext(ctx, cmd)
               logger := logging.New(cmd.ErrOrStderr(), slog.LevelInfo)

               repo, err := git.FindRepo(runCtx, pathArg(args))
               if err != nil {
                   return errors.Wrapf(runCtx, err, "find repository")
               }
               l, err := lock.Acquire(runCtx, repo)
               if err != nil {
                   return errors.Wrapf(runCtx, err, "acquire repository lock")
               }
               defer func() {
                   if releaseErr := l.Release(runCtx); releaseErr != nil {
                       logger.Warn(fmt.Sprintf("failed to release lock: %v", releaseErr))
                   }
               }()

               inRebase := git.IsInRebase(repo)
               inMerge := git.IsInMerge(repo)
               if !inRebase && !inMerge {
                   return errors.Errorf(runCtx, "not in a merge or rebase conflict state: %s", repo)
               }
               files, err := git.ConflictedFiles(runCtx, repo)
               if err != nil {
                   return errors.Wrapf(runCtx, err, "list conflicted files")
               }
               if len(files) == 0 {
                   state := "merge"
                   if inRebase {
                       state = "rebase"
                   }
                   return errors.Errorf(runCtx, "no conflicted files found in the %s state", state)
               }
               logger.Info(fmt.Sprintf("found %d conflicted file(s): %s", len(files), strings.Join(files, ", ")))

               failed := make([]string, 0)
               for _, path := range files {
                   if err := conflict.ResolveFile(runCtx, repo, path); err != nil {
                       logger.Error(fmt.Sprintf("failed to resolve %s: %v", path, err))
                       failed = append(failed, path)
                       continue
                   }
                   logger.Info(fmt.Sprintf("resolved and staged %s", path))
               }
               if len(failed) > 0 {
                   return errors.Errorf(runCtx, "failed to resolve %d file(s): %s", len(failed), strings.Join(failed, ", "))
               }

               if inRebase {
                   if err := git.ContinueRebase(runCtx, repo); err != nil {
                       return continuationError(runCtx, repo, "rebase", err)
                   }
               } else {
                   if err := git.ContinueMerge(runCtx, repo); err != nil {
                       return continuationError(runCtx, repo, "merge", err)
                   }
               }

               if err := git.Push(runCtx, repo); err != nil {
                   return errors.Wrapf(runCtx, err, "push after resolving conflicts")
               }
               logger.Info("resolved all conflicts, continued, and pushed")
               return nil
           },
       }
       return cmd
   }
   ```

   Add the helper:

   ```go
   func continuationError(ctx context.Context, repo string, kind string, cause error) error {
       conflicted, listErr := git.ConflictedFiles(ctx, repo)
       if listErr == nil && len(conflicted) > 0 {
           return errors.Wrapf(ctx, cause, "continuation failed - still have conflicts in: %s", strings.Join(conflicted, ", "))
       }
       return errors.Wrapf(ctx, cause, "continue %s", kind)
   }
   ```

   Rules:
   - `pathArg(args)` defaults to `.`; do not add a required argument.
   - The lock is acquired **before** the conflict-state guard. Acceptance Criterion 14's test holds the lock directly and runs `resolve` on a repository that is not necessarily conflicted, and expects the refusal to name the lock. Wrap the `Acquire` error with the literal words `acquire repository lock` so the output contains `lock`; do not return the raw `ErrLocked` message, which is "another instance is already running" and would fail `grep -qi 'lock'`.
   - The `defer` must release the lock on every exit path, including the guard failures and a failed file.
   - The conflict-state guard must accept either rebase or merge; a clean repository returns non-zero and writes no tracked file. Use `IsInRebase` first, matching `do_continue_rebase`.
   - An empty conflicted set returns an error whose message contains `no conflicted` (Acceptance Criterion 13 expects `grep -qi 'no conflicted'`), never a success for doing nothing.
   - Resolve every conflicted file; on failure, record the path, log it, and continue to the next file. If any file failed, return a non-zero error naming every failed path and do **not** continue or push; files that resolved stay resolved and staged.
   - On success, continue with `git.ContinueRebase` for a rebase and `git.ContinueMerge` for a merge. Do not use `git merge --continue`. If continuation fails and `ConflictedFiles` is non-empty, return an error naming the still-conflicted paths; otherwise wrap the continuation error.
   - Push only after a successful continuation. A push failure returns non-zero; the resolution is already committed and must not be rolled back.
   - Log the resolved path on success so the command's output names it (Acceptance Criterion 1). Log through `cmd.ErrOrStderr()` so the test can capture it with `cmd.SetErr`.
   - Do not add a `--model`, `--strategy`, `--interval`, or `--log-level` flag. The model is the CLI default and the other settings do not apply to this command.

3. Imports to add to `internal/cli/cli.go`: `log/slog`, `strings`, `github.com/bborbe/errors`, and `github.com/bborbe/git-ai-sync/internal/conflict`. The stdlib `errors` is already aliased as `stderrors`, so `errors.Errorf` / `errors.Wrapf` refer to `github.com/bborbe/errors` without a collision.

4. Create `internal/cli/resolve_test.go` (package `cli`) with a local bare-remote fixture modelled on `internal/sync/sync_test.go`. The fixture must:
   - `git init --bare` a remote, `symbolic-ref HEAD refs/heads/master`, configure the bare remote too (local `user.name`, `user.email`, `commit.gpgsign=false`, and `core.hooksPath` pinned to its own absolute hooks directory), clone it, and set the same local config on the clone, including `core.hooksPath` pinned to `$(git rev-parse --absolute-git-dir)/hooks`.
   - write `note.txt` (`base\n`) and `.gitignore` (`/.git-ai-sync.lock\n`), commit, and `push -u origin master`.
   - set `t.Setenv("GIT_EDITOR", "true")` so `git rebase --continue` cannot block on an editor.
   - provide a `git` helper that returns the error instead of failing, for the expected-to-conflict pull.

   Add a stub `claude` helper like prompt 1's: write an executable `claude` script into a temp dir, prepend the dir to `PATH`, drain stdin, and `cat "$CLAUDE_STUB_RESPONSE"`; build the response JSON with `encoding/json`. Add a `runResolve(t, repo) (output string, err error)` helper that builds `newRootCommand(context.Background())`, calls `cmd.SetErr(&buf)`, `cmd.SetArgs([]string{"resolve", repo})`, and `cmd.ExecuteContext(context.Background())`.

   Cover these cases:
   - `TestResolveMergeConflictPushes` (Acceptance Criteria 1 and 11-merge): commit `note.txt` as `from-ours\n` in the clone; push `from-theirs\n` from a second clone; run `git pull --no-rebase` in the clone and require it to fail with a conflict; the stub returns `from-ours\nfrom-theirs\n`. Run `resolve`. Assert: error is nil; `note.txt` contains `from-ours` once and `from-theirs` once; it contains no `<<<<<<<`; the captured output contains `note.txt`; `git ls-files --error-unmatch note.txt` exits 0; the remote's `master` equals the clone's `HEAD`; and `git -C <remote> show master:note.txt` contains both lines.
   - `TestResolveRebaseConflictPushes` (Acceptance Criteria 11-rebase): the same setup with `git pull --rebase`; after `resolve`, assert the rebase directories (`rebase-merge`, `rebase-apply`) are gone, both sides are present with no markers, the file is tracked, and the remote's `master` equals the clone's `HEAD`.
   - `TestResolveRefusesCleanRepo` (Acceptance Criterion 12): a clean fixture; `resolve` returns an error; `note.txt` is byte-identical. The lock file is created at the repository root but is gitignored, so assert on the tracked file, not on a clean `git status`.
   - `TestResolveRefusesNoConflictedFiles` (Acceptance Criterion 13): write `<clone>/.git/MERGE_HEAD` containing the current `HEAD` sha; run `resolve`; assert the error is non-nil and its message contains `no conflicted`.
   - `TestResolveRefusesWhenLocked` (Acceptance Criterion 14): `lock.Acquire(context.Background(), clone)`, then run `resolve`; assert the returned error satisfies `stderrors.Is(err, lock.ErrLocked)` and `strings.Contains(err.Error(), "lock")`; release the lock in a defer.
   - `TestResolvePushRejectedKeepsResolution` (Failure Modes: push rejected): write a `pre-receive` hook in the bare remote that exits 1; create the conflict; the stub returns both sides; run `resolve`; assert the returned error is non-nil, `note.txt` locally has both sides and no markers, the local `HEAD` is a commit (the continuation succeeded), and the remote's `master` did not move.

   Do not assert `git diff --cached --name-only` after a successful `resolve`; the continuation commits the file and empties the staged diff. The tracked-file and pushed-content assertions above are the meaningful equivalent (see the open question above).

5. Append one bullet under the existing `## Unreleased` section in `CHANGELOG.md` (prompt 1 created it). Do not create a second `## Unreleased`, and do not move or edit the `# Changelog` title or the SemVer preamble:

   ```markdown
   - feat: add `git-ai-sync resolve [repo-path]`, which resolves every conflicted file through the Claude CLI, continues the merge or rebase, and pushes under the same instance lock as `sync`
   ```
</requirements>

<constraints>
- `resolve` acquires the same instance lock as `sync` and releases it on every exit path.
- The command must refuse outside a merge or rebase conflict state, and refuse when the state holds no conflicted files.
- Every conflicted file is resolved; a failed file leaves that file byte-identical, the command exits non-zero naming the failed paths, and files that did resolve stay resolved and staged.
- When every file resolved, continue with `git rebase --continue` for a rebase and `git commit --no-edit` for a merge. `git merge --continue` is not a substitute.
- Continue only after every file resolved; push only after a successful continuation.
- If continuation leaves conflicts, exit non-zero naming the still-conflicted paths.
- If the push is rejected, exit non-zero; the resolution is already committed and must not be lost.
- The model is the CLI's default: add no `--model` flag and no model configuration.
- No test may call a real model, read real credentials, or depend on `HOME`; stub the `claude` executable on `PATH` in a temporary directory and never install it. The fixture must pass with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT modify `internal/git`, `internal/conflict`, `internal/lock`, `internal/sync`, or `internal/watch`; this prompt adds the command and its tests.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/cli/... -count=1 -v
go test ./... -count=1
make precommit-go
```

Expected: the build, vet, both test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing.

Before finishing, walk the assigned criteria against the tests:
- a forced conflicting pull resolves and pushes, naming the path, with both sides and no markers (1);
- both merge and rebase states resolve, continue, and push (11);
- a clean repository is refused and no tracked file is written (12);
- a conflict state with no conflicted files is refused with a message containing `no conflicted` (13);
- a held lock refuses `resolve` with an error that wraps `lock.ErrLocked` and names the lock (14);
- a rejected push leaves the resolution committed and exits non-zero (Failure Modes).

Acceptance Criterion 15 (no change under `src/`, `tests/`, `pyproject.toml`, `uv.lock`) and the real-CLI end-to-end runs are operator-side checks on the spec's Verification ladder; they are not runnable in this container. Do not add a `git diff origin/master` command to this verification block.

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
