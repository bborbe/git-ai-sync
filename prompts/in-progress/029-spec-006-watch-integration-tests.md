---
status: approved
spec: [006-watch-quiet-window-decay]
created: "2026-10-23T16:00:34Z"
queued: "2026-10-23T16:09:35Z"
---

# Prove the bounded sync from the production watch loop

<summary>
- The production watch loop is proven to force a sync at the first cycle boundary at or after the maximum wait, for a bound that is and is not a multiple of the interval
- A bound configured below the interval is proven to force a sync on every cycle, with a startup warning that says the debounce is effectively disabled
- The configured bound is proven to reach the loop through the environment rather than through a hardcoded number
- A successful sync is proven to reset the decay, so the cycle after it skips again when the vault is being written
- A skipped cycle is proven to report the window actually in force, not the starting interval
- A quiet vault is proven to sync on exactly the cycle it syncs on today, with no cap message
- Every existing watch assertion survives unchanged, so a regression in quiet-vault behaviour cannot hide behind a passing suite
- No test sleeps on the real clock — every scenario advances an injected clock
</summary>

<objective>
Add the watch-loop integration tests to `tests/test_main.py` that prove the decaying window and the maximum-wait cap through `cmd_watch` itself — not through the decision unit — and prove that a quiet vault is unchanged. **Why:** a correct decision unit that is never consulted, or a cap that is read but never acted on, passes every unit test while the production stall continues; only a test driving `cmd_watch` can tell the two apart.
</objective>

