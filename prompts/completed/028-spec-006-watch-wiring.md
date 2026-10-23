---
status: completed
spec: [006-watch-quiet-window-decay]
summary: Wired the decaying quiet-window decision and injected monotonic clock into cmd_watch, replacing the flat interval comparison and adding the success reset, cap log, and below-interval warning.
execution_id: git-ai-sync-exec-028-spec-006-watch-wiring
dark-factory-version: v0.198.7
created: "2026-10-23T16:00:34Z"
queued: "2026-10-23T16:09:35Z"
started: "2026-10-23T16:14:11Z"
completed: "2026-10-23T16:16:57Z"
---

# Wire the decaying window and the maximum-wait cap into the watch loop

<summary>
- The watch loop now asks the new decision unit whether a cycle may sync, instead of comparing the change age against a fixed interval
- A busy vault therefore starts syncing as soon as the halved window drops below the observed change age, and is forced to sync once the maximum wait elapses
- A successful sync — including a cycle that finds nothing to commit — puts the window back to the full interval and restarts the wait clock
- A failed cycle never restarts the wait clock, so a failing sync cannot postpone the next forced attempt indefinitely
- A skipped cycle reports the window currently in force, so an operator reading the log can see the decay working
- A cycle forced by the maximum wait says so, and a cycle accepted because the vault went quiet stays silent, so the two mechanisms are distinguishable in the log
- The loop's notion of "now" is injected, so tests can advance time deterministically instead of sleeping
- A quiet vault behaves exactly as it does today, because at zero skips the window equals the interval
- The consecutive-failure counter, the stall card, the pull strategy, and the pushgateway metrics are untouched
</summary>

<objective>
Replace the flat `seconds_since_change < interval` comparison in `cmd_watch` (`src/git_ai_sync/__main__.py`) with the decision from `git_ai_sync.quiet_window`, add the injected clock seam, the two log messages, the success reset, and the below-interval startup warning. **Why:** the flat comparison never releases while an agent fleet writes, so the syncer can go an unbounded time without committing or pulling — the wiring is what turns the bounded decision into bounded behaviour.
</objective>

