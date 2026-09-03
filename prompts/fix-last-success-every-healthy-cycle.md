---
spec: ["002-push-sync-health-metrics"]
status: draft
created: "2026-09-03"
---

<summary>
- `git_ai_sync_last_success_timestamp` is now pushed on EVERY watch cycle that completes without error — including cycles with no local changes and nothing to push
- Previously it was pushed only after an actual `git push`, so a healthy-but-idle vault either never got a last-success series (alert can't fire on it when it later wedges) or went quiet >1h and false-fired the sync-stall alert
- Debounce-skip cycles and failed cycles still push nothing (they are not healthy syncs)
- Semantics now match the `/watch` probe: "No local changes" counts as a successful cycle
- Regression tests pin the new no-op-cycle push and the unchanged skip/failure behavior
</summary>

<objective>
Make `git_ai_sync_last_success_timestamp` mean "last healthy watch cycle" instead of "last push", so the age-of-last-success alert fires only when a vault genuinely stops completing cycles — not when it is idle-but-healthy.
</objective>

<context>
Read CLAUDE.md for project conventions.

Read `src/git_ai_sync/__main__.py` — `cmd_watch`'s sync loop. The `if not has_local_changes:` block at ~line 224 has two branches:
- `is_ahead_of_remote` true → `git_operations.push(git_repo)` then `metrics.push_last_success(...)` (lines 226-236)
- else → `logger.info("No local changes")` then `continue` (lines 237-239) — this branch does NOT push last_success; this is the bug

The block after it (lines 241-251) pushes `last_success` after the final `git_operations.push(git_repo)` — that one is correct (a healthy cycle with local changes).

The debounce-skip `continue` (earlier in the loop, `seconds_since_change < interval`) and the two `except` blocks (GitError / Exception) must remain WITHOUT a last_success push — those are not healthy cycles. The `except git_operations.GitError` block with `"conflicts"` in the message does `sys.exit(1)` — that is a wedge exit, no push.

Read `tests/test_main.py` — the `TestCmdWatchDispatch` tests: `test_watch_last_success_after_successful_sync` (d) and `test_watch_last_success_zero_after_failed_sync` (e). These pin the current behavior. Add a new test for the no-local-changes cycle. The patch targets are `git_ai_sync.metrics.push_heartbeat` / `git_ai_sync.metrics.push_last_success` (as established by `test_watch_heartbeat_one_per_cycle`).

Read `CHANGELOG.md` — add a `fix:` bullet under `## Unreleased` (create the section directly above the highest `## vX.Y.Z` heading, currently `## v0.9.1`, if absent).

Read `specs/in-progress/002-push-sync-health-metrics.md` — Desired Behavior #2 currently says last_success is pushed when "push to remote succeeded". Update the wording to "when a watch cycle completes successfully — including idle no-change cycles" so the durable spec contract matches the shipped behavior (the Acceptance Criterion #2 wording "only after a successful sync" already reads consistently).
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, in the `cmd_watch` loop's `if not has_local_changes:` block, change the `else:` branch from:
   ```python
   else:
       logger.info("No local changes")
       continue
   ```
   to:
   ```python
   else:
       logger.info("No local changes")
       if config.pushgateway_url:
           metrics.push_last_success(
               config.pushgateway_url,
               config.pushgateway_username,
               config.pushgateway_password,
               git_repo,
           )
       continue
   ```
   The existing `continue` stays — the loop must not fall through to the post-push block. This makes a no-op healthy cycle push last_success, matching the `/watch` probe's "No local changes counts as success" semantics. Do NOT touch the `is_ahead_of_remote` branch or the post-push block — they already push correctly.

2. In `tests/test_main.py`, add `test_watch_last_success_on_no_local_changes_cycle` — same scaffolding as `test_watch_heartbeat_one_per_cycle` (patch `git_ai_sync.metrics.push_heartbeat` and `git_ai_sync.metrics.push_last_success`; config mock with `pushgateway_url="https://pushgateway.test"`; `has_local_changes=False`; `is_ahead_of_remote=False`; one loop iteration with `sleep_side_effect = [None, KeyboardInterrupt()]` + `contextlib.suppress(KeyboardInterrupt)`) → assert `push_heartbeat.assert_called_once()` AND `push_last_success.assert_called_once()` (both with the URL as first arg and `Path("/repo")` as last), `mock_git.push.assert_not_called()`. This is the exact scenario the `else` branch handles.

3. Update `test_watch_heartbeat_one_per_cycle` in `tests/test_main.py` — its scenario (no local changes, not ahead, pushgateway configured) is now a healthy cycle, so its `mock_last_success.assert_not_called()` (line ~460) MUST become `mock_last_success.assert_called_once()`. If the new test in requirement 2 is kept as a separate case, keep both; if it fully duplicates this test, fold the assertion change into `test_watch_heartbeat_one_per_cycle` and drop the new test — either way both tests must assert `push_last_success` IS called for the no-local-changes cycle.
   Confirm the two negative cases stay pinned: `test_watch_heartbeat_on_debounce_skip_cycle` and `test_watch_last_success_zero_after_failed_sync` — `push_last_success.assert_not_called()` (unchanged; debounce skip and failed cycles are not healthy). Do not modify these two; they must still pass.

4. In `CHANGELOG.md`, under `## Unreleased` (create if absent, directly above `## v0.9.1`), add exactly one bullet:
   - `- fix: git-ai-sync pushes the last-success timestamp on every watch cycle that completes without error, including idle no-change cycles, so the sync-stall alert measures "last healthy cycle" (matching the /watch probe) instead of "last push" and does not false-fire on quiet-but-healthy vaults`

5. Self-check: re-run the `<verification>` block and walk each requirement against the change before finishing.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Type hints on all function signatures, docstrings on public functions, no `print()`
- Do not change anything else in `cmd_watch` (heartbeat placement, pull logic, error handling, conflict exit all stay as-is)
- The debounce-skip and failure paths must NOT push last_success — only the three healthy-cycle outcomes (ahead-push, no-local-changes, local-changes-push)
</constraints>

<verification>
Run `make precommit` — must exit 0 (format + lint + typecheck + full test suite).

Run `uv run pytest tests/test_main.py -k watch -v` — all watch tests pass, including the new `test_watch_last_success_on_no_local_changes_cycle` and the unchanged debounce-skip / failed-sync negative tests.

Run `grep -n 'No local changes' src/git_ai_sync/__main__.py` — returns the line, and the following 6 lines contain `push_last_success`.
</verification>
