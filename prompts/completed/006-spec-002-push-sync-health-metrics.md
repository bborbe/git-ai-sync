---
status: completed
summary: 'Added per-vault pushgateway metrics (heartbeat + last-success) to git-ai-sync watch: new metrics module, config fields, watch-loop wiring, tests, README and CHANGELOG updates; make precommit passes'
execution_id: git-ai-sync-alert-sync-stalled-exec-006-spec-002-push-sync-health-metrics
dark-factory-version: dev
created: "2026-09-02T20:35:03Z"
queued: "2026-09-02T20:35:03Z"
started: "2026-09-02T20:35:05Z"
completed: "2026-09-02T20:40:48Z"
---
# Push heartbeat and last-success metrics to the Prometheus pushgateway

---
spec: ["002-push-sync-health-metrics"]
status: draft
created: "2026-09-02T20:10:00Z"
---

<summary>
- Watch mode now exports two Prometheus series per vault to the configured pushgateway: a heartbeat pushed on every watch cycle regardless of sync outcome, and a last-success timestamp pushed only after a successful push to the remote
- Both series carry a stable, human-readable vault label derived from the repository directory name (not the full path)
- Each vault's metrics are stored under its own pushgateway grouping key, so vaults can never overwrite each other's series
- With no pushgateway configured, metric pushing is fully disabled with zero network calls, and a startup warning explains how to enable it
- A failed, rejected, or timed-out metric push is logged as a warning and never interrupts the sync loop
- Pushgateway credentials are read from environment variables and never appear in logs
- Unit tests pin the exact metric names, push URL, body format, auth header, and per-cycle push counts (one heartbeat per cycle; last-success only after a successful sync)
- Documentation records the new environment variables and the one-watcher-per-vault alert caveat
</summary>

<objective>
Export `git_ai_sync_heartbeat_timestamp` and `git_ai_sync_last_success_timestamp` per vault from `git-ai-sync watch` to the existing Prometheus pushgateway, so a wedged vault (sync failing while the service keeps running) becomes alertable via age-of-last-success while staying silent during laptop-asleep periods. The push is best-effort: failures are logged and never break the sync loop, and the whole feature is inert when no pushgateway is configured.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff strict, mypy strict, pytest).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-pydantic-guide.md` — specifically the "optional-needs-default" rule: an optional (`T | None`) Config field MUST be declared with `= None` or `= Field(default=None)` or instantiation raises `ValidationError`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` section convention (insert directly above the highest `## vX.Y.Z`, after the SemVer preamble).

Read `docs/dod.md` — the Definition of Done checklist (type hints, docstrings, functions over classes, no absolute paths, existing tests keep passing).

Read these source files before writing anything:
- `src/git_ai_sync/config.py` — the `Config` class: pydantic-settings `BaseSettings` with `env_prefix="GIT_AI_SYNC_"`, `extra="ignore"`, optional fields declared as `str | None = Field(default=None, ...)`. This is the pattern the three new pushgateway fields must follow.
- `src/git_ai_sync/__main__.py` — `cmd_watch`: the watch loop (sleep → debounce skip → commit/pull/push). Note the local-import style (`from git_ai_sync import metrics` goes INSIDE `cmd_watch`, matching every other cmd_* import). The two successful-push sites are: the `is_ahead_of_remote` branch ("Local branch is ahead of remote, pushing...") and the final "Push to remote" block. The sync failure handling is the surrounding `except git_operations.GitError` / `except Exception` blocks.
- `src/git_ai_sync/git_operations.py` — `push` and the module style (module-level functions, full type hints, docstrings).
- `tests/test_main.py` — `TestCmdWatchDispatch` (the `sleep_side_effect = [None, KeyboardInterrupt()]` loop-termination pattern, `_GIT_OPS`/`_CONFIG`/`_ACQUIRE_LOCK` patch constants, `_mock_git_ops()`), `TestLockAcquisition`.
- `tests/test_config.py` — `TestConfigDefaults.test_defaults` (delenv pattern) and `TestConfigFromEnv`.
- `README.md` — the Configuration table.

Pushgateway protocol contract the code must implement (from the Prometheus pushgateway HTTP API):
- Push endpoint: `PUT /metrics/job/<job>[/<label>/<value>...]` with the body in Prometheus text exposition format. Labels in the URL path form the grouping key; the pushgateway adds them to every stored series.
- The pushgateway REJECTS a push (HTTP 400) when a metric-level label name in the body collides with a grouping-key label name from the URL path. Therefore the `vault` label must live ONLY in the body, and per-vault grouping must use a DIFFERENT URL label — `instance`. Do not "simplify" this to `/metrics/job/git_ai_sync/vault/<name>`; that shape 400s in production and the alert rule would never see a series.
- Basic auth is standard HTTP Basic; a non-2xx status (e.g. 401) surfaces as `urllib.error.HTTPError`.
</context>

