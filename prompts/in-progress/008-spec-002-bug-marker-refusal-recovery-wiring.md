---
status: approved
spec: [002-bug-marker-refusal-recovery]
created: "2026-09-02T21:20:00Z"
queued: "2026-09-02T21:25:33Z"
---

<summary>
- Both one-shot sync and watch mode route commits through the bounded marker-refusal recovery created by the prior prompts of this spec
- On a final marker-refusal failure, sync exits non-zero and logs the still-flagged files plus the `git-ai-sync resolve` pointer
- On a final marker-refusal failure, watch mode exits non-zero instead of looping forever, and never logs "Continuing to watch..." for this failure
- The happy-path sync flow (stage → commit → pull → push) is unchanged; non-marker commit failures still behave exactly as today
- A CHANGELOG entry records the fix under Unreleased
</summary>

<objective>
Wire the marker-refusal recovery into `cmd_sync` and `cmd_watch` in `src/git_ai_sync/__main__.py`, with the final-failure surface and `sys.exit(1)` in both commands, update the affected `tests/test_main.py` tests, and record the fix in the CHANGELOG.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention (insert after the frozen preamble, directly above the highest `## vX.Y.Z`; flat list; conventional prefixes).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/__main__.py` — `cmd_sync` (step-4 commit block, local-import style), `cmd_watch` (commit lines inside the `if has_local_changes:` branch, outer `except git_operations.GitError` handler), and `cmd_resolve` (the existing `asyncio.run(...)` pattern to mirror).
Read `src/git_ai_sync/conflict_resolver.py` — `commit_with_marker_recovery(repo_path: Path, message: str, model: str = ...) -> None` (async; created by the prior prompt of this spec; raises `git_operations.MarkerRefusalError` on final refusal).
Read `src/git_ai_sync/git_operations.py` — `MarkerRefusalError` (a `GitError` subclass) — its catch must come BEFORE the generic `GitError` catch.
Read `tests/test_main.py` — `_mock_git_ops()`, the `_GIT_OPS`/`_CONFIG`/`_ACQUIRE_LOCK` patch constants, `TestCmdSync.test_full_sync` (asserts `mock_git.commit.assert_called_once()` — this assertion must be updated), and `TestCmdWatchDispatch` (the watch harness with class-patched `ChangeTracker` and `time.sleep` `side_effect=[None, KeyboardInterrupt()]`).
Read `CHANGELOG.md` — the frozen preamble ends at the `* PATCH version...` line; the highest released section is `## v0.8.0`.
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, `cmd_sync`:

   a. Update the local imports. OLD:
      ```python
      from pathlib import Path

      from git_ai_sync import git_operations
      from git_ai_sync.config import Config
      ```

      NEW:
      ```python
      import asyncio
      from pathlib import Path

      from git_ai_sync import conflict_resolver, git_operations
      from git_ai_sync.config import Config
      ```

   b. Replace the step-4 commit block. OLD:
      ```python
          # 4. Commit with auto-generated message
          commit_msg = git_operations.generate_commit_message(config.commit_prefix)
          logger.info(f"Committing: {commit_msg}")
          try:
              git_operations.commit(git_repo, commit_msg)
              logger.info("Committed")
          except git_operations.GitError as e:
              logger.error(f"Failed to commit: {e}")
              sys.exit(1)
      ```

      NEW (mirrors the `cmd_resolve` `asyncio.run` pattern):
      ```python
          # 4. Commit with auto-generated message (with marker-refusal recovery)
          commit_msg = git_operations.generate_commit_message(config.commit_prefix)
          logger.info(f"Committing: {commit_msg}")

          async def run_commit() -> None:
              await conflict_resolver.commit_with_marker_recovery(git_repo, commit_msg, config.model)

          try:
              asyncio.run(run_commit())
              logger.info("Committed")
          except git_operations.GitError as e:
              logger.error(f"Failed to commit: {e}")
              sys.exit(1)
      ```

      The single `except git_operations.GitError` catch is sufficient for sync: `MarkerRefusalError` is a `GitError` subclass, and its message (raised by `commit_with_marker_recovery`) already names the still-flagged files and contains `Run 'git-ai-sync resolve <path>' to resolve` — so logging `str(e)` satisfies the sync final-failure surface (AC 6).

