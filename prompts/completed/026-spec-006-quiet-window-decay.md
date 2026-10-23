---
status: completed
spec: [006-watch-quiet-window-decay]
summary: Added the pure decaying quiet-window decision unit (decayed_window, GateDecision, QuietWindowTracker) with 16 dedicated tests; make precommit exits 0
execution_id: git-ai-sync-exec-026-spec-006-quiet-window-decay
dark-factory-version: v0.198.7
created: "2026-10-23T16:00:34Z"
queued: "2026-10-23T16:09:35Z"
started: "2026-10-23T16:10:17Z"
completed: "2026-10-23T16:11:58Z"
---

# Add the decaying quiet window and the maximum-wait cap as a pure decision unit

<summary>
- A busy vault's required quiet window now shrinks the longer the syncer has been waiting, instead of staying fixed forever
- The window starts at the poll interval and halves on every consecutive skipped cycle
- A single successful sync puts the window back to the full interval and restarts the wait clock
- A separate maximum wait guarantees a sync even when the vault never goes quiet
- The maximum wait binds at the first cycle boundary at or after it, so a cap that is not a multiple of the interval still produces a predictable, bounded delay
- The decision is a self-contained unit with its own tests, so the decay schedule and the cap arithmetic are provable without running the watch loop
- Nothing in the watch loop changes yet, so a quiet vault's behaviour is untouched by this step
</summary>

<objective>
Create `src/git_ai_sync/quiet_window.py` holding the decay schedule (`decayed_window`) and the per-cycle gate (`QuietWindowTracker.decide`), plus `tests/test_quiet_window.py`. **Why:** today the watch loop compares the newest change age against a single flat `interval`, so a vault written every few seconds never satisfies the gate and the syncer can go an unbounded time without committing or pulling. This prompt builds the bounded decision as a pure unit with its own tests; the next prompt wires it into `cmd_watch`.
</objective>

<context>
Read `CLAUDE.md` for project conventions (Python 3.14, `uv`, ruff line-length 100, mypy **strict**, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates: type hints on every function signature, docstrings on public functions, no unexplained `# type: ignore` / `noqa`, functions over classes for stateless operations, no absolute paths.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then the implementation, then the full suite).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-logging-guide.md` — the module you add takes no logger; the watch loop owns every log line, and this module stays log-free so its behaviour is assertable from return values.

Read `src/git_ai_sync/file_watcher.py` — `ChangeTracker.get_seconds_since_last_change() -> float` is the change-age source the gate consumes. It returns `float("inf")` when no change has ever been observed, so the gate must accept an infinite age. This module does NOT touch `file_watcher.py`.

Read `src/git_ai_sync/notifier.py` — `ConsecutiveFailureTracker` is the existing precedent for a small in-memory tracker class with `record_*` methods. Follow its shape (private attributes, public read accessors). That tracker counts consecutive FAILURES and must not be reused, changed, or coupled to this one.

Read `src/git_ai_sync/__main__.py` — `cmd_watch`'s debounce gate today:

```python
            seconds_since_change = tracker.get_seconds_since_last_change()
            if seconds_since_change < interval:
                logger.info(
                    f"[{iteration}] Skipping - files changed"
                    f" {seconds_since_change:.1f}s ago (still editing)"
                )
                stall_tracker.record_skip()
                continue
