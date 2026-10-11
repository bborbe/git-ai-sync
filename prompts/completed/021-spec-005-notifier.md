---
status: completed
spec: [005-consecutive-failure-attention-alert]
summary: Added src/git_ai_sync/notifier.py with the ConsecutiveFailureTracker, redaction, best-effort attention-card post/close primitives, plus tests/test_notifier.py and two shared fixtures in tests/conftest.py, and an Unreleased changelog entry
execution_id: git-ai-sync-exec-021-spec-005-notifier
dark-factory-version: v0.196.0
created: "2026-10-11T16:26:06Z"
queued: "2026-10-11T18:27:07Z"
started: "2026-10-11T18:27:32Z"
completed: "2026-10-11T18:30:50Z"
---

# Add the consecutive-failure tracker and the best-effort attention-card notifier

<summary>
- A new module can count consecutive failed sync cycles and remember that a stall card is currently on the operator's board
- When the count reaches a threshold and no card is outstanding, exactly one operator card is posted — naming the vault, the failure count, and the last error
- The card is posted by the supervisor's attention-ask script, invoked as an argument vector, never through a shell
- A missing, unreadable, failing, or hanging script never breaks or delays a sync cycle — every failure is a warning and a `None`
- On recovery the outstanding card is closed with a direct HTTP POST to the attention store's close route; the close is bounded and never retried
- An item id that is not a 32-character hex store id is refused before it is placed in the close URL
- A remote URL (which can embed a credential) can never reach the card, because the error summary is redacted before it is used
- Shared test fixtures let later prompts drive the notifier with a stub script and a recording HTTP store
- Nothing in this change is wired into the watch loop yet — the loop classification lands in a later prompt
</summary>

<objective>
Create `src/git_ai_sync/notifier.py` holding the consecutive-failure tracker and the best-effort attention-board notifier (post + close), following the `metrics.py` precedent exactly: stateless functions that return a value, never raise, and log every failure at WARNING. Plus `tests/test_notifier.py` and the two shared fixtures in `tests/conftest.py` that the watch-loop prompt will reuse.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff line-length 100, mypy strict, pytest with `asyncio_mode = "auto"`, `tests/` directory).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates (functions over classes for stateless operations, `subprocess.run` with `check=False` and explicit error handling, docstrings on public functions, type hints everywhere, no absolute paths).

Read `src/git_ai_sync/metrics.py` — the precedent this module must follow: module-level `logger = logging.getLogger(__name__)`, module-level `from urllib.request import Request, urlopen` (so tests patch `git_ai_sync.metrics.urlopen`), `PUSH_TIMEOUT_SECONDS = 5`, `vault_label(repo_path: Path) -> str` (the repository directory name — reuse it, do NOT re-implement it), and functions that return a bool, catch `Exception`, `logger.warning(...)`, and never raise.

Read `tests/test_metrics.py` — the unit-test shape this module's tests follow (patch the module's `urlopen`, assert on `mock_urlopen.call_args.args[0]`'s `full_url` / `method` / `data`, `caplog.at_level("WARNING")` for the failure paths, and assert a credential never appears in `caplog.text`).

Read `tests/conftest.py` — the existing `temp_dir` fixture; the two new fixtures go here.

Read `tests/test_no_print.py` — production code must not call `print()` (the notifier uses `logger` only).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention and the required conventional prefixes.

Read `CHANGELOG.md` — there is currently NO `## Unreleased` section; the newest section is `## v0.12.2`. Insert `## Unreleased` directly above `## v0.12.2`, after the frozen preamble (never move or edit the `# Changelog` title, the "All notable changes..." line, the SemVer link, or the MAJOR/MINOR/PATCH bullets).
</context>