2. In `src/git_ai_sync/__main__.py`, `cmd_watch`:

   a. Update the local imports. OLD:
      ```python
      import time
      from pathlib import Path

      from git_ai_sync import git_operations
      from git_ai_sync.config import Config
      from git_ai_sync.file_watcher import ChangeTracker
      ```

      NEW:
      ```python
      import asyncio
      import time
      from pathlib import Path

      from git_ai_sync import conflict_resolver, git_operations
      from git_ai_sync.config import Config
      from git_ai_sync.file_watcher import ChangeTracker
      ```

   b. Replace the commit lines inside the `if has_local_changes:` branch. OLD:
      ```python
                      git_operations.stage_all(git_repo)
                      commit_msg = git_operations.generate_commit_message(config.commit_prefix)
                      git_operations.commit(git_repo, commit_msg)
                      logger.info(f"Committed: {commit_msg}")
      ```

      NEW:
      ```python
                      git_operations.stage_all(git_repo)
                      commit_msg = git_operations.generate_commit_message(config.commit_prefix)

                      async def run_commit() -> None:
                          await conflict_resolver.commit_with_marker_recovery(
                              git_repo, commit_msg, config.model
                          )

                      asyncio.run(run_commit())
                      logger.info(f"Committed: {commit_msg}")
      ```

   c. Add a `MarkerRefusalError` catch as the FIRST clause of the outer `except` chain (it MUST precede `except git_operations.GitError` since it is a subclass). OLD:
      ```python
                  except git_operations.GitError as e:
                      logger.error(f"Sync failed: {e}")
                      logger.info("Continuing to watch...")

                  except Exception as e:
                      logger.exception(f"Unexpected error: {e}")
                      logger.info("Continuing to watch...")
      ```

      NEW:
      ```python
                  except git_operations.MarkerRefusalError as e:
                      logger.error(f"Sync failed: {e}")
                      sys.exit(1)

                  except git_operations.GitError as e:
                      logger.error(f"Sync failed: {e}")
                      logger.info("Continuing to watch...")

                  except Exception as e:
                      logger.exception(f"Unexpected error: {e}")
                      logger.info("Continuing to watch...")
      ```

      This is the AC 7 behavior: watch exits non-zero on a final marker refusal and never logs `Continuing to watch...` on the refusal path, while all non-marker `GitError`s and other exceptions keep the existing `Continuing to watch...` behavior unchanged.

3. Update `tests/test_main.py`:

   a. Add `AsyncMock` to the `from unittest.mock import ...` line (currently `MagicMock, patch` → `AsyncMock, MagicMock, patch`).
   b. Change `from git_ai_sync.git_operations import GitError` to `from git_ai_sync.git_operations import GitError, MarkerRefusalError`.
   c. Update `TestCmdSync.test_full_sync`: it currently asserts `mock_git.commit.assert_called_once()`, which no longer holds (commits now go through `conflict_resolver.commit_with_marker_recovery`). Patch the recovery with an AsyncMock and assert it was awaited instead:
      ```python
      def test_full_sync(self) -> None:
          mock_git = _mock_git_ops()
          mock_git.find_git_repo.return_value = Path("/repo")
          mock_git.get_current_branch.return_value = "master"
          mock_git.has_changes.return_value = True
          mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
          args = argparse.Namespace(command="sync", path="/repo", strategy="rebase")
          with (
              patch(_GIT_OPS, mock_git),
              patch(_CONFIG),
              patch(_ACQUIRE_LOCK),
              patch(
                  "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                  new_callable=AsyncMock,
              ) as mock_commit_recovery,
          ):
              cmd_sync(args)
              mock_git.stage_all.assert_called_once()
              mock_commit_recovery.assert_awaited_once()
              mock_git.pull_rebase.assert_called_once()
              mock_git.push.assert_called_once()
      ```
      (`test_full_sync_merge_strategy`, `test_full_sync_rebase_strategy`, and `test_conflict_exits` must ALSO patch `git_ai_sync.conflict_resolver.commit_with_marker_recovery` with a default AsyncMock (no `side_effect`). `_GIT_OPS` only affects `__main__`'s late import, NOT `conflict_resolver`'s module-level `git_operations` reference — an unmocked recovery runs the real `git_operations.commit` against the nonexistent `/repo` path and fails all three tests. With the mock in place they keep testing their real targets: pull strategy dispatch (merge/rebase) and the pull-conflict exit path.)
   d. Add a module-level `test_sync_marker_refusal_exits` (AC 6). IMPORTANT mock detail: the except clauses in the commands resolve `git_operations.MarkerRefusalError` through the patched module, so the mock MUST expose the real class — build with `mock_git = _mock_git_ops()` then set `mock_git.MarkerRefusalError = MarkerRefusalError` (without this, a MagicMock auto-attribute silently misses the match and the test fails). Then: `mock_git.has_changes.return_value = True`; patch `_GIT_OPS`, `_CONFIG`, `_ACQUIRE_LOCK`; patch `git_ai_sync.conflict_resolver.commit_with_marker_recovery` with AsyncMock `side_effect=MarkerRefusalError("Commit refused by pre-commit hook after 3 attempts; still-flagged files: conflict.md. Run 'git-ai-sync resolve /repo' to resolve")`; `pytest.raises(SystemExit) as exc_info`; assert `exc_info.value.code == 1` and caplog records contain `conflict.md` and `Run 'git-ai-sync resolve`.
   e. Add a module-level `test_watch_marker_refusal_exits` (AC 7) mirroring the `TestCmdWatchDispatch` harness (locally import `cmd_watch` from `git_ai_sync.__main__` and `ChangeTracker` from `git_ai_sync.file_watcher` inside the function, as the harness methods do): args `argparse.Namespace(path=".", interval=30, strategy="merge")`; build `mock_git = _mock_git_ops()` and set `mock_git.MarkerRefusalError = MarkerRefusalError` (same mock detail as 3d — required for the new `except git_operations.MarkerRefusalError` clause in `cmd_watch`); `mock_git.has_changes.return_value = True`, `mock_git.generate_commit_message.return_value = "auto: 2026-01-01"`; patch `_GIT_OPS`, `_CONFIG`, `_ACQUIRE_LOCK`, `ChangeTracker.start`/`stop`/`get_seconds_since_last_change` (return_value=999), `time.sleep` `side_effect=[None, KeyboardInterrupt()]`; patch `git_ai_sync.conflict_resolver.commit_with_marker_recovery` with AsyncMock `side_effect=MarkerRefusalError("Commit refused by pre-commit hook after 3 attempts; still-flagged files: conflict.md. Run 'git-ai-sync resolve /repo' to resolve")`. Assert `exc_info.value.code == 1`, NO caplog record contains `Continuing to watch...`, and records contain `conflict.md` and `Run 'git-ai-sync resolve`.