<context>
Read `CLAUDE.md` for project conventions (Python 3.14, `uv`, ruff line-length 100, mypy **strict**, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates.

Read `src/git_ai_sync/__main__.py` — `cmd_watch` in full, especially:
- the local import block `from git_ai_sync import conflict_resolver, git_operations, metrics, notifier`;
- the startup sequence `config = Config()` → `repo_path` → `interval = float(args.interval)` → `logger.info(f"Starting watch mode: ...")` → the `config.stall_threshold < 2` check → `find_git_repo` → `acquire_lock` → `logger.info(f"Interval: {interval}s (skips if actively editing)")` → the pushgateway block → `attention_script` / `stall_tracker` / `vault` → `tracker = ChangeTracker(git_repo)` / `tracker.start()` → `iteration = 0` → `while True:`;
- the debounce gate at the top of the loop body (the `seconds_since_change < interval` comparison with its `stall_tracker.record_skip()` and `continue`);
- the trailing classification block that ends in `stall_tracker.record_success()` for a successful cycle and `stall_tracker.record_failure()` otherwise.

Read `src/git_ai_sync/quiet_window.py` (created by the previous prompt) — `GateDecision` (`sync`, `window`, `cap_fired`), `QuietWindowTracker(now)` with `consecutive_skips`, `window(interval)`, `elapsed_since_success(now)`, `decide(*, seconds_since_change, interval, max_wait, now)`, `record_skip()`, and `record_success(now)`. Read it before writing the wiring; do not re-implement or second-guess its arithmetic.

Read `src/git_ai_sync/config.py` — the `max_wait: int` field (default `300`) added by the previous prompt.

Read `src/git_ai_sync/file_watcher.py` — `ChangeTracker.get_seconds_since_last_change()` returns `float("inf")` when no change was ever observed; the gate accepts an infinite age, so a vault that was already quiet still syncs on the first cycle.

Read `tests/test_main.py` — `_configure_watch_config` (the helper that sets every `Config` attribute `cmd_watch` reads on a patched `Config` mock), `_mock_git_ops()`, the `_GIT_OPS` / `_CONFIG` / `_ACQUIRE_LOCK` patch constants, `TestCmdWatchDispatch` (the harness: patched `ChangeTracker.start` / `stop` / `get_seconds_since_last_change`, `patch("time.sleep", side_effect=[None, KeyboardInterrupt()])` for one cycle, `contextlib.suppress(KeyboardInterrupt)` around the call), `_watch_sleep(cycles)`, and the debounce-skip tests `test_watch_heartbeat_on_debounce_skip_cycle` and `test_watch_skip_is_neutral`, which must keep asserting a skip at zero skips with `interval = 30`.

Do NOT change `tests/test_main.py`'s existing assertions. The only permitted edits there are the `_configure_watch_config` extension and the one new test named in requirement 9.
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, add `from collections.abc import Callable` to the module-level imports (stdlib `from` imports, before `from typing import NoReturn`). The module already imports `from typing import NoReturn` at the top; keep it.

2. Give `cmd_watch` an optional, keyword-only clock seam. The signature becomes:

   ```python
   def cmd_watch(args: argparse.Namespace, *, clock: Callable[[], float] | None = None) -> None:
       """Start watching and syncing with debounce-gated polling.

       Args:
           args: Parsed CLI arguments (``path``, ``interval``, ``strategy``).
           clock: Monotonic clock supplying the loop's notion of "now"; defaults
               to ``time.monotonic``. Tests inject a fake so time can advance
               without sleeping.
       """
   ```

   `main()` keeps calling `cmd_watch(args)` unchanged. A caller passing no clock must see today's behaviour.

3. In `cmd_watch`, add `quiet_window` to the local import block and bind the clock. The block becomes:

   ```python
       from git_ai_sync import conflict_resolver, git_operations, metrics, notifier, quiet_window
   ```

   and, after the `import ... from pathlib import Path` line, before `config = Config()`:

   ```python
       clock = clock if clock is not None else time.monotonic
   ```

   `time` is already imported inside `cmd_watch`; do not move it to module scope.

4. Resolve and log the effective maximum wait at startup. Immediately after `interval = float(args.interval)`:

   ```python
       max_wait = float(config.max_wait)
   ```

   and immediately after the existing `logger.info(f"Starting watch mode: path={repo_path}, interval={interval}s")` line:

   ```python
       logger.info(f"Maximum wait: {max_wait:.0f}s")
       if max_wait < interval:
           logger.warning(
               f"GIT_AI_SYNC_MAX_WAIT={max_wait:.0f}s is below --interval={interval:.0f}s:"
               " the debounce is effectively disabled — every cycle syncs"
           )
   ```

   The warning text must contain the phrase `debounce is effectively disabled` and must name `GIT_AI_SYNC_MAX_WAIT`. It fires for any value below the effective interval, including `0` and negative values. This is the only place the value is resolved and logged — do not read `config.max_wait` anywhere else, and do not rewrite or clamp the value.

   Also update the later startup line `logger.info(f"Interval: {interval}s (skips if actively editing)")` to name the decay, for example `f"Interval: {interval}s (skips while actively editing; window halves each skip)"`.

5. Create the tracker once, before the loop. Immediately before `iteration = 0`:

   ```python
       window_tracker = quiet_window.QuietWindowTracker(clock())
   ```

   This is the process-start reading of the monotonic clock and the initial last-success timestamp. The clock is called exactly once per cycle thereafter (requirement 6) — no other `clock()` call anywhere in `cmd_watch`.

6. Replace the debounce gate in the `while True:` body. OLD:

   ```python
               # Check if files changed recently (within interval)
               seconds_since_change = tracker.get_seconds_since_last_change()
               if seconds_since_change < interval:
                   logger.info(
                       f"[{iteration}] Skipping - files changed"
                       f" {seconds_since_change:.1f}s ago (still editing)"
                   )
                   stall_tracker.record_skip()
                   continue
   ```

   NEW:

   ```python
               now = clock()
               seconds_since_change = tracker.get_seconds_since_last_change()
               decision = window_tracker.decide(
                   seconds_since_change=seconds_since_change,
                   interval=interval,
                   max_wait=max_wait,
                   now=now,
               )
               if not decision.sync:
                   logger.info(
                       f"[{iteration}] Skipping - files changed"
                       f" {seconds_since_change:.1f}s ago (still editing;"
                       f" quiet window {decision.window:.1f}s)"
                   )
                   window_tracker.record_skip()
                   stall_tracker.record_skip()
                   continue

               if decision.cap_fired and seconds_since_change < decision.window:
                   logger.info(
                       f"[{iteration}] Maximum wait {max_wait:.0f}s reached —"
                       " syncing regardless of quiet"
                   )
   ```

   Invariants this gate must hold:
   - The skip log keeps the exact prefix `Skipping - files changed` and the `{seconds_since_change:.1f}s ago` value — the operator's post-deploy grep depends on that prefix, and the acceptance criterion pins that the record reports the window in force (`quiet window {decision.window:.1f}s` prints `30.0` at zero skips and `15.0` after one).
   - `stall_tracker.record_skip()` stays, unchanged and unconditional on the skip path — the consecutive-failure counter must still treat a debounce skip as neutral.
   - The cap message fires only when the cap is what made the decision, i.e. when the quiet condition did NOT also accept the cycle (`seconds_since_change < decision.window`). A cycle that would have been accepted on quiet must not claim the cap fired.
   - `now` is captured once per cycle, at the top, and is the same value handed to `decide` and later to `record_success`.
   - The `logger.info(f"[{iteration}] Checking...")` line and everything after it (commit → pull → push → classification) stay exactly as they are. The accepted cycle runs the existing sequence unchanged, including the pull the skip path still bypasses.

7. Reset the decay on success. In the trailing classification block, the success branch currently reads:

   ```python
               else:
                   if stall_tracker.outstanding_item_id is not None:
                       notifier.close_stall_card(
                           config.attention_store_url, stall_tracker.outstanding_item_id
                       )
                   stall_tracker.record_success()
   ```

   Add one line after `stall_tracker.record_success()`:

   ```python
                   window_tracker.record_success(now)
   ```

   It goes in the `else:` branch only. The failure branch (`stall_tracker.record_failure()`) must not touch the window tracker, so a failed cycle never restarts the maximum-wait clock.

8. In `tests/test_main.py`, extend `_configure_watch_config` so every existing `cmd_watch` test keeps working — `cmd_watch` now calls `float(config.max_wait)`, and `float()` on a bare `MagicMock` attribute raises `TypeError`:

   ```python
   def _configure_watch_config(
       mock_config: MagicMock,
       *,
       stall_threshold: int = 10,
       attention_script: str = "/nonexistent/attention-ask.py",
       attention_store_url: str = "http://localhost:18080",
       pushgateway_url: str | None = None,
       max_wait: int = 300,
   ) -> MagicMock:
       """Set every Config attribute cmd_watch reads on a patched Config mock."""
       mock_config.return_value.pushgateway_url = pushgateway_url
       mock_config.return_value.stall_threshold = stall_threshold
       mock_config.return_value.attention_script = attention_script
       mock_config.return_value.attention_store_url = attention_store_url
       mock_config.return_value.max_wait = max_wait
       return mock_config
   ```

   The default `300` keeps every existing test on the non-forcing path, because those tests do not inject a clock and the real monotonic elapsed time is effectively zero.

9. Add ONE new test to `tests/test_main.py` proving the seam is used: `test_watch_quiet_window_clock_seam`. Drive it through the `TestCmdWatchDispatch` harness (`_mock_git_ops()` with `find_git_repo` → `Path("/repo")`, `has_changes.return_value = False`, `is_ahead_of_remote.return_value = False`; patch `_GIT_OPS`, `_CONFIG` via `_configure_watch_config(mock_config, pushgateway_url=None)`, `_ACQUIRE_LOCK`, `ChangeTracker.start` / `stop`, `get_seconds_since_last_change` → `999`, and `time.sleep` with `side_effect=[None, KeyboardInterrupt()]`; wrap the call in `contextlib.suppress(KeyboardInterrupt)`). Pass `clock=MagicMock(side_effect=[0.0, 30.0])` as the keyword argument and assert: `mock_git.pull_merge.assert_called_once()`, the log contains `[1] Checking...`, and `clock_mock.call_count == 2` — one process-start reading plus exactly one per cycle. This test pins that the loop reads time only through the injected clock; the behavioural tests for the decay and the cap belong to the next prompt.
</requirements>

<constraints>
- Python 3.14, `uv`, ruff (line-length 100), mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- `docs/dod.md` applies in full: type hints on every function signature, docstrings on public functions, no unexplained `# type: ignore` or `noqa`, no absolute paths.
- The existing `interval` semantics for a quiet vault must not change: at zero skips the window equals the interval, so a vault that goes quiet between cycles syncs on exactly the cycles it syncs on today.
- The watch loop's notion of "now" is supplied by its caller so tests can advance time deterministically; no test may sleep on the real clock, and no production code may call `time.monotonic()` inside `cmd_watch` except through the `clock` binding.
- Do NOT change the pull strategy (`--strategy merge` / `rebase`), the consecutive-failure counter, the stall-attention card, the pushgateway metrics, the pre-commit hook, or the `sync` / `resolve` / `status` / `doctor` / `version` subcommands.
- Do NOT add a setting, a knob, or a metric. `max_wait` already exists; this prompt only reads it.
- Do NOT modify `src/git_ai_sync/quiet_window.py`, `src/git_ai_sync/config.py`, `src/git_ai_sync/launchd.py`, `README.md`, `CLAUDE.md`, `docs/`, or `CHANGELOG.md` — the setting and the docs landed in the previous prompt, and the changelog bullet for this change already covers the wiring.
- Do NOT remove, weaken, or reword any existing assertion in `tests/test_main.py`. `test_watch_heartbeat_on_debounce_skip_cycle` and `test_watch_skip_is_neutral` must still pass with `interval = 30` and still observe a skip at zero skips.
- Existing `time.sleep` patches (`side_effect=[None, KeyboardInterrupt()]` and `_watch_sleep(cycles)`) must keep working — the new logic runs inside the loop body, not around `sleep`.
- Existing tests must still pass.
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new seam test and every existing watch test pass:
`uv run pytest tests/test_main.py -v`
Confirm the loop consumes the decision unit and the injected clock:
`grep -nE 'quiet_window|QuietWindowTracker|window_tracker|max_wait|clock' src/git_ai_sync/__main__.py`
Confirm the flat comparison is gone:
`! grep -q 'seconds_since_change < interval' src/git_ai_sync/__main__.py`
Confirm the skip log still carries the prefix the operator greps for, and now reports the window:
`grep -n 'Skipping - files changed' src/git_ai_sync/__main__.py`
`grep -n 'quiet window' src/git_ai_sync/__main__.py`
Confirm the cap message names the maximum wait:
`grep -n 'Maximum wait' src/git_ai_sync/__main__.py`
Confirm the failure counter is still told about skips, exactly once:
`grep -c 'stall_tracker.record_skip()' src/git_ai_sync/__main__.py` — must print `1`
Confirm the success reset sits on the success path only:
`grep -c 'window_tracker.record_success(now)' src/git_ai_sync/__main__.py` — must print `1`
Confirm the below-interval warning exists and names the consequence:
`grep -n 'debounce is effectively disabled' src/git_ai_sync/__main__.py`
Confirm the clock default is the real monotonic clock:
`grep -n 'time.monotonic' src/git_ai_sync/__main__.py`
Confirm the whole suite passes:
`uv run pytest tests/ -q`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change — in particular that a successful "nothing to commit" cycle resets the decay, and that a failed cycle does not.
</verification>