<requirements>
1. Create `src/git_ai_sync/notifier.py`. Module docstring naming it as the best-effort consecutive-failure alerting side channel for the watch loop. Module-level:

   ```python
   """Best-effort attention-board alerting for a stalled git-ai-sync watch loop."""

   import logging
   import os
   import re
   import subprocess
   import sys
   import time
   from datetime import datetime, timezone
   from pathlib import Path
   from urllib.error import HTTPError
   from urllib.request import Request, urlopen

   from git_ai_sync import metrics

   logger = logging.getLogger(__name__)

   PRODUCER_KIND = "cron"
   ITEM_ID_HEX_LEN = 32
   CONTEXT_ERROR_MAX_CHARS = 200
   _HEX_DIGITS = frozenset("0123456789abcdef")
   _URL_RE = re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://\S+")
   ```

   Read the timeout from `metrics.PUSH_TIMEOUT_SECONDS` **at call time** (i.e. `timeout=metrics.PUSH_TIMEOUT_SECONDS`), never through a module-level `from ... import PUSH_TIMEOUT_SECONDS` alias — tests patch `git_ai_sync.metrics.PUSH_TIMEOUT_SECONDS` to keep the suite fast, and a value bound at import time would ignore that patch.

2. In `notifier.py`, add `class ConsecutiveFailureTracker` — a small stateful object in the `ChangeTracker` style (`src/git_ai_sync/file_watcher.py`), holding the failure counter and the outstanding-card guard:

   ```python
   class ConsecutiveFailureTracker:
       """Consecutive failed-cycle counter and outstanding-card guard for the watch loop."""

       def __init__(self) -> None:
           self.consecutive_failures: int = 0
           self.outstanding_item_id: str | None = None
           self.first_failure_at: float = 0.0

       def record_failure(self) -> None:
           """Count one failed cycle; stamp the run's start on the 0 -> 1 transition."""

       def record_success(self) -> None:
           """Reset the counter and clear the outstanding-card guard."""

       def record_skip(self) -> None:
           """Leave the counter and any outstanding card unchanged."""
   ```

   Semantics, exactly:
   - `record_failure()` — if `consecutive_failures == 0`, set `first_failure_at = time.time()` (so it names the FIRST failure of the run, not the latest); then increment `consecutive_failures` by 1. Called once per failed cycle, however many phases failed.
   - `record_success()` — set `consecutive_failures = 0`, `first_failure_at = 0.0`, and `outstanding_item_id = None`. This is the reset-on-recovery path and it clears the guard **unconditionally** — the loop closes the card before calling this, and a failed close must not leave the guard set.
   - `record_skip()` — a documented no-op (`pass`). A debounce skip is neither a failure nor a recovery.
   - `first_failure_at` is `0.0` when no failure run is in progress (mirroring `ChangeTracker._last_change_time: float = 0.0`), so the attribute is `float`, not `float | None` — mypy strict must pass.