4. Update `CHANGELOG.md`: insert a `## Unreleased` section between the frozen preamble (which ends at `* PATCH version when you make backwards-compatible bug fixes.`) and `## v0.8.0`, with a single `fix:` bullet:

   ```
   ## Unreleased

   - fix: Auto-recover when the global pre-commit hook refuses a commit over conflict-marker content — detect the refusal signature, resolve flagged files via the existing AI conflict resolver, re-stage, and retry the commit up to 3 attempts; on final failure sync and watch both exit non-zero with the still-flagged files and a `git-ai-sync resolve` pointer, so watch mode no longer loops silently forever on this failure
   ```

   Do NOT modify anything above the `# Changelog` title, the preamble lines, or the `## v0.8.0` section.

5. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk each acceptance criterion from the spec (AC 6 sync exit, AC 7 watch exit) against the change.
</requirements>

<constraints>
- Reuse the existing `resolve_conflict_with_claude` recovery path via `conflict_resolver.commit_with_marker_recovery` — do NOT re-implement resolution or a retry loop in `__main__.py`
- Do NOT weaken, bypass, or extend the global pre-commit hook
- Watch mode: the `MarkerRefusalError` catch MUST come before `except git_operations.GitError` and MUST NOT log `Continuing to watch...`; non-marker `GitError` and `Exception` paths keep the existing `Continuing to watch...` behavior unchanged
- Sync mode: final marker-refusal failure exits non-zero; the happy path (stage → commit → pull → push) and all pull/push behavior are unchanged
- Preserve existing error-handling shape: `GitError` subclasses before broad `except Exception`
- Type hints required (mypy strict); pytest conventions only
- Follow the local-import style already used in `cmd_sync`/`cmd_watch` (imports inside the function body)
- All new tests are module-level functions (node ids must match the spec's AC evidence paths: `test_sync_marker_refusal_exits`, `test_watch_marker_refusal_exits`)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the acceptance-criteria tests pass:
`uv run pytest tests/test_main.py::test_sync_marker_refusal_exits tests/test_main.py::test_watch_marker_refusal_exits -v`
Confirm the full main test module: `uv run pytest tests/test_main.py -v`
Confirm the CHANGELOG structure: `grep -n "^## " CHANGELOG.md` — must show `## Unreleased` directly above `## v0.8.0`, with nothing inserted above the `# Changelog` title or inside the preamble.
</verification>
