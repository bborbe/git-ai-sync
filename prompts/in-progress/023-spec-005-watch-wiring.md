---
status: approved
spec: [005-consecutive-failure-attention-alert]
created: "2026-10-11T16:26:06Z"
queued: "2026-10-11T18:27:07Z"
---

# Wire the stall tracker and notifier into the watch loop

<summary>
- The watch loop now classifies every cycle as a failure, a success, or a debounce skip, and counts consecutive failures
- Once the configured number of consecutive failures is reached, exactly one operator card is posted — never two while it is outstanding
- The next successful cycle closes the card and resets the count, so a later stall posts a fresh card
- A debounce skip changes nothing: it is neither a failure nor a recovery
- A single isolated failure stays silent, and so does a failure run that is interrupted by a success
- A cycle that ends the process (a conflict-marker refusal, a pull conflict) is not counted at all
- With the notifier script absent, one startup warning names the path and no notifier work happens per cycle
- An unusable threshold (below 2) fails the `watch` startup with an error naming the setting, while `status` is unaffected
- A real watch process against a clone with an unreachable remote posts exactly one card after the second failed cycle and keeps looping
</summary>

<objective>
Wire `notifier.ConsecutiveFailureTracker` and the notifier functions from `src/git_ai_sync/notifier.py` into `cmd_watch` in `src/git_ai_sync/__main__.py`, classifying each cycle as failure / success / skip exactly once per cycle, plus the loop-level tests and the throwaway-clone integration run. **Why:** the watch loop today retries a failing cycle forever and tells nobody, so a vault can stall for hours while launchd reports it healthy — this makes that stall visible as one operator card within about N × interval of the first failure.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff line-length 100, mypy strict, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates. Also read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention — the same two guides the notifier prompt references.

Read `src/git_ai_sync/__main__.py` — `cmd_watch` in full. The pieces that matter: the local import block (`from git_ai_sync import conflict_resolver, git_operations, metrics`), the pushgateway startup block (`if config.pushgateway_url: logger.info(...) else: logger.warning("pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL")`), the `tracker = ChangeTracker(git_repo)` / `tracker.start()` pair (note the name `tracker` is already taken — the failure tracker must use a different local name), the `while True:` body with its debounce gate, the commit `try` block with the `MarkerRefusalError` / `UnsafePathRefusalError` / `GitError` / `Exception` chain, the separate pull `try` block with its conflict `sys.exit(1)` and its two `continue` statements, the `if marker_refused: sys.exit(1)` guard, the `if not has_local_changes:` push-or-last-success branch that ends in `continue`, and the trailing push `try` block.

Read `src/git_ai_sync/notifier.py` (created by the previous prompt) — `ConsecutiveFailureTracker` (`record_failure()`, `record_success()`, `record_skip()`, and the attributes `consecutive_failures: int`, `outstanding_item_id: str | None`, `first_failure_at: float`), `validate_attention_script(script_path: str) -> Path | None`, `should_post_card(consecutive_failures: int, threshold: int, outstanding_item_id: str | None) -> bool`, `post_stall_card(script_path: Path, vault: str, repo_path: Path, failure_count: int, first_failure_at: float, last_error: str) -> str | None`, and `close_stall_card(store_url: str, item_id: str) -> bool`. Every one of them is best-effort and never raises — call them with NO `try`/`except` around them.

Read `src/git_ai_sync/config.py` (extended by the previous prompt) — the new fields `stall_threshold: int`, `attention_script: str`, `attention_store_url: str`.

Read `src/git_ai_sync/metrics.py` — `vault_label(repo_path: Path) -> str` is the vault label; reuse it so one vault has one label across the pushgateway and the card.