<requirements>
1. **Add pushgateway config fields to `src/git_ai_sync/config.py`.** Append a "Pushgateway metrics" block to the `Config` class (after the Logging section, keeping the existing `# Logging` comment and `log_level` field unchanged):

   ```python
   # Pushgateway metrics
   pushgateway_url: str | None = Field(
       default=None,
       description="Prometheus pushgateway base URL (e.g. https://pushgateway.dev.nuke.benjamin-borbe.de)",
   )
   pushgateway_username: str | None = Field(
       default=None,
       description="Pushgateway basic-auth username",
   )
   pushgateway_password: str | None = Field(
       default=None,
       description="Pushgateway basic-auth password",
   )
   ```

   The `env_prefix="GIT_AI_SYNC_"` already maps these to `GIT_AI_SYNC_PUSHGATEWAY_URL`, `GIT_AI_SYNC_PUSHGATEWAY_USERNAME`, `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD`. Every `str | None` field MUST carry `default=None` (see python-pydantic-guide optional-needs-default rule).

2. **Create `src/git_ai_sync/metrics.py`** — a new module following the existing codebase pattern: module-level functions (no classes), full type hints, docstrings on all public functions, `logger = logging.getLogger(__name__)`. No absolute paths (paths via `Path`). No `print()` (enforced by `tests/test_no_print.py`).

   Module-level constants:
   - `JOB = "git_ai_sync"`
   - `HEARTBEAT_METRIC = "git_ai_sync_heartbeat_timestamp"`
   - `LAST_SUCCESS_METRIC = "git_ai_sync_last_success_timestamp"`
   - `PUSH_TIMEOUT_SECONDS = 5`

   Public functions (exact signatures):
   ```python
   def vault_label(repo_path: Path) -> str:
       """Stable, human-readable vault label: the repository directory name, not the full path."""

   def push_heartbeat(
       pushgateway_url: str,
       username: str | None,
       password: str | None,
       repo_path: Path,
   ) -> bool:
       """Push git_ai_sync_heartbeat_timestamp to the pushgateway. Returns True on success, False on any failure (logged at WARNING). Never raises."""

   def push_last_success(
       pushgateway_url: str,
       username: str | None,
       password: str | None,
       repo_path: Path,
   ) -> bool:
       """Push git_ai_sync_last_success_timestamp to the pushgateway. Returns True on success, False on any failure (logged at WARNING). Never raises."""
   ```

   Implement one private helper `_push_metric(pushgateway_url: str, username: str | None, password: str | None, metric_name: str, vault: str) -> bool` (fully type-annotated — mypy strict runs with `disallow_untyped_defs = true`) shared by both public functions. The two public functions are thin wrappers that pass `vault_label(repo_path)` and their fixed metric constant.

   Push semantics (implement exactly):
   - Build the URL: `base = pushgateway_url.rstrip("/")` (tolerates a trailing slash in the env value), then `f"{base}/metrics/job/{JOB}/instance/{quote(vault, safe='')}"` with `from urllib.parse import quote`. The vault is URL-quoted so names with spaces/slashes survive in the path. Grouping key = `instance=<vault>`; the `vault` label appears ONLY in the body (see the collision constraint in `<context>`).
   - Build the body (Prometheus text exposition format, UTF-8, timestamp `int(time.time())`). For the heartbeat it must look exactly like this (same shape for last-success with its metric name):
     ```
     # TYPE git_ai_sync_heartbeat_timestamp gauge
     git_ai_sync_heartbeat_timestamp{vault="Personal"} 1720000000
     ```
     The label value must be escaped for the text format: `\` → `\\`, `"` → `\"` before wrapping in quotes.
   - Build a `urllib.request.Request` with `method="PUT"`, `data=body.encode("utf-8")`, and headers `Content-Type: text/plain; version=0.0.4` plus — only when `username` is not None — `Authorization: Basic <base64(username:password)>` (build the token with `base64.b64encode(f"{username}:{password}".encode()).decode()`).
   - Send via `urllib.request.urlopen(request, timeout=PUSH_TIMEOUT_SECONDS)`. Success is "no exception raised" (a 401/400/503 raises `urllib.error.HTTPError`, a timeout/refusal raises `URLError`/`TimeoutError`, all caught). Return `True` on success.
   - On ANY exception: `logger.warning(f"metric push failed for {metric_name}: {e}")` — the substring `metric push failed` is load-bearing (spec AC 5 greps the run log for it) — then return `False`. Never re-raise. Never log `username` or `password`. No retry (retry-storm is explicitly forbidden by the spec; the next watch cycle pushes again).
   - Imports: `base64`, `logging`, `time`, `Path` from pathlib, `quote` from `urllib.parse`, `Request`, `urlopen` from `urllib.request`.

