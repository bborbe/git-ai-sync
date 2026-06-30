---
status: completed
summary: Added configurable pull strategy (merge/rebase) to git-ai-sync watch and sync commands with full test coverage
execution_id: git-ai-sync-merge-strategy-exec-005-add-strategy-flag
dark-factory-version: dev
created: "2026-06-30T15:00:00Z"
queued: "2026-06-30T16:19:00Z"
started: "2026-06-30T16:19:24Z"
completed: "2026-06-30T16:30:40Z"
---

<summary>
- Users can choose between merge and rebase pull strategies per-vault, defaulting to merge
- Merge strategy avoids the redundant-commit detection failure that wedges `git pull --rebase` in autocommit + multi-client vaults
- The `watch` and `sync` commands accept a `--strategy {merge,rebase}` flag and honor the `GIT_AI_SYNC_STRATEGY` env var
- A new `pull_merge` git operation mirrors the existing `pull_rebase` structure and error handling
- Conflict-error messages in `watch` and `sync` no longer say "Rebase conflicts" — they are generic so both strategies read correctly
- Existing conflict-resolution flow is untouched (already supports both merge and rebase states)
- README documents the new flag; CHANGELOG records the change under Unreleased
</summary>

<objective>
Add a configurable pull strategy (`merge` or `rebase`) to `git-ai-sync watch` and `git-ai-sync sync` so the default `merge` strategy avoids the `git pull --rebase` wedge on redundant-commit detection in autocommit + multi-client vaults. The conflict resolver already handles both merge and rebase states, so no conflict_resolver changes are needed.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-cli-arguments-guide.md` for the env-var-default + CLI-override pattern (env default via `os.getenv`, CLI flag overrides).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (failing test first, then implement, then full suite green).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the Unreleased-section convention.
Read `docs/dod.md` for the Definition of Done checklist.

Discovered exemplar files (in-tree patterns to mirror):
- `src/git_ai_sync/git_operations.py` — `pull_rebase` (function to clone for `pull_merge`), `is_in_rebase`/`is_in_merge` (conflict-state checks already present), `subprocess.run(..., check=False)` idiom used throughout.
- `src/git_ai_sync/__main__.py` — `parse_args` (`--interval` flag with `os.getenv` default + `choices=` is the exact pattern to clone for `--strategy`), `cmd_watch` (pull block at the `git_operations.pull_rebase(git_repo)` call + the `except git_operations.GitError` conflict branch that says "Rebase conflicts detected"), `cmd_sync` (step 5 pull block with the same conflict message).
- `tests/test_git_operations.py` — `TestPullRebase` class (copy → `TestPullMerge`), `_mock_result` helper, `REPO` constant.
- `tests/test_main.py` — `_sync_args()` helper (must add `strategy` field or `cmd_sync` raises `AttributeError`), `TestCmdSync.test_full_sync` (asserts `pull_rebase.assert_called_once()` — needs a merge-strategy variant).
- `README.md` — Quick Start section documents `watch` and `sync` invocations.
- `CHANGELOG.md` — `## Unreleased` section does not yet exist; add it above the latest `## v0.7.0` heading, after the SemVer preamble.
</context>

<requirements>
1. Add a `pull_merge` function to `src/git_ai_sync/git_operations.py`, placed immediately after the existing `pull_rebase` function. It must mirror `pull_rebase`'s structure and error handling exactly, with two differences:
   - Run `["git", "pull", "--no-rebase"]` instead of `["git", "pull", "--rebase"]`.
   - On non-zero exit, check `is_in_merge(repo_path)` (not `is_in_rebase`) and raise `GitError("Merge conflicts detected - use 'git-ai-sync resolve' to resolve")` when in merge state; otherwise raise `GitError(f"Failed to pull: {result.stderr}")`.

   The existing `pull_rebase` to mirror:
   ```python
   def pull_rebase(repo_path: Path) -> None:
       """Pull with rebase from remote.

       Args:
           repo_path: Path to git repository

       Raises:
           GitError: If pull fails or conflicts occur
       """
       result = subprocess.run(
           ["git", "pull", "--rebase"],
           cwd=repo_path,
           capture_output=True,
           text=True,
           check=False,
       )

       if result.returncode != 0:
           # Check if it's a conflict
           if is_in_rebase(repo_path):
               raise GitError("Rebase conflicts detected - use 'git-ai-sync resolve' to resolve")
           raise GitError(f"Failed to pull: {result.stderr}")
   ```

   The new function must have full type hints (`repo_path: Path` -> `None`) and a docstring in the same style ("Pull with merge from remote.").