Read `tests/test_main.py` — `_mock_git_ops()` (a `MagicMock` carrying the real `GitError` / `MarkerRefusalError` / `UnsafePathRefusalError`), the `_GIT_OPS` / `_CONFIG` / `_ACQUIRE_LOCK` patch constants, `TestCmdWatchDispatch` (the watch harness: patched `ChangeTracker.start`/`stop`/`get_seconds_since_last_change`, `patch("time.sleep", side_effect=[None, KeyboardInterrupt()])` to run one cycle, `contextlib.suppress(KeyboardInterrupt)` around the call), `_build_repo_with_remote(tmp_path)`, `_git_stdout(repo, *argv)`, and the real-repo tests (`test_sync_pulls_when_commit_refused_recovered`, `test_watch_pulls_when_commit_refused_recovered`) for the throwaway-repo style.

Read `tests/conftest.py` — the `stub_notifier` factory fixture (`stub.script`, `stub.argv_log`, `stub.invocations()`) and the `recording_store` fixture (`store.url`, `store.requests`, `store.status`), both added by the previous prompt.

Read `CHANGELOG.md` — a `## Unreleased` section exists; append a bullet to it, do NOT create a second `## Unreleased`.
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, add `notifier` to `cmd_watch`'s local import block:

   ```python
   from git_ai_sync import conflict_resolver, git_operations, metrics, notifier
   ```

2. In `cmd_watch`, add the startup gate and the notifier setup. The threshold check goes immediately after the `logger.info(f"Starting watch mode: ...")` line and BEFORE `git_operations.find_git_repo(...)`:

   ```python
       if config.stall_threshold < 2:
           logger.error(
               f"GIT_AI_SYNC_STALL_THRESHOLD must be >= 2 (got {config.stall_threshold})"
           )
           sys.exit(1)
   ```

   The error text must name `GIT_AI_SYNC_STALL_THRESHOLD` verbatim. The check lives ONLY here: `cmd_sync`, `cmd_status`, `cmd_doctor`, and `cmd_resolve` must not read `config.stall_threshold` at all.

   Then, immediately after the existing pushgateway startup block (`if config.pushgateway_url: ... else: logger.warning(...)`) and before `tracker = ChangeTracker(git_repo)`:

   ```python
       attention_script = notifier.validate_attention_script(config.attention_script)
       stall_tracker = notifier.ConsecutiveFailureTracker()
       vault = metrics.vault_label(git_repo)
   ```

   `validate_attention_script` logs the single startup WARNING naming the path when the script is absent, and returns `None`; when it returns `None` the loop makes ZERO notifier attempts per cycle (the guard below is what enforces that). Do not add a second warning of your own, and do not add an enable/disable flag.