3. **Wire metric pushes into `cmd_watch` in `src/git_ai_sync/__main__.py`.** Use the local-import style: add `from git_ai_sync import metrics` inside `cmd_watch`.

   a. **Startup enablement log.** After the instance-lock `try/except LockError` block and before `tracker = ChangeTracker(git_repo)`:
      ```python
      if config.pushgateway_url:
          logger.info(f"Pushgateway metrics enabled: {config.pushgateway_url}")
      else:
          logger.warning("pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL")
      ```
      The WARNING text is exact (including the em-dash) — spec AC 5 asserts it. Logging the URL is fine (it is not a secret); never log `config.pushgateway_username` or `config.pushgateway_password`.

   b. **Heartbeat per watch cycle.** In the `while True` loop, immediately after `time.sleep(interval)` and BEFORE the `seconds_since_change < interval` debounce-skip check, push the heartbeat when configured:
      ```python
      if config.pushgateway_url:
          metrics.push_heartbeat(
              config.pushgateway_url,
              config.pushgateway_username,
              config.pushgateway_password,
              git_repo,
          )
      ```
      Placing it before the skip check means a debounce-skipped cycle still emits a heartbeat — "on every watch cycle" per spec Desired Behavior 1.

   c. **Last-success after successful sync.** After EACH successful `git_operations.push(git_repo)` call — both in the `is_ahead_of_remote` branch (after `logger.info("Pushed to remote")`) and in the final "Push to remote" block (after `logger.info("Pushed to remote")`) — push last-success when configured, same argument shape as (b) with `metrics.push_last_success(...)`.

   d. Do NOT wrap these calls in try/except — `push_heartbeat`/`push_last_success` never raise (they self-log and return bool). A failed push therefore cannot abort, retry-storm, or otherwise affect the sync cycle (spec Failure Mode "Pushgateway unreachable").

4. **Scope boundary:** metric pushing is wired ONLY into `cmd_watch`. Do NOT add metric calls to `cmd_sync`, `cmd_resolve`, or any other command (spec wires watch only).

