---
status: completed
spec: [004-bug-unsafe-path-refusal-skips-pull]
summary: Restructured cmd_watch and cmd_sync so a refused commit never skips the pull, with watch continuing to the next interval and sync exiting non-zero only on unrecoverable refusals
execution_id: git-ai-sync-exec-019-spec-004-never-skip-pull
dark-factory-version: v0.196.0
created: "2026-10-11T14:30:00Z"
queued: "2026-10-11T12:54:21Z"
started: "2026-10-11T13:00:35Z"
completed: "2026-10-11T13:10:54Z"
---

<summary>
- A commit that cannot be made no longer stops the checkout from receiving remote commits — the pull always runs
- Watch mode reports the refusal loudly (naming the offending path) and keeps watching on its normal interval, instead of treating a refusal as a dead cycle
- One-shot sync behaves the same way: it pulls even when its commit was refused, and its exit code reflects the outcome — zero when recovery succeeded, non-zero when the paths are still offending
- The log no longer prints a bare `Sync failed: Failed to commit:` with nothing after it, so a refusal is never indistinguishable from an empty cycle
- Every existing pull, push, conflict-detection, and marker-refusal behavior is unchanged
- The local checkout can no longer silently fall behind `origin` because of a filename
</summary>

<objective>
In `src/git_ai_sync/__main__.py`, restructure `cmd_watch` and `cmd_sync` so a commit failure (marker refusal or unsafe-path refusal) never skips the pull. Watch continues to the next interval after a refusal; sync pulls and then exits non-zero only when the refusal was unrecoverable.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest + pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention.
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/__main__.py` — `cmd_watch` (the sync `try` block: `has_local_changes` → `stage_all` → `commit_with_marker_recovery` → pull, all inside ONE `try`, with the `except git_operations.MarkerRefusalError` / `except git_operations.GitError` / `except Exception` chain at the end) and `cmd_sync` (steps 3-6, where a commit failure `sys.exit(1)`s at step 4 before the pull at step 5).
Read `src/git_ai_sync/conflict_resolver.py` — `commit_with_marker_recovery(repo_path: Path, message: str, model: str = ...) -> None` (async; raises `MarkerRefusalError` after 3 attempts for a marker refusal, `UnsafePathRefusalError` after 3 attempts for an unsafe path — and unstages the offending paths before raising the latter).
Read `src/git_ai_sync/git_operations.py` — `UnsafePathRefusalError` and `MarkerRefusalError` (both `GitError` subclasses; their catches MUST precede `except git_operations.GitError`).
Read `tests/test_main.py` — `_mock_git_ops()` (sets `mock.GitError` and `mock.MarkerRefusalError`), the `_GIT_OPS` / `_CONFIG` / `_ACQUIRE_LOCK` patch constants, `TestCmdSync.test_full_sync`, `TestCmdWatchDispatch` (the watch harness with patched `ChangeTracker` and `time.sleep` `side_effect=[None, KeyboardInterrupt()]`), and the existing `test_sync_marker_refusal_exits` / `test_watch_marker_refusal_exits`.
Read `CHANGELOG.md` — the `## Unreleased` section already exists (added by prompt 1 of this spec); append a bullet to it.
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, restructure the `cmd_watch` sync block. The commit and the pull MUST live in separate `try` blocks so a commit failure cannot skip the pull. OLD (inside `while True:` after the debounce gate):

   ```python
                   logger.info(f"[{iteration}] Checking...")
                   try:
                       # Check for local changes first
                       has_local_changes = git_operations.has_changes(git_repo)

                       # Commit local changes before pulling (pull --rebase requires clean tree)
                       if has_local_changes:
                           changed_files = git_operations.get_changed_files_short(git_repo)
                           logger.info(f"Local changes detected ({len(changed_files)} file(s))")
                           for file_line in changed_files[:5]:
                               logger.info(f"  {file_line}")
                           if len(changed_files) > 5:
                               logger.info(f"  ... and {len(changed_files) - 5} more")

                           git_operations.stage_all(git_repo)
                           commit_msg = git_operations.generate_commit_message(config.commit_prefix)

                           async def run_commit(msg: str = commit_msg) -> None:
                               await conflict_resolver.commit_with_marker_recovery(
                                   git_repo, msg, config.model
                               )

                           asyncio.run(run_commit())
                           logger.info(f"Committed: {commit_msg}")

                       # Pull to get remote changes
                       try:
                           before_pull = git_operations.get_head_commit(git_repo)
                           ... pull ...
                       except git_operations.GitError as e:
                           if "conflicts" in str(e).lower():
                               ... sys.exit(1)
                           raise

                       if not has_local_changes:
                           ... push/last_success branches ...
                           continue

                       # Push to remote
                       git_operations.push(git_repo)
                       ...
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

   NEW structure — preserve the existing pull body, the conflict sub-handler, the `has_local_changes` push branches, and the metrics calls verbatim; only the commit/pull framing changes:

   ```python
                   logger.info(f"[{iteration}] Checking...")
                   has_local_changes = False
                   try:
                       # Check for local changes first
                       has_local_changes = git_operations.has_changes(git_repo)

                       # Commit local changes before pulling (pull --rebase requires clean tree)
                       if has_local_changes:
                           changed_files = git_operations.get_changed_files_short(git_repo)
                           logger.info(f"Local changes detected ({len(changed_files)} file(s))")
                           for file_line in changed_files[:5]:
                               logger.info(f"  {file_line}")
                           if len(changed_files) > 5:
                               logger.info(f"  ... and {len(changed_files) - 5} more")

                           git_operations.stage_all(git_repo)
                           commit_msg = git_operations.generate_commit_message(config.commit_prefix)

                           async def run_commit(msg: str = commit_msg) -> None:
                               await conflict_resolver.commit_with_marker_recovery(
                                   git_repo, msg, config.model
                               )

                           asyncio.run(run_commit())
                           logger.info(f"Committed: {commit_msg}")

                   except git_operations.MarkerRefusalError as e:
                       # A marker refusal is fatal in watch mode (spec-002): exit non-zero.
                       logger.error(f"Sync failed: {e}")
                       sys.exit(1)

                   except git_operations.UnsafePathRefusalError as e:
                       # A commit that could not be made must NOT skip the pull: log
                       # loudly (naming the offending paths) and fall through to pull.
                       logger.error(f"Commit refused; pulling anyway: {e}")

                   except git_operations.GitError as e:
                       logger.error(f"Sync failed: {e}")

                   except Exception as e:
                       logger.exception(f"Unexpected error: {e}")
                       logger.info("Continuing to watch...")

                   # Pull to get remote changes — ALWAYS runs, even when the commit above failed.
                   try:
                       before_pull = git_operations.get_head_commit(git_repo)
                       if args.strategy == "merge":
                           git_operations.pull_merge(git_repo)
                       else:
                           git_operations.pull_rebase(git_repo)
                       after_pull = git_operations.get_head_commit(git_repo)

                       if before_pull != after_pull:
                           commit_count = git_operations.get_commit_count(
                               git_repo, before_pull, after_pull
                           )
                           commits = git_operations.get_commit_log(git_repo, before_pull, after_pull)
                           logger.info(f"Pulled {commit_count} commit(s) from remote")
                           for commit_line in commits[:3]:
                               logger.info(f"  {commit_line}")
                           if len(commits) > 3:
                               logger.info(f"  ... and {len(commits) - 3} more")
                       else:
                           logger.info("No new commits from remote")

                   except git_operations.GitError as e:
                       if "conflicts" in str(e).lower():
                           logger.error(f"Conflicts detected: {e}")
                           logger.error(f"Run 'git-ai-sync resolve {git_repo}' to resolve")
                           sys.exit(1)
                       logger.error(f"Sync failed: {e}")
                       logger.info("Continuing to watch...")
                       continue

                   except Exception as e:
                       logger.exception(f"Unexpected error: {e}")
                       logger.info("Continuing to watch...")
                       continue

                   if not has_local_changes:
                       if git_operations.is_ahead_of_remote(git_repo):
                           logger.info("Local branch is ahead of remote, pushing...")
                           git_operations.push(git_repo)
                           logger.info("Pushed to remote")
                           if config.pushgateway_url:
                               metrics.push_last_success(...)
                       else:
                           logger.info("No local changes")
                           if config.pushgateway_url:
                               metrics.push_last_success(...)
                       continue

                   try:
                       git_operations.push(git_repo)
                       logger.info("Pushed to remote")
                       if config.pushgateway_url:
                           metrics.push_last_success(...)
                   except git_operations.GitError as e:
                       logger.error(f"Sync failed: {e}")
                       logger.info("Continuing to watch...")

                   except Exception as e:
                       logger.exception(f"Unexpected error: {e}")
                       logger.info("Continuing to watch...")
   ```

   Keep the `metrics.push_last_success(...)` argument lists exactly as they are today (URL, username, password, `git_repo`).

   The trailing `except Exception` catch-all is NOT optional — the OLD block had it, and dropping it would let any non-`GitError` escape `while True` and kill the watch process. That is the same "silently stops syncing" class this spec exists to fix, and no existing test exercises it, so `make precommit` would stay green while the regression shipped.

   Behavior the tests pin:
   - A marker refusal still `sys.exit(1)`s and never logs `Continuing to watch...` (the existing `test_watch_marker_refusal_exits` must pass unchanged).
   - An unsafe-path refusal logs an ERROR that names the offending path, then the pull runs — never a bare `Sync failed: Failed to commit:` with an empty tail.
   - Watch continues to the next interval after any refusal (the loop does not exit, and does not tight-loop: the next `time.sleep(interval)` is what paces it).
   - The push is now guarded by its own `try`; a push failure logs `Sync failed: {e}` and `Continuing to watch...` and does not abort the loop.
   - An unexpected non-`GitError` anywhere in the cycle is still caught by the trailing `except Exception` and logged as `Unexpected error`, and the loop continues.
   - A generic `GitError` from the commit `try` (e.g. `stage_all` fails) now falls through to the pull and the push rather than aborting the cycle — this is intended, consistent with the pull guarantee.
   - `has_local_changes` is initialised to `False` before the `try`; if `git_operations.has_changes()` itself raises, the cycle proceeds to the pull and then takes the `if not has_local_changes:` branch. That is acceptable — do not add special handling for it.

2. In `src/git_ai_sync/__main__.py`, restructure `cmd_sync` steps 4-6 so the pull runs even when the commit was refused. OLD:

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

       # 5. Pull with strategy
       ...
   ```

   NEW:

   ```python
       # 4. Commit with auto-generated message (with marker + unsafe-path recovery)
       commit_msg = git_operations.generate_commit_message(config.commit_prefix)
       logger.info(f"Committing: {commit_msg}")

       async def run_commit() -> None:
           await conflict_resolver.commit_with_marker_recovery(git_repo, commit_msg, config.model)

       sync_exit_code = 0
       try:
           asyncio.run(run_commit())
           logger.info("Committed")
       except git_operations.MarkerRefusalError as e:
           # Marker refusal is fatal (spec-002): still pull, but exit non-zero.
           logger.error(f"Failed to commit: {e}")
           sync_exit_code = 1
       except git_operations.UnsafePathRefusalError as e:
           # A commit that could not be made must NOT skip the pull: pull first,
           # then exit non-zero with the offending paths named.
           logger.error(f"Failed to commit; pulling anyway: {e}")
           sync_exit_code = 1
       except git_operations.GitError as e:
           logger.error(f"Failed to commit: {e}")
           sync_exit_code = 1

       # 5. Pull with strategy — ALWAYS runs, even when the commit above failed.
       <existing step-5 pull block, unchanged except that its conflict branch keeps sys.exit(1)>

       # 6. Push to remote — unchanged.

       if sync_exit_code != 0:
           sys.exit(sync_exit_code)

       logger.info(f"Sync completed: {git_repo}")
   ```

   So a recovered-and-pulled sync exits 0; an unrecoverable refusal (paths still offending after 3 attempts) still pulls, then exits 1; a marker refusal still pulls, then exits 1; a pull conflict still `sys.exit(1)`s from the pull handler as today.