3. In `cmd_watch`, restructure the `while True:` body so each cycle is classified EXACTLY ONCE. The rule, per cycle:
   - **failure** — a phase raised an exception the loop caught and continued past: `record_failure()` (once per cycle, however many phases failed).
   - **success** — every phase ran to completion without a caught-and-continued exception, including the idle "No local changes" cycle and the ahead-of-remote push cycle: close the outstanding card if there is one, then `record_success()`.
   - **skip** — the debounce gate suppressed the cycle: `record_skip()`.
   - **process-ending** — a marker refusal, a pull conflict, or an uncaught exception ends the process: NONE of the three, so the counter is never touched. This is why the classification must NOT be in a `finally:` block (`SystemExit` would run it) and why an exception raised by the unguarded `git_operations.push(...)` inside the `if not has_local_changes:` ahead-of-remote branch must stay unguarded.

   The old → new shape. OLD (the tail of the loop body):

   ```python
               # Pull to get remote changes — ALWAYS runs, even when the commit above failed.
               try:
                   before_pull = git_operations.get_head_commit(git_repo)
                   ...
               except git_operations.GitError as e:
                   if "conflicts" in str(e).lower():
                       logger.error(f"Conflicts detected: {e}")
                       logger.error(f"Run 'git-ai-sync resolve {git_repo}' to resolve")
                       sys.exit(1)
                   logger.error(f"Sync failed: {e}")
                   if marker_refused:
                       sys.exit(1)
                   logger.info("Continuing to watch...")
                   continue
               except Exception as e:
                   logger.exception(f"Unexpected error: {e}")
                   if marker_refused:
                       sys.exit(1)
                   logger.info("Continuing to watch...")
                   continue

               if marker_refused:
                   sys.exit(1)

               if not has_local_changes:
                   if git_operations.is_ahead_of_remote(git_repo):
                       ...
                   else:
                       ...
                   continue

               try:
                   git_operations.push(git_repo)
                   ...
               except git_operations.GitError as e:
                   logger.error(f"Sync failed: {e}")
                   logger.info("Continuing to watch...")

               except Exception as e:
                   logger.exception(f"Unexpected error: {e}")
                   logger.info("Continuing to watch...")
   ```

   NEW — same phases, same log lines, same `sys.exit` paths, but with a per-cycle `cycle_failed` / `last_error` pair and the two `continue` statements replaced by a `pull_failed` flag so the classification block at the end of the body runs exactly once:

   ```python
               # Safe to sync - no recent changes
               logger.info(f"[{iteration}] Checking...")
               has_local_changes = False
               marker_refused = False
               cycle_failed = False
               last_error = ""
               try:
                   <existing commit block, unchanged>
               except git_operations.MarkerRefusalError as e:
                   logger.error(f"Sync failed: {e}")
                   marker_refused = True
               except git_operations.UnsafePathRefusalError as e:
                   logger.error(f"Commit refused; pulling anyway: {e}")
                   cycle_failed = True
                   last_error = str(e)
               except git_operations.GitError as e:
                   logger.error(f"Sync failed: {e}")
                   cycle_failed = True
                   last_error = str(e)
               except Exception as e:
                   logger.exception(f"Unexpected error: {e}")
                   logger.info("Continuing to watch...")
                   cycle_failed = True
                   last_error = str(e)

               # Pull to get remote changes — ALWAYS runs, even when the commit above failed.
               pull_failed = False
               try:
                   <existing pull block, unchanged>
               except git_operations.GitError as e:
                   if "conflicts" in str(e).lower():
                       logger.error(f"Conflicts detected: {e}")
                       logger.error(f"Run 'git-ai-sync resolve {git_repo}' to resolve")
                       sys.exit(1)
                   logger.error(f"Sync failed: {e}")
                   if marker_refused:
                       sys.exit(1)
                   logger.info("Continuing to watch...")
                   pull_failed = True
                   cycle_failed = True
                   last_error = str(e)
               except Exception as e:
                   logger.exception(f"Unexpected error: {e}")
                   if marker_refused:
                       sys.exit(1)
                   logger.info("Continuing to watch...")
                   pull_failed = True
                   cycle_failed = True
                   last_error = str(e)

               if marker_refused:
                   sys.exit(1)

               if not pull_failed:
                   if not has_local_changes:
                       if git_operations.is_ahead_of_remote(git_repo):
                           logger.info("Local branch is ahead of remote, pushing...")
                           git_operations.push(git_repo)
                           logger.info("Pushed to remote")
                           if config.pushgateway_url:
                               metrics.push_last_success(
                                   config.pushgateway_url,
                                   config.pushgateway_username,
                                   config.pushgateway_password,
                                   git_repo,
                               )
                       else:
                           logger.info("No local changes")
                           if config.pushgateway_url:
                               metrics.push_last_success(
                                   config.pushgateway_url,
                                   config.pushgateway_username,
                                   config.pushgateway_password,
                                   git_repo,
                               )
                   else:
                       try:
                           git_operations.push(git_repo)
                           logger.info("Pushed to remote")
                           if config.pushgateway_url:
                               metrics.push_last_success(
                                   config.pushgateway_url,
                                   config.pushgateway_username,
                                   config.pushgateway_password,
                                   git_repo,
                               )
                       except git_operations.GitError as e:
                           logger.error(f"Sync failed: {e}")
                           logger.info("Continuing to watch...")
                           cycle_failed = True
                           last_error = str(e)
                       except Exception as e:
                           logger.exception(f"Unexpected error: {e}")
                           logger.info("Continuing to watch...")
                           cycle_failed = True
                           last_error = str(e)

               # Classify the cycle exactly once: failure, or success.
               if cycle_failed:
                   stall_tracker.record_failure()
                   if attention_script is not None and notifier.should_post_card(
                       stall_tracker.consecutive_failures,
                       config.stall_threshold,
                       stall_tracker.outstanding_item_id,
                   ):
                       stall_tracker.outstanding_item_id = notifier.post_stall_card(
                           attention_script,
                           vault,
                           git_repo,
                           stall_tracker.consecutive_failures,
                           stall_tracker.first_failure_at,
                           last_error,
                       )
               else:
                   if stall_tracker.outstanding_item_id is not None:
                       notifier.close_stall_card(
                           config.attention_store_url, stall_tracker.outstanding_item_id
                       )
                   stall_tracker.record_success()
   ```

   And the debounce gate gains one line:

   ```python
               if seconds_since_change < interval:
                   logger.info(
                       f"[{iteration}] Skipping - files changed"
                       f" {seconds_since_change:.1f}s ago (still editing)"
                   )
                   stall_tracker.record_skip()
                   continue
   ```

   Invariants this structure must hold:
   - `stall_tracker.outstanding_item_id` is assigned the RETURN VALUE of `post_stall_card`, so a failed post (`None`) leaves the guard unset and the next failing cycle retries the post. Never set the guard to a value that did not come from a successful post.
   - The close runs BEFORE `record_success()`, and `record_success()` clears the guard unconditionally — a failed or 409-rejected close must not leave the guard set, or every later card for the rest of the stall would be silently suppressed.
   - Do not add a `try`/`except` around the notifier calls, and do not add a `finally:` classification block.
   - The classification block sits at the very end of the loop body; the loop then iterates and `time.sleep(interval)` paces it.
   - Keep every existing log line and the `metrics.push_last_success(...)` argument lists exactly as they are today.