5. **Create `tests/test_metrics.py`** — pytest style, `unittest.mock` (no unittest classes), following the project's test conventions. Patch target for the HTTP call: `git_ai_sync.metrics.urlopen` (the name the module imported from `urllib.request`). The `Request` object is the first positional arg of the urlopen mock; inspect `req.full_url`, `req.method`, `req.headers` (dict-like), and `req.data` (bytes). Enumerate these tests:

   a. `test_vault_label_is_directory_name` — `vault_label(Path("/a/b/Personal")) == "Personal"` (never the full path).

   b. `test_push_heartbeat_url_method_and_auth` — patch `git_ai_sync.metrics.urlopen` (return a `MagicMock()`) and `git_ai_sync.metrics.time.time` (return `1720000000.0`); call `push_heartbeat("https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal"))`; assert:
      - returns `True`
      - `req.full_url == "https://pushgateway.test/metrics/job/git_ai_sync/instance/Personal"`
      - `req.method == "PUT"`
      - `req.headers["Authorization"] == "Basic " + base64.b64encode(b"monitoring:s3cret").decode()`
      - `req.headers["Content-Type"] == "text/plain; version=0.0.4"`
      - `req.data.decode()` contains `# TYPE git_ai_sync_heartbeat_timestamp gauge` and `git_ai_sync_heartbeat_timestamp{vault="Personal"} 1720000000`

   c. `test_push_heartbeat_tolerates_trailing_slash` — base URL `"https://pushgateway.test/"` → `full_url` has exactly one `/metrics/...` slash (`https://pushgateway.test/metrics/...`).

   d. `test_push_heartbeat_url_encodes_vault_in_path_only` — vault `Path("/vaults/My Vault")` → path contains `instance/My%20Vault`, while the body label stays readable: `vault="My Vault"`.

   e. `test_push_heartbeat_escapes_quote_in_label_value` — a vault name containing `"` → body label value escapes it (`\"`), and the push does not break.

   f. `test_push_heartbeat_no_auth_header_without_username` — `username=None, password=None` → `Authorization` not present in `req.headers`.

   g. `test_push_last_success_metric_name` — same shape as (b) but `push_last_success` → body contains `# TYPE git_ai_sync_last_success_timestamp gauge` and `git_ai_sync_last_success_timestamp{vault="Personal"} 1720000000`.

   h. `test_push_failure_returns_false_and_logs_warning` — `urlopen` raises `urllib.error.HTTPError` (e.g. `HTTPError(req.full_url, 401, "Unauthorized", {}, None)`) → returns `False`, caplog records a WARNING whose message contains `metric push failed`, and the password string `"s3cret"` does NOT appear anywhere in caplog text.

   i. `test_push_timeout_returns_false` — `urlopen` raises `TimeoutError` → returns `False`, no exception propagates.

   i2. `test_push_unreachable_returns_false` — `urlopen` raises `urllib.error.URLError("connection refused")` (the spec's headline failure mode: pushgateway unreachable) → returns `False`, WARNING contains `metric push failed`, no exception propagates.

   j. `test_push_never_raises_on_arbitrary_error` — `urlopen` raises `RuntimeError("boom")` → returns `False`, no exception propagates, WARNING contains `metric push failed`.

6. **Update `tests/test_config.py`.**
   - In `TestConfigDefaults.test_defaults`, add `monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_URL", raising=False)`, `monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_USERNAME", raising=False)`, `monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_PASSWORD", raising=False)`, and assert `config.pushgateway_url is None`, `config.pushgateway_username is None`, `config.pushgateway_password is None`.
   - Add a `TestConfigFromEnv` test `test_pushgateway_from_env` setting all three vars and asserting the fields are populated.

7. **Update `tests/test_main.py`.** The existing `TestCmdWatchDispatch` tests patch `git_ai_sync.config.Config` with a plain `MagicMock`, whose `pushgateway_url` attribute would be truthy — so the new wiring would attempt a push with a garbage URL in those tests. Update them AND add the new coverage:

   a. Update BOTH existing `TestCmdWatchDispatch` tests (`test_cmd_watch_merge_strategy`, `test_cmd_watch_rebase_strategy`) to bind the config mock and disable metrics explicitly:
      ```python
      with (
          patch(_GIT_OPS, mock_git),
          patch(_CONFIG) as mock_config,
          patch(_ACQUIRE_LOCK),
          patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
          patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
          ...
      ):
          mock_config.return_value.pushgateway_url = None
          ...
          mock_heartbeat.assert_not_called()
          mock_last_success.assert_not_called()
      ```
      (Asserting zero calls in these tests doubles as the spec AC 2 "zero pushes when the pushgateway is unconfigured" check.)

   b. Add `test_watch_heartbeat_one_per_cycle` — patch `git_ai_sync.metrics.push_heartbeat` and `git_ai_sync.metrics.push_last_success` as in 7a; config mock with `pushgateway_url="https://pushgateway.test"`, `has_local_changes=False`, `is_ahead_of_remote=False`, one loop iteration (same `sleep_side_effect = [None, KeyboardInterrupt()]` + `contextlib.suppress(KeyboardInterrupt)` pattern) → `push_heartbeat.assert_called_once()` with `pushgateway_url` as its first positional arg and `Path("/repo")` as its last; `push_last_success.assert_not_called()`; `mock_git.push.assert_not_called()`.

   c. Add `test_watch_heartbeat_on_debounce_skip_cycle` — same patches as (b); `get_seconds_since_last_change` returns a value below the interval (e.g. `5` with interval 30) so the cycle takes the debounce-skip `continue` → `push_heartbeat.assert_called_once()`, `push_last_success.assert_not_called()`. This pins the placement requirement 3b (heartbeat before the skip check).

   d. Add `test_watch_last_success_after_successful_sync` — same patches as (b); config mock with URL set, `has_local_changes=True`, `generate_commit_message` returning a fixed string, pull succeeds, push succeeds (default mock behavior) → after one cycle: `push_heartbeat.assert_called_once()` AND `push_last_success.assert_called_once()` (both called with the URL as first arg).

   e. Add `test_watch_last_success_zero_after_failed_sync` — same patches as (b); same as (d) but `mock_git.push.side_effect = GitError("Failed to push: boom")` → after one cycle: `push_heartbeat.assert_called_once()` (heartbeat is cycle-level, unaffected by failure) and `push_last_success.assert_not_called()`. This is the spec AC 2 "zero after a failed one" and Failure Mode "Vault wedged while laptop awake".

   f. Add `test_watch_startup_warning_when_unconfigured` (caplog) — same patches as (b); config mock with `pushgateway_url=None`, one cycle → caplog contains a WARNING record with message `pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL`, and NO caplog record anywhere contains `metric push failed` (spec AC 5 negative).

8. **Update `README.md`.**
   - Configuration table: add rows
     `| GIT_AI_SYNC_PUSHGATEWAY_URL | Prometheus pushgateway base URL (e.g. https://pushgateway.dev.nuke.benjamin-borbe.de) | unset (disabled) |`,
     `| GIT_AI_SYNC_PUSHGATEWAY_USERNAME | Pushgateway basic-auth username | unset |`,
     `| GIT_AI_SYNC_PUSHGATEWAY_PASSWORD | Pushgateway basic-auth password | unset |`.
   - Add a short "Metrics" section stating: `git-ai-sync watch` pushes `git_ai_sync_heartbeat_timestamp` on every watch cycle and `git_ai_sync_last_success_timestamp` after each successful sync, per vault; a fresh heartbeat with a stale last-success means the vault has stopped landing commits while the watcher is alive; credentials come from the environment and are never logged; and — load-bearing caveat from the spec's Failure Modes — exactly ONE watcher per vault is required, because two watchers pushing the same vault overwrite each other's series and can mask a wedge.

9. **Update `CHANGELOG.md`.** Add a `## Unreleased` section directly above `## v0.8.0` (the file currently has `# Changelog` + SemVer preamble + `## v0.8.0`, no Unreleased section) with the entry:
   ```
   - feat: `git-ai-sync watch` pushes per-vault heartbeat and last-success timestamps to the Prometheus pushgateway (env `GIT_AI_SYNC_PUSHGATEWAY_URL`/`_USERNAME`/`_PASSWORD`; disabled when unconfigured; failed pushes are logged and never break the sync loop) so a wedged vault is alertable via age-of-last-success while laptop-asleep periods stay silent
   ```

10. **Self-check before finishing:** re-run the `<verification>` block and confirm every command passes; then walk each of spec AC 1, 2, and 5 against the change and confirm the behavior is covered by a test.
</requirements>

<constraints>
- Reuse the existing `monitoring/pushgateway` (ClusterIP 9090, nuke prod + dev) — do NOT deploy a new one; from the laptop the push must use the traefik ingress URL `https://pushgateway.{dev,prod}.nuke.benjamin-borbe.de` with the shared monitoring basic auth — the in-cluster `http://pushgateway.monitoring:9090` name does not resolve outside the cluster
- Metric names are EXACTLY `git_ai_sync_heartbeat_timestamp` and `git_ai_sync_last_success_timestamp`; the alert rule in the nuke repo depends on these exact names
- Per-vault isolation: each watched vault pushes under its own grouping key so one vault's metrics do not overwrite another's — use the `instance` URL label for the grouping key; never put `vault` in the URL path (pushgateway rejects label-name collisions between the body and the grouping key)
- The heartbeat push must tolerate gateway unavailability and never block the sync cycle — no retry storm, no exception can escape `push_heartbeat`/`push_last_success`
- Credentials are read from environment variables, never logged, never written to disk by the app
- Push scope is only the two git-ai-sync series, per vault, to the configured gateway — no arbitrary metric injection
- The push goes over TLS (urllib default HTTPS with certificate verification) and basic auth
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Functions over classes for stateless operations (existing codebase pattern)
- No absolute paths in code — paths via `Path`
- Metric wiring is watch-only: do NOT push metrics from `cmd_sync`, `cmd_resolve`, or any other command
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Type hints required on all functions (mypy strict); docstrings on all public functions per `docs/dod.md`; ruff line-length 100
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).

Additional verification:
- `uv run pytest tests/test_metrics.py -v` — all new metric-push tests pass.
- `uv run pytest tests/test_main.py -k watch -v` — updated + new watch wiring tests pass.
- `uv run pytest tests/test_config.py -v` — config defaults + env tests pass.
- `grep -rn 'git_ai_sync_last_success_timestamp' src/` — the exact metric name exists in the source (spec container verification).
- `grep -rn 'git_ai_sync_heartbeat_timestamp' src/` — both series names present.
</verification>