3. Update `tests/test_main.py`:

   a. Add `UnsafePathRefusalError` to the import: `from git_ai_sync.git_operations import GitError, MarkerRefusalError, UnsafePathRefusalError`.
   b. Update `_mock_git_ops()` so the mock exposes the real `UnsafePathRefusalError` too (the command handlers resolve `git_operations.UnsafePathRefusalError` through the patched module):

      ```python
      def _mock_git_ops() -> MagicMock:
          """Create mock git_operations with real GitError subclasses attached."""
          mock = MagicMock()
          mock.GitError = GitError
          mock.MarkerRefusalError = MarkerRefusalError
          mock.UnsafePathRefusalError = UnsafePathRefusalError
          return mock
      ```
   c. Add a module-level `test_watch_pulls_when_commit_refused_unrecovered` (AC 6, unrecoverable sub-scenario) mirroring the `TestCmdWatchDispatch` harness (locally import `cmd_watch` and `ChangeTracker` inside the function): args `argparse.Namespace(path=".", interval=30, strategy="merge")`; `mock_git = _mock_git_ops()`; `find_git_repo.return_value = Path("/repo")`, `has_changes.return_value = True`, `generate_commit_message.return_value = "auto: 2026-01-01"`; patch `_GIT_OPS`, `_CONFIG` (set `pushgateway_url = None`), `_ACQUIRE_LOCK`, `ChangeTracker.start`/`stop`/`get_seconds_since_last_change` (return 999), `time.sleep` `side_effect=[None, KeyboardInterrupt()]`; patch `git_ai_sync.conflict_resolver.commit_with_marker_recovery` with `AsyncMock(side_effect=UnsafePathRefusalError("Commit refused by pre-commit hook after 3 attempts; still-offending paths: unsafe?.md. Rename them to Windows-safe names and commit again"))`. Assert: `mock_git.pull_merge.assert_called_once()` (the pull ran despite the refusal); `mock_git.push.assert_called_once()` (the cycle still pushed); no caplog record matches `^Sync failed: Failed to commit:\s*$` with `re.MULTILINE`; at least one record contains `unsafe?.md`.
   d. Add a module-level `test_sync_pulls_when_commit_refused_recovered` (AC 9, recovered-and-pulled exits 0). This one MUST drive the real recovery so the exit code is genuinely 0 — do NOT mock `commit_with_marker_recovery`. Build a real repo in `tmp_path` with a bare remote at `tmp_path / "remote.git"`, a seeded `clean.md` commit pushed to `origin/master`, an `unsafe?.md` staged locally, and the remote advanced by one commit the local checkout does not have. Set `core.hooksPath` to a non-existent dir. Patch `"subprocess.run"` with a wrapper that refuses the FIRST `git commit` and delegates every other call to the real `subprocess.run`. The refusing branch returns this verbatim hook block, inlined so this prompt is self-contained:

      ```python
      BLOCKED_STDERR = (
          "\n"
          "✋ Blocked: staged path is not safe on Windows\n"
          "\n"
          "     unsafe?.md  —  contains one of  : ? * \" < > |\n"
          "\n"
          "   Windows rejects these characters in a path component: : ? * \" < > |\n"
          "   It also rejects a component ending in a dot or a space, and a name\n"
          "   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.\n"
      )
      ```

      (the reason string carries two spaces after `of` — that is the hook's own text, `~/.git-hooks/pre-commit:137`). Patch `_CONFIG` (set `pushgateway_url = None`) and `_ACQUIRE_LOCK`; do NOT patch `_GIT_OPS` (the command must use the real git operations). Run `cmd_sync(argparse.Namespace(command="sync", path=str(repo), strategy="merge"))`. Assert: no `SystemExit` (or `exc_info.value.code == 0` if you wrap it); `git -C <repo> rev-list --count HEAD..origin/master == 0` (the pull landed the remote commit). Do NOT assert `rev-parse HEAD == rev-parse origin/master`: the recovered local commit makes the branch diverged, so the merge pull produces a merge commit and the two SHAs differ; `git -C <repo> ls-tree -r HEAD --name-only` contains the sanitized name (derive it by calling `sanitize_path("unsafe?.md")` rather than hard-coding — it is `unsafe.md` under the corrected rule set, since the stem's trailing dash is trimmed); the captured log contains the offending path. Capture with `caplog.at_level("INFO")` — the recovered path logs the offending path only at INFO (`Renamed unsafe path: …` / `Unsafe-path refusal detected; renaming …`); there is no ERROR line on the recovered path, so the default WARNING root level would make this assertion fail.
   e. Add a module-level `test_watch_continues_after_refusal` (AC 8). Mirror the harness with `time.sleep` `side_effect=[None, None, KeyboardInterrupt()]` so TWO cycles run, and make `commit_with_marker_recovery` an `AsyncMock(side_effect=UnsafePathRefusalError(...))`. Assert: the captured log contains `[2] Checking...`; `mock_git.get_head_commit.call_count == 4` (two per cycle — `before_pull` + `after_pull` — across two cycles; a tight retry loop would give an unbounded count, so an exact 4 proves the interval elapsed and cycle 2 re-evaluated `git rev-parse HEAD`); `mock_git.pull_merge.call_count == 2` (a pull in each cycle); no `SystemExit` raised.
   f. Add a module-level `test_watch_pulls_when_commit_refused_recovered` (AC 6, the recoverable sub-scenario — the one that proves a real commit actually lands in watch mode; without it, AC 6's "the cycle landed a real commit whose tree contains the sanitized path" clause is never exercised in watch mode, because 3c mocks the recovery away). Mirror requirement 3d's real-recovery harness exactly — real repo in `tmp_path`, bare remote at `tmp_path / "remote.git"`, seeded `clean.md` pushed to `origin/master`, `unsafe?.md` staged locally, the remote advanced by one commit the local checkout lacks, `core.hooksPath` pointed at a non-existent dir, `"subprocess.run"` patched with the same first-commit-refusing wrapper, `_GIT_OPS` NOT patched — but drive `cmd_watch` rather than `cmd_sync` — its args are `argparse.Namespace(path=str(repo), interval=30, strategy="merge")` (`cmd_watch` reads `args.interval`, which `cmd_sync` does not take) — and patch `_CONFIG` (`pushgateway_url = None`), `_ACQUIRE_LOCK`, `ChangeTracker.start`/`stop`/`get_seconds_since_last_change` (return 999), and `time.sleep` with `side_effect=[None, KeyboardInterrupt()]`, wrapping the call in `with contextlib.suppress(KeyboardInterrupt):` so the terminating interrupt does not error the test. Assert: `git -C <repo> ls-tree -r HEAD --name-only` contains the sanitized name (derived via `sanitize_path("unsafe?.md")`); `git -C <repo> rev-list --count HEAD..origin/master == 0` (the pull landed the remote commit); and the captured log, taken with `caplog.at_level("INFO")`, names the offending path (the recovered path logs it only at INFO — there is no ERROR line on this path).
</requirements>

<constraints>
- The pull MUST run on every cycle in which a pull is possible — including cycles whose commit was refused (this is the load-bearing guarantee)
- Watch mode: a marker refusal still `sys.exit(1)`s and never logs `Continuing to watch...`; an unsafe-path refusal logs the offending paths and continues to the next interval
- Sync mode: a recovered-and-pulled cycle exits 0; an unrecoverable refusal still pulls then exits non-zero; a marker refusal still pulls then exits non-zero; a pull conflict still exits non-zero
- The `UnsafePathRefusalError` and `MarkerRefusalError` catches MUST precede `except git_operations.GitError` (both are subclasses)
- Do NOT change pull/push strategy (`--rebase` vs `--merge`), the retry interval, or the watch-loop machinery
- Do NOT weaken, bypass, or extend the global pre-commit hook
- Preserve existing error-handling shape; follow the local-import style already used in `cmd_sync`/`cmd_watch`
- Type hints on all new functions (mypy strict); pytest conventions only (`asyncio_mode = "auto"`); no `unittest`
- All new tests are module-level functions (node ids must match the spec's AC evidence paths: `test_watch_pulls_when_commit_refused_unrecovered`, `test_sync_pulls_when_commit_refused_recovered`, `test_watch_continues_after_refusal`)
- Append a `fix:` bullet to the existing `## Unreleased` section in `CHANGELOG.md` (do not create a second `## Unreleased`):

  ```
  - fix: Never skip the pull when a commit is refused — in watch and sync, a commit failure (unsafe path or conflict marker) no longer suppresses the pull, so the local checkout can no longer silently fall behind `origin`; sync pulls then exits non-zero only when the refusal was unrecoverable, and watch continues to the next interval
  ```
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the acceptance-criteria tests pass:
`uv run pytest tests/test_main.py -k 'watch_pulls_when_commit_refused or sync_pulls_when_commit_refused or watch_continues_after_refusal' -v`
Confirm the existing marker-refusal exits still pass:
`uv run pytest tests/test_main.py -k 'marker_refusal' -v`
Confirm the full main test module:
`uv run pytest tests/test_main.py -v`
</verification>