4. Update `tests/test_main.py` so the existing watch tests keep passing. `cmd_watch` now reads three new attributes off `Config`, and a bare `MagicMock` attribute fails the `config.stall_threshold < 2` comparison with a `TypeError`, so EVERY test that patches `_CONFIG` and calls `cmd_watch` must configure them. Add one module-level helper next to `_mock_git_ops()`:

   ```python
   def _configure_watch_config(
       mock_config: MagicMock,
       *,
       stall_threshold: int = 10,
       attention_script: str = "/nonexistent/attention-ask.py",
       attention_store_url: str = "http://localhost:18080",
       pushgateway_url: str | None = None,
   ) -> MagicMock:
       """Set every Config attribute cmd_watch reads on a patched Config mock."""
       mock_config.return_value.pushgateway_url = pushgateway_url
       mock_config.return_value.stall_threshold = stall_threshold
       mock_config.return_value.attention_script = attention_script
       mock_config.return_value.attention_store_url = attention_store_url
       return mock_config
   ```

   The default `attention_script` is deliberately a non-existent path, so the notifier is disabled and the existing assertions (invocation counts, log contents) are unchanged. Replace every existing `mock_config.return_value.pushgateway_url = <value>` assignment in a test that calls `cmd_watch` with a `_configure_watch_config(mock_config, pushgateway_url=<value>)` call. That is exactly the fifteen `cmd_watch` tests that patch `_CONFIG`: all of `TestCmdWatchDispatch` (the merge/rebase strategy tests, the three metrics tests, `test_watch_last_success_after_successful_sync`, `test_watch_last_success_zero_after_failed_sync`, and `test_watch_startup_warning_when_unconfigured`), `test_watch_marker_refusal_exits`, `test_watch_marker_refusal_still_pulls`, `test_watch_marker_refusal_exits_even_when_pull_fails`, `test_watch_unexpected_error_still_pulls`, `test_watch_pulls_when_commit_refused_unrecovered`, `test_watch_continues_after_refusal`, and `test_watch_pulls_when_commit_refused_recovered`. Leave `TestLockAcquisition.test_cmd_watch_exits_on_lock_error` alone — it does not patch `_CONFIG` at all (it uses the real `Config`, whose default threshold is 10) and it exits on the lock error before any of the new behaviour runs. Leave every `cmd_sync` test alone.