2. Add a `--strategy` argument to BOTH the `watch` and `sync` subparsers in `parse_args` in `src/git_ai_sync/__main__.py`. Use the same env-var-default pattern as `--interval`:
   ```python
   watch_parser.add_argument(
       "--strategy",
       default=os.getenv("GIT_AI_SYNC_STRATEGY", "merge"),
       choices=["merge", "rebase"],
       help="Pull strategy: merge or rebase",
   )
   ```
   Add the identical `add_argument` call to `sync_parser` as well. Place each `--strategy` definition after the existing `path` argument on its respective subparser.

   IMPORTANT — env-var validation boundary: argparse `choices=` validates ONLY CLI-supplied values, NOT the default. If an operator exports `GIT_AI_SYNC_STRATEGY=squash`, argparse silently accepts it as the default and `args.strategy == "squash"`, which then falls through the dispatch `else` branch to `pull_rebase` — a confusing silent misroute instead of an error. After `parse_args` returns (or at the top of both `cmd_watch` and `cmd_sync` before the dispatch), add an explicit guard:
   ```python
   if args.strategy not in ("merge", "rebase"):
       logger.error(f"Invalid strategy {args.strategy!r}; must be merge or rebase")
       sys.exit(2)
   ```
   This guard is the only thing that catches an invalid env-var value; without it the misroute is silent.

3. In `cmd_watch`, replace the single call `git_operations.pull_rebase(git_repo)` (inside the `try:` block after `before_pull = ...`) with a dispatch on `args.strategy`:
   ```python
   if args.strategy == "merge":
       git_operations.pull_merge(git_repo)
   else:
       git_operations.pull_rebase(git_repo)
   ```

4. In `cmd_watch`, genericize the conflict-error message in the `except git_operations.GitError as e:` block. The current code:
   ```python
   except git_operations.GitError as e:
       if "conflicts" in str(e).lower():
           logger.error(f"Rebase conflicts detected: {e}")
           logger.error(f"Run 'git-ai-sync resolve {git_repo}' to resolve")
           sys.exit(1)
       raise
   ```
   Change `f"Rebase conflicts detected: {e}"` to `f"Conflicts detected: {e}"` so the message is correct for both merge and rebase strategies.

5. In `cmd_sync`, replace the step-5 pull block. The current code:
   ```python
   logger.info("Pulling with rebase...")
   try:
       git_operations.pull_rebase(git_repo)
       logger.info("Pulled")
   except git_operations.GitError as e:
       if "conflicts" in str(e).lower():
           logger.error(f"Rebase conflicts detected: {e}")
           logger.error("Run 'git-ai-sync resolve' to resolve conflicts")
           sys.exit(1)
       logger.error(f"Failed to pull: {e}")
       sys.exit(1)
   ```
   Change to dispatch on strategy and genericize the message:
   ```python
   logger.info(f"Pulling with {args.strategy}...")
   try:
       if args.strategy == "merge":
           git_operations.pull_merge(git_repo)
       else:
           git_operations.pull_rebase(git_repo)
       logger.info("Pulled")
   except git_operations.GitError as e:
       if "conflicts" in str(e).lower():
           logger.error(f"Conflicts detected: {e}")
           logger.error("Run 'git-ai-sync resolve' to resolve conflicts")
           sys.exit(1)
       logger.error(f"Failed to pull: {e}")
       sys.exit(1)
   ```

6. Add tests to `tests/test_git_operations.py`. Copy the `TestPullRebase` class as `TestPullMerge` and adapt:
   - Import `pull_merge` (add to the import block from `git_ai_sync.git_operations`).
   - `test_pulls`: assert `mock.call_args[0][0] == ["git", "pull", "--no-rebase"]`.
   - `test_raises_conflict_error`: patch `git_ai_sync.git_operations.is_in_merge` (not `is_in_rebase`) to return `True`, and assert `pytest.raises(GitError, match="conflicts")`.

   The existing `TestPullRebase` to copy:
   ```python
   class TestPullRebase:
       def test_pulls(self) -> None:
           with patch("subprocess.run", return_value=_mock_result()) as mock:
               pull_rebase(REPO)
               assert mock.call_args[0][0] == ["git", "pull", "--rebase"]

       def test_raises_conflict_error(self) -> None:
           with (
               patch(
                   "subprocess.run",
                   return_value=_mock_result(returncode=1, stderr="conflict"),
               ),
               patch("git_ai_sync.git_operations.is_in_rebase", return_value=True),
               pytest.raises(GitError, match="conflicts"),
           ):
               pull_rebase(REPO)
   ```