3. In `notifier.py`, add the pure helpers:

   ```python
   def dedup_key(vault: str) -> str:
       """The attention store's re-post suppression key for one vault's stall card."""
       # returns f"git-ai-sync-{vault}-stalled"

   def producer_id(vault: str) -> str:
       """The explicit producer id for one vault's stall card."""
       # returns f"git-ai-sync-{vault}"

   def is_valid_item_id(candidate: str) -> bool:
       """True iff candidate is a 32-character lowercase-hex attention store item id."""

   def should_post_card(
       consecutive_failures: int, threshold: int, outstanding_item_id: str | None
   ) -> bool:
       """True iff the failure count has reached the threshold and no card is outstanding."""
       # returns consecutive_failures >= threshold and outstanding_item_id is None
   ```

   `is_valid_item_id` accepts exactly 32 characters from `_HEX_DIGITS` (lowercase only — the store's ids are lowercase hex). These four values are NOT configurable: no parameter, no env var, no config field.

4. In `notifier.py`, add `def validate_attention_script(script_path: str) -> Path | None`:

   - Return the resolved `Path` when `script_path` names a regular, **readable** file (`Path(script_path).is_file()` and `os.access(..., os.R_OK)`). **Do NOT require the executable bit.** The supervisor's `attention-ask.py` ships mode **100644** in every installed copy — the marketplace copy, its git mode (`git ls-files -s` → `100644`), and all **188** plugin-cache copies — and every caller in the plugin runs it as `python3 attention-ask.py`. An `X_OK` gate therefore returns `None` on every machine, silently disabling the notifier in production. (The stub of requirement 8 is deliberately left **non-executable** — mode 0644, launched through the interpreter — so an `X_OK` gate fails the stub-based tests too and the regression cannot hide behind an executable stub.) This is a correctness requirement, not a preference.
   - Otherwise `logger.warning(...)` ONCE, naming the offending path and saying the attention stall alert is disabled, and return `None`. The WARNING message must contain the path verbatim (the acceptance evidence greps the log for it).
   - Never raises (a `str` that is not a usable path is the same as a missing file).

5. In `notifier.py`, add `def redact_error_summary(text: str) -> str`:

   - Replace every URL-shaped run (`_URL_RE`, i.e. `scheme://` followed by non-whitespace) with the literal `<url>`.
   - Truncate the result to `CONTEXT_ERROR_MAX_CHARS` (200) characters.
   - Return the redacted, truncated text.
   - Provenance: the spec's Security section — the error text is influenced by the watched repository's contents, and a remote URL can carry an embedded credential, so a URL must never reach the card. Redact BEFORE truncating, so a URL cannot survive as a truncated fragment.

6. In `notifier.py`, add the poster:

   ```python
   def post_stall_card(
       script_path: Path,
       vault: str,
       repo_path: Path,
       failure_count: int,
       first_failure_at: float,
       last_error: str,
   ) -> str | None:
       """Post one stall card through the attention-ask script; return its item id or None.

       Returns the 32-hex item id on success, None on any failure (logged at
       WARNING). Never raises, never retries within the call.
       """
   ```

   - Build the argv as a LIST of separate elements — never a shell string, never `shell=True`:
     `[sys.executable, str(script_path), "post", "--dedup-key", dedup_key(vault), "--producer-id", producer_id(vault), "--producer-kind", PRODUCER_KIND, "--payload", <payload-text>, "--context", <context-text>, "--option", <option 1>, "--option", <option 2>, "--recommend", <one of the two option labels>]`
   - Exactly two `--option` elements, and `--recommend` names one of them (the store's own `build_options` requires the recommended label to be one of the options).
   - Do NOT pass `--liveness-ref` (the script's `owner:` default is the contract) and do NOT pass `--expires-at` (the script's default TTL is the contract).
   - `<payload-text>` names what stalled: it must contain the vault label and the failure count.
   - `<context-text>` names what is blocked: it must contain `str(repo_path)`, the first-failure time derived from `first_failure_at` (e.g. `datetime.fromtimestamp(first_failure_at, tz=timezone.utc).isoformat()`), and `redact_error_summary(last_error)`.
   - The payload and context must contain no pushgateway URL, no model name, no environment value, and no remote URL — only the vault label, the count, the repository path, the first-failure time, and the redacted error summary.
   - The two option labels: one actionable (e.g. "Investigate the vault's sync failure now"), one deferring (e.g. "Leave it — it will retry automatically"); `--recommend` names the actionable one.
   - Run it with `subprocess.run(argv, check=False, capture_output=True, text=True, timeout=metrics.PUSH_TIMEOUT_SECONDS)`. Inherit the environment (do NOT override `ATTENTION_STORE_URL`): the script reads that variable itself, and git-ai-sync's `Config` reads the same variable, so the post and the close cannot disagree about which store they are talking to.
   - Exit code 0 means a card was created: parse the item id from the stdout line beginning `ITEM_ID: `. Take the remainder of that line, strip it, and accept it only when `is_valid_item_id(...)` is True. Return it.
   - Return `None` (never raise, never retry) after a `logger.warning(...)` in each of these cases: `subprocess.TimeoutExpired` (name the timeout), `OSError` (spawn failure), a non-zero return code (name the code and a bounded tail of stdout/stderr), and a 0 exit whose stdout carries no valid `ITEM_ID:` line (say the item id was unreadable).
   - Never log a credential: log the exit code and a bounded tail of the script's output, never an environment value.

7. In `notifier.py`, add the closer:

   ```python
   def close_stall_card(store_url: str, item_id: str) -> bool:
       """POST {store}/api/1.0/attention/{item_id}/close; return True on success.

       Best-effort: never raises, never retries. Any failure — an unreadable item
       id, an unreachable store, or a rejected close — is logged at WARNING and
       reported as False.
       """
   ```

   - First validate: when `is_valid_item_id(item_id)` is False, `logger.warning(...)` saying the item id is not a 32-hex store id, issue NO request, and return `False`. The id must be validated before it is interpolated into the URL path so a crafted or truncated value cannot redirect the close.
   - URL: `f"{store_url.rstrip('/')}/api/1.0/attention/{item_id}/close"`.
   - `Request(url, method="POST")` — no body, no credential (the local default store is unauthenticated, exactly as the posting script assumes).
   - `urlopen(request, timeout=metrics.PUSH_TIMEOUT_SECONDS)` inside `try`; return `True` on success.
   - On `HTTPError` (imported in requirement 1), `logger.warning(...)` NAMING THE HTTP STATUS CODE (the 409 case must produce a WARNING containing `409`) and return `False`. A 409 means the card was already closed — an illegal transition the store rejects rather than treating as a no-op. Never retry.
   - On any other `Exception` (unreachable store, timeout, malformed URL), `logger.warning(...)` and return `False`.

8. Extend `tests/conftest.py` with two shared fixtures. Keep the existing `temp_dir` fixture untouched. These are the fixtures the watch-loop prompt reuses, so their names and shapes are pinned:

   ```python
   @dataclass
   class StubNotifier:
       """A stub standing in for the supervisor's attention-ask.py."""
       script: Path
       argv_log: Path

       def invocations(self) -> list[list[str]]:
           """Return one argv list per recorded invocation (empty when never invoked)."""

   @pytest.fixture
   def stub_notifier(tmp_path: Path) -> Callable[..., StubNotifier]:
       """Factory writing a stub attention-ask script, launched via the interpreter.

       make(item_id: str = <32 lowercase-hex "0"*32>, exit_code: int = 0,
            sleep_seconds: float = 0.0) -> StubNotifier
       """
   ```

   The factory writes `tmp_path / "stub-attention-ask.py"` — **no `chmod`, and never an executable bit**: `post_stall_card` launches the script as `[sys.executable, str(script_path), ...]`, exactly as every real caller in the supervisor plugin runs `attention-ask.py`. It returns a `StubNotifier` whose `argv_log` is `tmp_path / "attention-argv.log"`. The script body is Python:

   ```python
   import sys

   with open("<argv_log>", "a") as fh:
       # Reconstruct the argv the parent used: the interpreter it launched us
       # with, then our own argv (script path, then the post arguments).
       fh.write(sys.executable + "\n")
       for a in sys.argv:
           fh.write(a + "\n")
       fh.write("===\n")
   # time.sleep(<sleep_seconds>)   only when sleep_seconds > 0
   # print("ITEM_ID: <item_id>")   only when exit_code == 0
   # sys.exit(<exit_code>)
   ```

   When `exit_code != 0` the stub prints `FAILED: stub` instead of the `ITEM_ID:` line. One argv element per line, and a `===` delimiter after each invocation, so `StubNotifier.invocations()` splits the log on `===` and returns the non-empty groups as lists of argv elements — **each group's first element is the interpreter (`sys.executable`), the second is the script path, and the third is `"post"`**. Tests that previously asserted `group[0] == str(script_path)` and `group[1] == "post"` shift by one accordingly.

   ```python
   @dataclass
   class RecordingStore:
       """A local HTTP server standing in for the attention store."""
       url: str
       requests: list[tuple[str, str]]   # (method, path) per received request
       status: int                       # response status; default 200, settable per test

   @pytest.fixture
   def recording_store() -> Generator[RecordingStore]:
       """A `http.server.HTTPServer` on port 0, served from a daemon thread."""
   ```

   The handler appends `(self.command, self.path)` to `requests` and answers `status` with an empty body; the fixture shuts the server down and joins the thread on teardown. `RecordingStore.status` must be read at request time (not captured when the server starts) so a test can flip it to 409 mid-run.

9. Create `tests/test_notifier.py`. All tests are module-level or plain classes (no `unittest`). Required coverage:

   a. **Tracker semantics.** `record_failure()` increments and stamps `first_failure_at` on the 0 -> 1 transition only (patch `git_ai_sync.notifier.time.time` to a fixed value; a second failure leaves the stamp unchanged); `record_success()` resets the count, `first_failure_at`, and the guard; `record_skip()` changes nothing (count, stamp, and guard all unchanged).

   b. **`should_post_card` table test** — `(9, 10, None) -> False`, `(10, 10, None) -> True`, `(11, 10, None) -> True`, `(10, 10, "a"*32) -> False`.

   c. **`is_valid_item_id` table test** — `"0"*32` and a mixed 32-char lowercase-hex id -> True; 31 chars, 33 chars, uppercase hex, a non-hex character, `""` -> False.

   d. **Key format.** `dedup_key("Personal") == "git-ai-sync-Personal-stalled"` and `producer_id("Personal") == "git-ai-sync-Personal"`.

   e. **`validate_attention_script`.** A non-existent path -> `None` with exactly ONE WARNING containing the path; a directory -> `None` + WARNING; **a regular readable file with mode 0644 and NO executable bit -> the resolved `Path` with no WARNING** — this is the case that MUST pass, because the real `attention-ask.py` is mode 0644 and an `X_OK` gate would silently disable the notifier in production; a regular file with mode 0000 -> `None` + WARNING. Never raises for any input.

   f. **`redact_error_summary`.** `"fatal: unable to access 'https://user:s3cret@host/repo.git'"` yields a result containing neither `s3cret` nor `https://`, and containing `<url>`; a 300-character non-URL string is truncated to 200 characters; a string under 200 characters is returned unchanged.

   g. **Card contract (AC 7).** With `stub_notifier()` as the script and `post_stall_card(stub.script, "Personal", Path("/vaults/Personal"), 2, <fixed time>, "Sync failed: boom")`, assert the returned item id is the stub's 32-hex id and assert the recorded argv FIELD BY FIELD: its first two elements are `sys.executable` and `str(stub.script)` (the script is launched through the interpreter, never by its own executable bit), then it contains exactly one `--dedup-key` whose value is `git-ai-sync-Personal-stalled`; `--producer-id` equals `git-ai-sync-Personal`; `--producer-kind` equals `cron`; exactly two `--option` elements; exactly one `--recommend` whose value equals one of the two option values; the `--payload` contains `Personal` and the count `2`; the `--context` contains `/vaults/Personal` and the error summary; there is NO `--liveness-ref` and NO `--expires-at` element. Also assert the script was invoked exactly once (`len(stub.invocations()) == 1`).

   h. **Post failure paths are best-effort (AC 10b, notifier half).** Each of these returns `None`, logs a WARNING, and never raises: a non-existent script path; `stub_notifier(exit_code=1)`; `stub_notifier(exit_code=0, item_id="not-hex")` (unreadable item id); and `stub_notifier(sleep_seconds=30)` with `patch("git_ai_sync.metrics.PUSH_TIMEOUT_SECONDS", 0.2)` — for the hanging stub, also assert the call returned within a couple of seconds and that the WARNING names the timeout. In no case may an exception escape.

   i. **Close primitive (AC 8).** Point `close_stall_card` at `recording_store.url` and assert `True`; assert `recording_store.requests == [("POST", f"/api/1.0/attention/{item_id}/close")]` — exactly one request, that method, that path. With `recording_store.status = 409`: `close_stall_card` returns `False`, logs a WARNING containing `409`, and `len(recording_store.requests) == 1` (no retry). With an invalid item id: returns `False`, logs a WARNING, and `recording_store.requests == []` (no request issued). With an unreachable store URL (a closed port) it returns `False` and logs a WARNING. `close_stall_card` never raises.

   j. **Composed recovery run (AC 8 + AC 9, "a later stall posts a fresh card").** Compose the module's own primitives as the loop will: post via `stub_notifier()`, assert one invocation; set `tracker.outstanding_item_id` to the returned id; then `close_stall_card(recording_store.url, tracker.outstanding_item_id)` followed by `tracker.record_success()`; assert the recording store saw exactly one request and that `tracker.consecutive_failures == 0` and `tracker.outstanding_item_id is None`; then drive `record_failure()` to the threshold again and post once more, and assert `len(stub.invocations()) == 2` with both invocations carrying the SAME `--dedup-key` — proving the guard was cleared by the success rather than left holding the first card's item id. Repeat the run with `recording_store.status = 409` and assert the close is still issued exactly once, the WARNING names `409`, and the counter and guard are cleared regardless.

   k. **No credential on the card.** Drive a post whose `last_error` embeds `https://user:s3cret@host/repo.git` and assert `s3cret` appears in neither the recorded argv nor `caplog.text`.

10. Insert a `## Unreleased` section into `CHANGELOG.md` directly above `## v0.12.2` (the frozen preamble stays untouched) with this bullet:

   ```
   - feat: Post one operator attention card when a vault's watch loop has failed a configurable number of consecutive cycles, and close it on the next success — a best-effort side channel that never breaks a sync cycle
   ```
</requirements>

<constraints>
- Python 3.14, `uv`, ruff, mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green (ruff strict, mypy strict, pytest).
- Follow the `metrics.py` precedent exactly: a stateless module of functions that return a value, never raise, and log every failure at WARNING. The module must be safe to call with no `try`/`except` around it.
- The tracker is the ONLY class in the module (the spec allows one small object in the `ChangeTracker` style); every other operation is a function.
- Invoke the notifier as an argument vector, never through a shell — no `shell=True`, no string interpolation into a command line.
- The close request must validate the item id as a 32-character lowercase-hex store id before it is placed in the URL path; an id that does not match is treated as unreadable (WARNING, no request) rather than interpolated.
- Never place a credential, a token, a remote URL, or any environment value into the payload or context. The error summary is redacted and truncated to at most 200 characters before use.
- The notifier's argv and the card's wording must not include the pushgateway URL, the model name, or any environment value — only the vault label, the count, the repository path, the first-failure time, and the redacted error summary.
- No absolute paths hardcoded in source; no user path literals. Use `Path` and `Path.home()` only where a default is genuinely needed (this prompt needs none — the script path is a parameter).
- Do NOT add an enable/disable flag, a configurable dedup key, a configurable vault label, or a configurable producer id.
- Do NOT add a persisted counter or a state file on disk — the counter is in-memory by design.
- Do NOT add a new timeout constant: both the subprocess timeout and the close-request timeout are `metrics.PUSH_TIMEOUT_SECONDS`, read at call time.
- Do NOT wire anything into `cmd_watch` in this prompt — the loop classification is a later prompt. Do not modify `src/git_ai_sync/__main__.py`, `config.py`, or `launchd.py`.
- Do NOT touch the pushgateway metrics path (`metrics.py`) or its series.
- `print()` is forbidden in production code (see `tests/test_no_print.py`) — use `logger` only.
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new module's tests pass:
`uv run pytest tests/test_notifier.py -v`
Confirm the shared fixtures did not break the existing suite:
`uv run pytest tests/test_metrics.py tests/test_config.py -v`
Confirm the module is importable and exports the contract:
`uv run python -c "from git_ai_sync import notifier; print(notifier.dedup_key('V'), notifier.producer_id('V'), notifier.is_valid_item_id('0'*32), notifier.should_post_card(2, 2, None))"`
Confirm the dedup-key format exists in the source:
`grep -rn 'git-ai-sync-.*-stalled' src/`
Confirm the shell is never used:
`! grep -rn 'shell=True' src/git_ai_sync/notifier.py`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change.
</verification>