5. Add the loop-level tests to `tests/test_main.py`. All are module-level functions (so their node ids are greppable). Each drives `cmd_watch` through the `TestCmdWatchDispatch` harness: `patch(_GIT_OPS, mock_git)`, `patch(_CONFIG) as mock_config` + `_configure_watch_config(...)`, `patch(_ACQUIRE_LOCK)`, `ChangeTracker.start`/`stop`, `patch.object(ChangeTracker, "get_seconds_since_last_change", ...)`, `patch("time.sleep", side_effect=[...])` where one `None` per cycle is followed by a terminating `KeyboardInterrupt()`, and `contextlib.suppress(KeyboardInterrupt)` around the call. `mock_git.find_git_repo.return_value = Path("/repo")` (so the vault label is `repo`), `mock_git.has_changes.return_value = False`, `mock_git.is_ahead_of_remote.return_value = False`, and `mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")` for a failing cycle.

   The tests, with the assertions the acceptance criteria demand:

   a. `test_watch_posts_card_at_threshold` — threshold 2, the stub script (`stub_notifier()`), two failing cycles. Assert `len(stub.invocations()) == 1`; the single recorded argv's `--payload` value contains `repo` and the count `2`; and the invocation happened on the SECOND cycle (assert the failing cycles ran: `mock_git.pull_merge.call_count == 2`). Also assert no close request was made (`recording_store.requests == []`) because no success followed.

   b. `test_watch_posts_card_at_threshold_three` — `stall_threshold=3`, three failing cycles: exactly 1 invocation, and its `--payload` carries the count `3`.

   c. `test_watch_silent_below_threshold` — threshold 2, ONE failing cycle: `stub.invocations() == []` and `recording_store.requests == []`.

   d. `test_watch_silent_on_isolated_failure` — threshold 2, one failing cycle then one successful cycle (no `pull_merge` side effect on the second): `stub.invocations() == []` and `recording_store.requests == []` (nothing was posted, so nothing may be closed).

   e. `test_watch_resets_on_success` — threshold 2, cycles: fail, success, fail: `stub.invocations() == []` and `recording_store.requests == []`.

   f. `test_watch_skip_is_neutral` — threshold 2, cycles: fail, skip, fail. Drive the skip by giving `get_seconds_since_last_change` a `side_effect` list of `[999, 5, 999]` (the middle cycle sees a change 5 s ago and is debounce-skipped). Assert exactly 1 invocation, its `--payload` carries the count `2`, and that the loop ran three iterations (the skip did not reset the counter and did not count as a failure).

   g. `test_watch_one_card_until_reset` — threshold 2, four failing cycles: exactly 1 invocation in total, and `recording_store.requests == []`.

   h. `test_watch_closes_card_on_recovery` — threshold 2, cycles: fail, fail, success. Assert `len(stub.invocations()) == 1` and `recording_store.requests == [("POST", f"/api/1.0/attention/{<the stub's item id>}/close")]` — exactly one request, that method, that path, and no other request. Point the notifier at the recording store via `_configure_watch_config(mock_config, attention_store_url=recording_store.url, ...)`.

   i. `test_watch_second_stall_posts_fresh_card` — threshold 2, cycles: fail, fail, success, fail, fail. Assert `len(stub.invocations()) == 2` and that both invocations carry the SAME `--dedup-key` value (`git-ai-sync-repo-stalled`), with the second recorded after the success. Point the notifier at the recording store via `_configure_watch_config(mock_config, attention_store_url=recording_store.url, ...)` (without this the close targets the helper's default `http://localhost:18080` and the assertion cannot hold), then assert `len(recording_store.requests) == 1` (the success closed the first card).

   j. `test_watch_notifier_disabled_when_script_absent` — `attention_script` left at the non-existent default path, at least three failing cycles: the run returns normally (no `SystemExit`), `caplog` (taken with `caplog.at_level("WARNING")`) contains EXACTLY ONE WARNING whose message contains the configured path, counted as `sum(1 for r in caplog.records if r.levelname == "WARNING" and path in r.getMessage()) == 1`, no per-cycle notifier failure is logged, and `recording_store.requests == []` (zero notifier attempts, so nothing was posted and nothing may be closed).

   k. `test_watch_exits_when_threshold_below_two` — drive it through the REAL environment, not the mock: `monkeypatch.setenv("GIT_AI_SYNC_STALL_THRESHOLD", "1")` with the real `Config` (do NOT patch `_CONFIG` here). `pytest.raises(SystemExit)` around `cmd_watch(argparse.Namespace(path=".", interval=30, strategy="merge"))` with `exc_info.value.code == 1`, plus a `caplog` ERROR record containing `GIT_AI_SYNC_STALL_THRESHOLD`. The exit must happen before any git work, so no `_GIT_OPS` or `_ACQUIRE_LOCK` patch is needed — assert instead that no `[1] Checking...` line was logged (the run never reached the loop).

   l. `test_status_ignores_threshold_below_two` — `monkeypatch.setenv("GIT_AI_SYNC_STALL_THRESHOLD", "1")`, a real repo built in `tmp_path` (`git init`), then `cmd_status(argparse.Namespace(command="status", path=str(repo)))` must NOT raise `SystemExit` (the `>= 2` check is applied only when `watch` starts).

   m. `test_watch_stall_alert_integration` — the throwaway-fixture run (acceptance criterion 11). Build it like this:
      - `git init -q --bare <tmp>/remote.git`; `git clone -q <tmp>/remote.git <tmp>/vault`; set `user.email`, `user.name`, `commit.gpgsign=false` in the clone; write a seed file, `git add`, `git commit`, `git push -q -u origin HEAD` so the clone tracks a real upstream.
      - `mkdir <tmp>/vault/.obsidian` (the vault marker the fixture requires).
      - Delete `<tmp>/remote.git` so the clone's `origin` is a bare LOCAL remote that is now unreachable.
      - `stub = stub_notifier()`; env `{**os.environ, "GIT_AI_SYNC_STALL_THRESHOLD": "2", "GIT_AI_SYNC_ATTENTION_SCRIPT": str(stub.script), "ATTENTION_STORE_URL": recording_store.url}`, with `GIT_AI_SYNC_PUSHGATEWAY_URL` popped from the copy so the run cannot reach a host pushgateway.
      - Start `subprocess.Popen([sys.executable, "-m", "git_ai_sync", "watch", "--interval", "1", str(vault)], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)`.
      - Poll `stub.invocations()` (bounded, e.g. every 0.25 s up to ~15 s) until the first post appears, then let it run ~2 more seconds, then `proc.terminate()` and read the captured output with `proc.communicate(timeout=10)`.
      - Assert exactly 1 invocation after the second failed cycle, and that the captured output contains at least 3 `Sync failed` lines — i.e. the process kept looping instead of exiting. Assert the process did not die on its own before `terminate()` (`proc.poll() is None`).

   n. `test_watch_exit_path_posts_no_card` (the process-exit class). `stall_threshold=2` with the stub wired (`attention_script=str(stub.script)`), a marker refusal on the first cycle (`commit_with_marker_recovery` side_effect `MarkerRefusalError`), `pytest.raises(SystemExit)`: assert `stub.invocations() == []` and `recording_store.requests == []`. A cycle that ends the process must never touch the counter, so no card is posted for this class — this is the test that fails if classification is moved into a `finally:` block.

   o. `test_watch_non_giterror_pull_is_a_failure`. `stall_threshold=2`, two cycles with `mock_git.pull_merge.side_effect = RuntimeError("boom")`: assert `len(stub.invocations()) == 1` and that the recorded `--payload` carries the count `2` — a non-`GitError` phase failure is still exactly one cycle failure.

   p. `test_watch_failed_post_retries_each_cycle`. `stall_threshold=2`, three failing cycles, with the notifier pointed at `stub_notifier(exit_code=1).script`: assert `len(stub.invocations()) == 3` — a post is retried on every failing cycle at or after the threshold, because a failed post never sets the outstanding-card guard — and `recording_store.requests == []`. This is the invariant a plausible wrong implementation (`outstanding_item_id = item_id or ""`) would violate while passing every other test.

6. Append this bullet to the existing `## Unreleased` section of `CHANGELOG.md` (do not create a second `## Unreleased`):

   ```
   - feat: Count consecutive failed watch cycles and post one operator attention card at the threshold, closing it automatically on the next success — a single isolated failure stays silent, a debounce skip is neutral, and a cycle that exits the process is not counted
   ```
</requirements>

<constraints>
- Python 3.14, `uv`, ruff, mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- Classification is per CYCLE, not per phase: a cycle in which the commit failed while the pull and push succeeded is ONE failure, and the counter increments once per cycle however many phases failed.
- `MarkerRefusalError` and `UnsafePathRefusalError` are both `GitError` subclasses; their catches MUST precede `except git_operations.GitError`.
- A cycle that ends the process (marker refusal, pull conflict, uncaught exception) must never update the counter — do not classify in a `finally:` block, and do not wrap the ahead-of-remote `git_operations.push(...)` call in a `try`.
- The counter and the notifier must not run in `cmd_sync`; one-shot sync keeps today's exit-non-zero-on-failure behaviour unchanged.
- The `>= 2` threshold check is applied ONLY when `watch` starts, so `sync`, `status`, `doctor`, and `resolve` are unaffected by a threshold value they never read.
- The notifier calls must have NO `try`/`except` around them — the notifier functions are already best-effort and never raise.
- The counter is per watch-process and in-memory only: no state file, no persistence, no launchd-restart survival.
- The `outstanding_item_id` guard is set only from a successful post's return value, and is cleared unconditionally on the first successful cycle, regardless of the close's outcome.
- Do NOT add an enable/disable flag for the notifier, and do NOT make the dedup key, vault label, or producer id configurable.
- Do NOT change the pushgateway behaviour, metric names, or their per-vault grouping key; do NOT change `cmd_sync`, `cmd_resolve`, `cmd_status`, `cmd_doctor`, or the launchd commands.
- Keep the existing watch-loop tests that patch `time.sleep` with a side-effect list working — the new logic runs inside the loop body, not around `sleep`.
- Existing tests must still pass (including every test that patches `_CONFIG` and calls `cmd_watch`, which must be updated to configure the three new settings).
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new loop tests pass:
`uv run pytest tests/test_main.py -k 'watch or threshold' -v`   # matches all 16 new tests (a)-(p)
Confirm the existing watch and sync tests still pass:
`uv run pytest tests/test_main.py -v`
Confirm the whole suite:
`uv run pytest`
Confirm the loop consults the notifier contract:
`grep -nE 'record_failure|record_success|record_skip|should_post_card|post_stall_card|close_stall_card|validate_attention_script' src/git_ai_sync/__main__.py`
Confirm the threshold gate names the setting:
`grep -n 'GIT_AI_SYNC_STALL_THRESHOLD' src/git_ai_sync/__main__.py`
Confirm the threshold check is NOT in the one-shot paths:
`grep -c 'stall_threshold' src/git_ai_sync/__main__.py` — must print `3`: the `if config.stall_threshold < 2:` line, its error-message line, and the `config.stall_threshold` argument passed to `should_post_card` in the per-cycle classification block. **All three are inside `cmd_watch`, the path that MUST read the field — the third is required, not a leak, and must not be deleted.** A count of `4` or more means the setting leaked into a path that must not read it.
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change.
</verification>