<context>
Read `CLAUDE.md` for project conventions (Python 3.14, `uv`, ruff line-length 100, mypy **strict**, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow.

Read `src/git_ai_sync/__main__.py` — `cmd_watch` as wired by the previous prompt: the keyword-only `clock` parameter, `max_wait = float(config.max_wait)`, the below-interval startup warning containing `debounce is effectively disabled`, the `QuietWindowTracker` created with `clock()`, the per-cycle `now = clock()` → `decision = window_tracker.decide(...)` gate, the skip log line ending in `quiet window {decision.window:.1f}s`, the cap log line `Maximum wait {max_wait:.0f}s reached — syncing regardless of quiet` (emitted only when the cap, not the quiet condition, made the decision), and `window_tracker.record_success(now)` on the success branch only.

Read `src/git_ai_sync/quiet_window.py` — `QuietWindowTracker` and `GateDecision`; you do not test them here, the previous prompts already do.

Read `tests/test_main.py` in full — `_mock_git_ops()`, `_configure_watch_config(mock_config, *, stall_threshold, attention_script, attention_store_url, pushgateway_url, max_wait)`, the `_GIT_OPS` / `_CONFIG` / `_ACQUIRE_LOCK` patch constants, `_watch_sleep(cycles)`, `test_watch_quiet_window_clock_seam` (added by the previous prompt — the harness shape you must copy), `test_watch_heartbeat_on_debounce_skip_cycle`, `test_watch_skip_is_neutral`, and `test_watch_exits_when_threshold_below_two` (the real-`Config` + `monkeypatch.setenv` style you must copy for the environment-driven case).

Read `tests/conftest.py` — the `stub_notifier` and `recording_store` fixtures exist but are NOT needed here: leave `attention_script` at the non-existent default path so the notifier is disabled and cannot interfere with log assertions.
</context>

<requirements>
1. Add these tests to `tests/test_main.py` as module-level functions. Every name contains `quiet_window`, because the spec's selector is `-k quiet_window`. Each test drives `cmd_watch` directly with `clock=<MagicMock(side_effect=[...])>`, so the list has exactly one entry for the process-start reading plus one per cycle — an extra or missing `clock()` call must fail the test with `StopIteration` rather than silently pass.

   The shared harness for every test in this prompt (copy `test_watch_quiet_window_clock_seam`):
   - `args = argparse.Namespace(path=".", interval=30, strategy="merge")` (use the interval the test names);
   - `mock_git = _mock_git_ops()` with `find_git_repo.return_value = Path("/repo")`;
   - patch `_GIT_OPS`, `_ACQUIRE_LOCK`, `ChangeTracker.start`, `ChangeTracker.stop`;
   - patch `ChangeTracker.get_seconds_since_last_change` with the change stream the test names;
   - patch `"time.sleep"` with `side_effect=[None] * cycles + [KeyboardInterrupt()]`, and wrap the `cmd_watch(...)` call in `contextlib.suppress(KeyboardInterrupt)`;
   - patch `git_ai_sync.conflict_resolver.commit_with_marker_recovery` with `AsyncMock()` wherever `has_changes.return_value = True`;
   - take `caplog` with `caplog.at_level("INFO")` (the skip and cap records are INFO; the below-interval warning is WARNING);
   - never call `time.monotonic`, never sleep, never use `_watch_sleep`.

   a. `test_quiet_window_cap_binds_at_second_boundary` — `interval=30`, `max_wait=60` via `_configure_watch_config(mock_config, pushgateway_url=None, max_wait=60)`, `clock=MagicMock(side_effect=[0.0, 30.0, 60.0])`, `get_seconds_since_last_change.return_value = 1.0` (a change stream that never goes quiet), `has_changes.return_value = True`, `generate_commit_message.return_value = "auto: 2026-01-01"`, `is_ahead_of_remote.return_value = False`, two cycles. Assert: `mock_git.pull_merge.call_count == 1` and `mock_git.push.call_count == 1` (the sync ran exactly once, on the second boundary); `"[2] Checking..."` is in the log and `"[1] Checking..."` is not; a record contains `Maximum wait 60` — the cap names the configured maximum wait, not the interval. A hardcoded "every second cycle" implementation must pass this test and fail (b).

   b. `test_quiet_window_cap_binds_at_third_boundary` — same shape with `max_wait=75`, `clock=MagicMock(side_effect=[0.0, 30.0, 60.0, 90.0])`, three cycles. Assert: `mock_git.pull_merge.call_count == 1`; `"[3] Checking..."` is in the log while `"[2] Checking..."` and `"[1] Checking..."` are not; a record contains `Maximum wait 75`. This is the case a hardcoded two-cycle boundary cannot satisfy.

   c. `test_quiet_window_env_max_wait_forces_every_cycle` — drive the setting through the REAL environment, not the mock: `monkeypatch.setenv("GIT_AI_SYNC_MAX_WAIT", "10")`, and `monkeypatch.delenv(..., raising=False)` for `GIT_AI_SYNC_STALL_THRESHOLD`, `GIT_AI_SYNC_PUSHGATEWAY_URL`, `GIT_AI_SYNC_PUSHGATEWAY_USERNAME`, `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD`, and `GIT_AI_SYNC_INTERVAL` so a host value cannot change the outcome. Do NOT patch `_CONFIG`; do NOT call `_configure_watch_config`. `interval=30` through the args, `clock=MagicMock(side_effect=[0.0, 30.0, 60.0])`, `get_seconds_since_last_change.return_value = 1.0`, `has_changes.return_value = True`, two cycles. Assert: `mock_git.pull_merge.call_count == 2` and `mock_git.push.call_count == 2` — a bound below the interval forces every cycle; the log contains both `"[1] Checking..."` and `"[2] Checking..."`; and at least one WARNING record contains `debounce is effectively disabled`. This pins that the value reaches the loop from the environment and is honoured rather than rewritten.

   d. `test_quiet_window_resets_after_success` — `interval=30`, `max_wait=300`, `clock=MagicMock(side_effect=[0.0, 30.0, 60.0])`, `get_seconds_since_last_change` with `side_effect=[999, 5]` (the first cycle sees a quiet vault, the second sees a change 5 s old), `has_changes.return_value = True`, `is_ahead_of_remote.return_value = False`, two cycles. Assert: `mock_git.pull_merge.call_count == 1` and `mock_git.push.call_count == 1` (only the first cycle synced); `"[2] Checking..."` is not in the log; the record whose message starts with `[2] Skipping` contains `30` — the window is back to the full interval, proving the successful first cycle reset the decay. An implementation that never resets would have a 15 s window at the second cycle and would accept the 5 s change, failing this test.

   e. `test_quiet_window_skip_log_reports_decayed_window` — `interval=30`, `max_wait=300`, `clock=MagicMock(side_effect=[0.0, 30.0, 60.0])`, `get_seconds_since_last_change.return_value = 5.0`, `has_changes.return_value = False`, `is_ahead_of_remote.return_value = False`, two cycles, both skipped. Assert: `mock_git.pull_merge.assert_not_called()`; the record starting with `[1] Skipping` contains `30` (the starting window) and the record starting with `[2] Skipping` contains `15` (the halved window). A constant-window implementation logs `30` on both cycles and fails on the second assertion.

   f. `test_quiet_window_quiet_vault_unchanged` — `interval=30`, `max_wait=300`, `clock=MagicMock(side_effect=[0.0, 30.0])`, `get_seconds_since_last_change.return_value = 999`, `has_changes.return_value = False`, `is_ahead_of_remote.return_value = False`, one cycle. Assert: `mock_git.pull_merge.assert_called_once()`; the log contains `"[1] Checking..."`; no record contains `Maximum wait` (the cap did not fire on a quiet vault); no record contains `Skipping`. This is the regression guard for "a quiet vault behaves exactly as it does today".

2. Keep every existing assertion in `tests/test_main.py` intact. `test_watch_heartbeat_on_debounce_skip_cycle` and `test_watch_skip_is_neutral` must still pass unchanged, still observing a skip at zero skips with `interval = 30` — do not edit them, do not relax them, do not rename them.

3. If any of the new tests exposes a genuine defect in the wiring, fix the production code in `src/git_ai_sync/__main__.py` in this prompt and say so in your final summary — do not weaken or delete the test to make it pass. If a test cannot be made to pass without changing behaviour the spec fixes, stop and report the conflict instead of adjusting the assertion.
</requirements>

<constraints>
- Python 3.14, `uv`, ruff (line-length 100), mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- `docs/dod.md` applies in full: type hints on every test signature, docstrings where the surrounding tests have them, no unexplained `# type: ignore` or `noqa`.
- This prompt is test-only. The single permitted production change is the defect fix allowed by requirement 3.
- The new tests carry `quiet_window` in their names so the spec's `-k quiet_window` selector collects them.
- No test may sleep on the real clock: every scenario injects a clock, and every `time.sleep` is patched.
- Do NOT remove or weaken any existing assertion. The acceptance criterion checks that no assertion line was deleted from `tests/test_main.py` relative to `origin/master`.
- Do NOT change the `interval` semantics for a quiet vault, the pull strategy, the consecutive-failure counter, the stall-attention card, the pushgateway metrics, the pre-commit hook, or the `sync` / `resolve` / `status` / `doctor` / `version` subcommands.
- Do NOT touch `src/git_ai_sync/quiet_window.py`, `src/git_ai_sync/config.py`, `src/git_ai_sync/launchd.py`, `README.md`, `CLAUDE.md`, `docs/`, or `CHANGELOG.md`.
- Do NOT add a second `## Unreleased` section to `CHANGELOG.md` or any changelog bullet — the change already carries one.
- Existing tests must still pass.
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new integration tests pass:
`uv run pytest tests/test_main.py -q -k quiet_window -v`
Confirm the spec's combined selector for the window, gate and watch tests:
`uv run pytest tests/ -q -k "decay or window or quiet_window"`
Confirm the whole main test module passes, including every pre-existing watch test:
`uv run pytest tests/test_main.py -v`
Confirm the quiet-vault regression guards are still present and unedited:
`grep -c 'def test_watch_heartbeat_on_debounce_skip_cycle' tests/test_main.py` — must print `1`
`grep -c 'def test_watch_skip_is_neutral' tests/test_main.py` — must print `1`
Confirm `tests/test_main.py` really changed, so the assertion-removal probe below cannot pass vacuously:
`git rev-parse --verify origin/master`
`test -n "$(git diff origin/master -- tests/test_main.py)"`
Confirm no assertion line was removed from `tests/test_main.py`:
`git diff origin/master -- tests/test_main.py | grep -cE '^-[^-].*assert' || true` — must print `0`
Confirm the whole suite passes:
`uv run pytest tests/ -q`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change — the two cap boundaries, the environment-driven bound, the post-success skip, the decayed skip log, and the unchanged quiet vault.
</verification>