7. Update `tests/test_main.py`:
   - Add `strategy="merge"` to the `_sync_args()` helper so `cmd_sync` can read `args.strategy` without `AttributeError`:
     ```python
     def _sync_args(path: str = ".") -> argparse.Namespace:
         return argparse.Namespace(command="sync", path=path, strategy="merge")
     ```
   - In `TestCmdSync`, add a `test_full_sync_merge_strategy` test that passes `strategy="merge"` via the args and asserts `mock_git.pull_merge.assert_called_once()` (mirror `test_full_sync` but for merge dispatch).
   - Add a `test_full_sync_rebase_strategy` test that passes `strategy="rebase"` and asserts `mock_git.pull_rebase.assert_called_once()` (verifies the rebase path still dispatches correctly).
   - In `TestParseArgs`, add `test_watch_strategy_default` asserting `args.strategy == "merge"` for `["git-ai-sync", "watch"]`, and `test_watch_strategy_rebase` asserting `args.strategy == "rebase"` for `["git-ai-sync", "watch", "--strategy", "rebase"]`.
   - Add a `cmd_watch` dispatch test mirroring the sync dispatch tests: a `test_cmd_watch_merge_strategy` that builds watch args with `strategy="merge"` and asserts `mock_git.pull_merge.assert_called_once()`, and a `test_cmd_watch_rebase_strategy` asserting `mock_git.pull_rebase.assert_called_once()` for `strategy="rebase"`. This covers the `cmd_watch` dispatch code path edited in requirement 3 (without it, `cmd_watch`'s strategy dispatch is only shape-verified indirectly).
   - Note: the `TestLockAcquisition.test_cmd_sync_exits_on_lock_error` test builds a raw `argparse.Namespace(path=str(tmp_path))` without `strategy`. This is fine because `acquire_lock` raises before `cmd_sync` reaches the pull step where `args.strategy` is read — do NOT modify that test. If it nonetheless fails with `AttributeError`, add `strategy="merge"` to that Namespace too.

8. Update `README.md`:
   - **Quick Start section**: add a `--strategy` example to the `watch` and `sync` command examples, and add a one-line note that `merge` is the default and `rebase` is available, with the `GIT_AI_SYNC_STRATEGY` env var as an alternative. Keep it terse.
   - **Configuration table** (the env-var reference table listing `GIT_AI_SYNC_INTERVAL`, `GIT_AI_SYNC_MODEL`, `GIT_AI_SYNC_COMMIT_PREFIX`): add a new row `| GIT_AI_SYNC_STRATEGY | Pull strategy (merge or rebase) | merge |` so the env var is discoverable alongside the others.

9. Update `CHANGELOG.md`: add a `## Unreleased` section directly above the `## v0.7.0` heading (the file currently has only `# Changelog` then `## v0.7.0` — there is NO SemVer preamble above it, so insert `## Unreleased` immediately after the `# Changelog` title line) with the entry:
   ```
   - feat: Add `--strategy {merge,rebase}` flag to `watch` and `sync` commands (env `GIT_AI_SYNC_STRATEGY`, default `merge`) so the pull strategy is configurable per-vault; merge default avoids the `git pull --rebase` wedge on redundant-commit detection in autocommit + multi-client vaults
   ```

10. Run the full test suite and precommit checks. All existing tests must still pass; all new tests must pass.

</requirements>

<constraints>
- Conflict resolver already handles both merge + rebase (`is_in_merge`, `continue_merge` exist in `git_operations.py`) — DO NOT modify `conflict_resolver.py`
- Python 3.14, uv-based workflow, `uv run` prefix for all commands
- Ruff line-length 100, mypy strict (full type hints on `pull_merge`)
- `subprocess.run` with `check=False` + explicit error handling (per docs/dod.md)
- Functions over classes for stateless operations (follow `git_operations.py` style)
- Repo-relative paths only in prompt body (never `~/` or `/Users/`)
- Combine TDD red+green in ONE prompt (dark-factory validator rejects red-only via non-zero exit) — add failing test, then implement, then run full suite to exit 0
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Follow the existing code style: functions (not classes) in `git_operations.py`, `subprocess.run` with `check=False`
- Type hints required (mypy strict mode)
- Keep the debounce/skip logic in `cmd_watch` unchanged — only the pull dispatch and conflict message change
</constraints>

<verification>
Run `uv run make precommit` — must pass (format + lint + typecheck + test).

Additional verification:
- `uv run pytest tests/test_git_operations.py -k TestPullMerge` — new merge-pull tests pass.
- `uv run pytest tests/test_main.py -k strategy` — new strategy-dispatch tests pass.
- `uv run mypy src` — no new type errors (strict mode).
- `uv run ruff check .` — no new lint errors.
</verification>