```

This prompt only defines the replacement decision; `cmd_watch` is not modified here.

Read `tests/test_file_watcher.py` and `tests/test_notifier.py` for the module-test style used in this repo (plain pytest functions/classes, no `unittest`, no real sleeping).
</context>

<requirements>
1. Create `src/git_ai_sync/quiet_window.py` with exactly this public surface. Module docstring first, then `from dataclasses import dataclass`.

   a. A frozen value object for one cycle's verdict:

   ```python
   @dataclass(frozen=True)
   class GateDecision:
       """One watch cycle's debounce verdict.

       Attributes:
           sync: Whether the cycle should run the sync sequence.
           window: The quiet window in force for this cycle, in seconds.
           cap_fired: Whether the decision was forced by the maximum wait.
       """

       sync: bool
       window: float
       cap_fired: bool
   ```

   b. The decay schedule as a pure function:

   ```python
   def decayed_window(interval: float, consecutive_skips: int) -> float:
       """Return the quiet window required after ``consecutive_skips`` skips.

       The window starts at ``interval`` and halves on every consecutive skip,
       so a vault that never goes quiet still becomes eligible to sync.

       Args:
           interval: The configured poll interval, in seconds.
           consecutive_skips: Consecutive skipped cycles since the last success.

       Returns:
           The required quiet window, in seconds.
       """
       return interval * (0.5**consecutive_skips)
   ```

   The multiplication form is required, not `interval / (2**consecutive_skips)`: the division form raises `OverflowError` once `consecutive_skips` is large enough that `2**n` cannot be converted to a float, while `0.5**n` simply underflows to `0.0`. Do not clamp, round, or floor the result — at `interval = 32` the schedule must be exactly `32.0, 16.0, 8.0, 4.0, 2.0` for `n = 0..4`, and powers of two and their halves are exact in binary floating point.

   c. The per-cycle gate, holding only the two pieces of state the spec allows (an in-memory skip counter and a monotonic last-success timestamp):

   ```python
   class QuietWindowTracker:
       """Tracks consecutive skipped cycles and the last successful sync.

       The quiet window halves on every consecutive skip and returns to the
       full interval after any successful sync. The elapsed clock is monotonic
       and restarts on each success, so a failed cycle never resets it.
       """

       def __init__(self, now: float) -> None:
           """Start the tracker with a zero skip count and the current clock.

           Args:
               now: The caller's monotonic clock reading at process start.
           """

       @property
       def consecutive_skips(self) -> int:
           """Consecutive skipped cycles since the last successful sync."""

       def window(self, interval: float) -> float:
           """The quiet window in force, in seconds."""

       def elapsed_since_success(self, now: float) -> float:
           """Seconds of monotonic time since the last successful sync."""

       def decide(
           self,
           *,
           seconds_since_change: float,
           interval: float,
           max_wait: float,
           now: float,
       ) -> GateDecision:
           """Decide whether this cycle may sync.

           The cycle is accepted when the newest change is older than the
           window in force, or when the maximum wait has elapsed since the last
           successful sync — whichever happens first.
           """

       def record_skip(self) -> None:
           """Count one skipped cycle, halving the window for the next one."""

       def record_success(self, now: float) -> None:
           """Reset the decay and restart the maximum-wait clock."""
   ```

   `decide` computes `window = self.window(interval)`, `cap_fired = self.elapsed_since_success(now) >= max_wait`, and returns `GateDecision(sync=cap_fired or seconds_since_change >= window, window=window, cap_fired=cap_fired)`. `window(interval)` delegates to `decayed_window(interval, self.consecutive_skips)` — one implementation of the schedule, never a second copy. `record_success` sets the skip count to `0` and the last-success timestamp to `now`.

   The code blocks above state the contract, not the finished bodies: implement every body so it
   satisfies its docstring and the rules below. A docstring-only stub returns `None` and fails
   mypy strict on the annotated return types.

   Invariants this surface must hold:
   - `cap_fired` is `elapsed_since_success(now) >= max_wait`, a plain `>=` at the cycle boundary. Do not add an epsilon, a rounding step, or an `interval`-based adjustment — the caller supplies the boundary-aligned `now`.
   - A `max_wait` of `0` or negative makes `cap_fired` true on the first cycle (`elapsed >= 0` always holds), which is the spec's intended "always force" behaviour.
   - An infinite `seconds_since_change` (no change ever observed) is accepted by the decay condition, preserving today's behaviour for a vault that was already quiet.
   - `decide` is a pure read: it must not mutate the tracker. Only `record_skip` and `record_success` mutate state.
   - The module imports nothing from `git_ai_sync` and takes no logger, no clock, and no config — every input arrives as a parameter.

2. Create `tests/test_quiet_window.py` covering the schedule, the gate, and the reset. Every test name must contain `window` or `decay` — the spec's verification selectors are `-k "decay or window"`. Import from `git_ai_sync.quiet_window` (`GateDecision`, `QuietWindowTracker`, `decayed_window`). Use only the injected `now` values — never `time.monotonic()`, never `time.sleep`, never a patched clock.

   a. `test_decayed_window_halves_per_skip` — for `interval = 32`, `consecutive_skips` `0, 1, 2, 3, 4` give exactly `32.0, 16.0, 8.0, 4.0, 2.0` (compare with `==` on the floats; use a parametrized table or a loop over the expected list). Also assert `decayed_window(32, 0) == 32.0`, pinning that the window starts at the interval.

   b. `test_decayed_window_tiny_after_many_skips` — `decayed_window(32, 20) < 0.001`. This is the acceptance criterion's "the window collapses after a long stall" clause.

   c. `test_quiet_window_resets_after_success` — build `QuietWindowTracker(now=0.0)`, call `record_skip()` three times, assert `tracker.window(32) == 4.0` and `tracker.consecutive_skips == 3`; then `record_success(now=90.0)` and assert `tracker.window(32) == 32.0`, `tracker.consecutive_skips == 0`, and `tracker.elapsed_since_success(90.0) == 0.0`. This pins the acceptance criterion's "back to 32 after a completed cycle".

   d. `test_window_gate_rejects_then_accepts_at_interval_30` — pin `seconds_since_change=5.0`, `interval=30.0`, `max_wait=300.0`, `now=30.0` (so the cap cannot fire). Assert the decision sequence as the skip count grows: `record_skip()` once → `decide(...).sync is False` and `decide(...).window == 15.0`; twice → `False` and `7.5`; three times → `True` and `3.75`. The gate's own `sync` verdict is what is asserted — not a helper's return value.

   e. `test_window_gate_rejects_then_accepts_at_interval_100` — same pinned `seconds_since_change=5.0` with `interval=100.0`, `max_wait=1000.0`, `now=30.0`. Assert `False` through four skips (window `6.25`) and `True` at five skips (window `3.125`). A hardcoded "accept after the third skip" implementation passes (d) and must fail here.

   f. `test_window_gate_cap_binds_at_second_boundary` — `interval=30.0`, `max_wait=60.0`, `seconds_since_change=1.0` (a change stream that never goes quiet), tracker started at `now=0.0`. Walk the boundaries: at `now=30.0` assert `sync is False` and `cap_fired is False`; at `now=60.0` assert `sync is True` and `cap_fired is True`. Assert the forcing boundary is the second (60 s), not the first (30 s).

   g. `test_window_gate_cap_binds_at_third_boundary` — `interval=30.0`, `max_wait=75.0`, `seconds_since_change=1.0`, started at `now=0.0`. At `now=60.0` assert `sync is False`; at `now=90.0` assert `sync is True` and `cap_fired is True`. A hardcoded "every second cycle" implementation passes (f) and must fail here.

   h. `test_window_gate_cap_does_not_fire_before_the_max_wait` — `interval=30.0`, `max_wait=300.0`, `seconds_since_change=1.0`, started at `now=0.0`. At `now=270.0` assert `cap_fired is False` and `sync is False` (with no skips recorded the window is still the full 30 s, above the 1 s change age). This pins that an implementation reading the maximum wait but never acting on it is not mistaken for one that acts.

   i. `test_window_gate_accepts_when_max_wait_below_interval` — `interval=30.0`, `max_wait=10.0`, `seconds_since_change=0.1`, started at `now=0.0`: at the first boundary `now=30.0` assert `sync is True` and `cap_fired is True`. `test_window_gate_zero_max_wait_always_forces` — `max_wait=0.0`, `seconds_since_change=0.0`, `now=30.0` → `sync is True` and `cap_fired is True`. Together these pin the spec's "a cap below the interval forces a sync on every cycle" and "0 or negative is treated as always force" failure modes.

   j. `test_window_gate_accepts_infinite_change_age` — `seconds_since_change=float("inf")`, `interval=30.0`, `max_wait=300.0`, `now=30.0`, no skips recorded: `sync is True`, `cap_fired is False`, `window == 30.0`. This is the quiet-vault path and it must not be reachable only through the cap.

   Every one of these tests asserts `GateDecision` field values and/or the tracker's public accessors — no test asserts a private attribute.
</requirements>

<constraints>
- Python 3.14, `uv`, ruff (line-length 100), mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- `docs/dod.md` applies in full: type hints on every function signature, docstrings on every public function and class, no unexplained `# type: ignore` or `noqa`, functions over classes for stateless operations, no absolute paths.
- This prompt creates a new module and its tests ONLY. Do NOT modify `src/git_ai_sync/__main__.py`, `src/git_ai_sync/config.py`, `src/git_ai_sync/file_watcher.py`, `src/git_ai_sync/notifier.py`, `src/git_ai_sync/launchd.py`, `README.md`, `CLAUDE.md`, `docs/`, or `CHANGELOG.md` — the wiring, the setting, and the docs each belong to a later prompt.
- Do NOT change `interval` semantics for a quiet vault: at zero skips the window equals the interval, so the gate accepts exactly the cycles it accepts today.
- Do NOT reuse, wrap, or alter `notifier.ConsecutiveFailureTracker`; the decay counter is a separate piece of state. The consecutive-failure counter and the stall-attention card are unchanged by this spec.
- Do NOT change the pull strategy, the pushgateway metrics, the pre-commit hook, or the `sync` / `resolve` / `status` / `doctor` / `version` subcommands.
- The module takes its notion of "now" as a parameter and reads no clock — no test may sleep on the real clock, and no test may call `time.monotonic()`.
- Do NOT add a settings knob, an enable/disable flag, a tunable decay factor, or a metric in this module. The decay is fixed at one half per skip; the only configurable value is the maximum wait, and it arrives as a `decide(...)` argument.
- Existing tests must still pass.
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new unit tests pass:
`uv run pytest tests/test_quiet_window.py -v`
Confirm the spec's decay/window selector collects them:
`uv run pytest tests/ -q -k "decay or window"`
Confirm the schedule is one implementation and the module is log-free:
`grep -c 'def decayed_window' src/git_ai_sync/quiet_window.py` — must print `1`
`! grep -q 'logging\|logger' src/git_ai_sync/quiet_window.py`
Confirm no clock is read inside the module:
`! grep -q 'time\.monotonic\|time\.time' src/git_ai_sync/quiet_window.py`
Confirm the watch loop and the config are untouched by this prompt:
`! grep -q 'quiet_window' src/git_ai_sync/__main__.py`
`! grep -q 'max_wait' src/git_ai_sync/config.py`
Confirm the whole suite still passes:
`uv run pytest tests/ -q`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt (the `32, 16, 8, 4, 2` schedule, the `< 0.001` collapse, the reset to `32`, both gate sequences, both cap boundaries) against the change.
</verification>
