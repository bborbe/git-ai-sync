---
status: approved
created: "2026-09-02T20:35:03Z"
queued: "2026-09-02T20:35:03Z"
---
# Rotating log file handler for bounded log output

---
spec: ["002-push-sync-health-metrics"]
status: draft
created: "2026-09-02T20:11:00Z"
---

<summary>
- Logging can now write to a rotating file capped at 5 MB per file with up to 5 backups, so long-running watch services cannot grow logs unbounded (previously observed at 27 MB unrotated)
- Operators enable rotation by setting an environment variable to a log file path
- Without the variable set, all commands behave exactly as before (stderr only) — backward compatible
- A test forces rotation by writing more than 5 MB through the logger and verifies the live file stays capped with a bounded backup count
- The new environment variable is documented in the README; the change is recorded in the changelog
</summary>

<objective>
Cap git-ai-sync log output via a `RotatingFileHandler` so a long-running `git-ai-sync watch` service cannot grow an unbounded log file (observed 27 MB unrotated). The rotating file handler is enabled when a log file path is configured and is fully inert otherwise, so one-shot CLI commands and existing setups are unaffected.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff strict, mypy strict, pytest).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-logging-guide.md` — the `RotatingFileHandler` pattern (import `from logging.handlers import RotatingFileHandler`; `maxBytes` + `backupCount` rotation semantics; attach to the root logger).

Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-pydantic-guide.md` — the "optional-needs-default" rule for the new optional Config field.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` section convention (insert directly above the highest `## vX.Y.Z`, after the SemVer preamble).

Read `docs/dod.md` — the Definition of Done checklist (type hints, docstrings, no absolute paths — paths via `Path`).

Read these source files before writing anything:
- `src/git_ai_sync/logging_setup.py` — the `configure_logging(level: str = "INFO") -> None` function calling `logging.basicConfig(format=..., level=..., datefmt=...)`.
- `src/git_ai_sync/__main__.py` — `main()`, which currently calls `configure_logging(args.log_level)` after `parse_args()` and before `setup_signal_handlers()`.
- `src/git_ai_sync/config.py` — the `Config` class pattern for the new `log_file` field (`str | None = Field(default=None, ...)`, `env_prefix="GIT_AI_SYNC_"` maps `GIT_AI_SYNC_LOG_FILE`).
- `tests/test_logging_setup.py` — the five existing tests patch `logging.basicConfig` and assert on its kwargs; they must keep passing unchanged.
- `tests/test_config.py` — `TestConfigDefaults.test_defaults` (delenv pattern) and `TestConfigFromEnv`.
- `README.md` — the Configuration table.

Note on the observed problem: `docs/launchd-service.md` and `docs/systemd-user-service.md` show services writing logs via OS-level redirection (`/tmp/git-ai-sync-obsidian.log`), which grows unrotated. This change adds an in-app rotating file handler so the operator can point `GIT_AI_SYNC_LOG_FILE` at a bounded file. Do NOT rewrite those service docs in this prompt — the operator applies the env var in their service definition.
</context>

<requirements>
1. **Add a `log_file` field to `src/git_ai_sync/config.py`.** Append to the `Config` class (after the pushgateway fields added by the sibling prompt, or after the Logging section if they are absent — keep existing fields unchanged):

   ```python
   # Logging
   log_file: str | None = Field(
       default=None,
       description="Log file path for the rotating file handler (GIT_AI_SYNC_LOG_FILE)",
   )
   ```

   `env_prefix="GIT_AI_SYNC_"` maps it to `GIT_AI_SYNC_LOG_FILE`. The `str | None` field MUST carry `default=None` (python-pydantic-guide optional-needs-default rule).

2. **Update `configure_logging` in `src/git_ai_sync/logging_setup.py`.** Change the signature to:

   ```python
   def configure_logging(level: str = "INFO", log_file: Path | None = None) -> None:
       """Configure logging for the application.

       Args:
           level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
           log_file: Optional log file path; when set, log output is also written to
               this file via a RotatingFileHandler (maxBytes=5_000_000, backupCount=5).
       """
   ```

   Behavior — **follow the python-logging-guide "Extract Logging Configuration to Dedicated Module" pattern (L96-152); NEVER combine `basicConfig` with manual `root.addHandler(...)`** (the guide's MUST constraint at L213-236 forbids that exact combination — its BAD example is `logging.basicConfig(...)` followed by `logging.root.addHandler(my_handler)`):
   - When `log_file` is None: call `logging.basicConfig(format=..., level=..., datefmt=...)` exactly as today — the existing five tests in `tests/test_logging_setup.py` assert its kwargs and must keep passing unchanged (they assert only the `level`/`format`/`datefmt` keys, so the `handlers=` kwarg in the other branch does not break them). Do not alter the format string, `datefmt`, or the level resolution.
   - When `log_file` is not None: create `log_path = Path(log_file)`; ensure the parent exists via `log_path.parent.mkdir(parents=True, exist_ok=True)`; build a handlers list of `[logging.StreamHandler(sys.stderr), RotatingFileHandler(log_path, maxBytes=5_000_000, backupCount=5, encoding="utf-8")]`; call `setFormatter(logging.Formatter(<same format string>, <same datefmt>))` on EACH handler; then configure the root logger via `logging.basicConfig(level=..., format=..., datefmt=..., handlers=handlers, force=True)` — `force=True` is mandatory: under pytest the root logger already carries pytest's own handlers, so a `basicConfig` without `force=True` is a silent no-op and the file handler never attaches (this is what makes the new tests 4b/4c fail). `maxBytes=5_000_000` and `backupCount=5` are exact values from spec AC 6 — do not change them. Import `from logging.handlers import RotatingFileHandler`, `import sys`, and `from pathlib import Path`.
   - When `log_file` is None: add NO file handler (current behavior preserved).

3. **Wire the log file into `main()` in `src/git_ai_sync/__main__.py`.** In `main()`, replace the current `configure_logging(args.log_level)` call with:

   ```python
   from git_ai_sync.config import Config
   from pathlib import Path

   config = Config()
   configure_logging(args.log_level, Path(config.log_file) if config.log_file else None)
   ```

   Keep `setup_signal_handlers()` right after. This applies rotation uniformly to every command (the long-running `watch` service is the real beneficiary).

4. **Update `tests/test_logging_setup.py`.** Keep all five existing tests unchanged. Add these tests (pytest style, `unittest.mock` where needed):

   a. `test_no_file_handler_without_log_file` — `configure_logging()` → the root logger has no `RotatingFileHandler` among its handlers.

   b. `test_file_handler_attached_with_rotation` — `configure_logging("INFO", tmp_path / "git-ai-sync.log")` → root logger has a `RotatingFileHandler` whose `maxBytes == 5_000_000` and `backupCount == 5`; log a message via `logging.getLogger("rotation_probe").info(...)` → the log file exists and its content contains the message.

   c. `test_rotation_caps_live_file_and_backups` (spec AC 6) — with the handler attached to `tmp_path / "git-ai-sync.log"`, write MORE than 5 MB through the logger: `logger = logging.getLogger("rotation_probe")` (the same name as 4b), then `for _ in range(12_000): logger.info("x" * 1000)` (≈12 MB with the format overhead, forcing at least two rotations). Then assert: the live file size (`(tmp_path / "git-ai-sync.log").stat().st_size`) is `<= 5_000_000`, and the number of rotated backups (`sorted(tmp_path.glob("git-ai-sync.log.*"))`) is `<= 5`.

   c2. `test_main_wires_log_file_into_configure_logging` — place these in `tests/test_main.py` (where the existing `TestMainDispatch` tests live) and give them the same scaffolding: patch `sys.argv` (e.g. `["git-ai-sync", "version"]` — `main()` calls `parse_args()` first, and without a patched argv the real `sys.argv` under pytest raises `SystemExit` from the `required=True` subparsers before `configure_logging` is ever invoked), patch `git_ai_sync.__main__.setup_signal_handlers`, patch `git_ai_sync.config.Config` to return a config with `log_file="/tmp/x/git-ai-sync.log"`, patch `git_ai_sync.__main__.configure_logging`, then invoke `main()` → `configure_logging` was called with `Path("/tmp/x/git-ai-sync.log")` as its second arg. Add `test_main_wires_none_when_log_file_unset` — config with `log_file=None` → second arg is `None`. (These cover the modified `main()` → `configure_logging` bridge in requirement 3.)

   d. **Handler cleanup** — because handlers attached to the root logger persist across tests in the same process, add a fixture that snapshots the root logger's handler list before the test and removes + closes any handler added during it (covers both the `RotatingFileHandler` and the `StreamHandler(sys.stderr)` that `force=True` installs). Imports needed: `from collections.abc import Generator`, `import logging`, `import logging.handlers`:
      ```python
      @pytest.fixture(autouse=True)
      def _cleanup_file_handlers() -> Generator[None]:
          original = list(logging.getLogger().handlers)
          yield
          root = logging.getLogger()
          for handler in list(root.handlers):
              if handler not in original:
                  handler.close()
                  root.removeHandler(handler)
      ```
      This prevents the rotation test's handlers from leaking into other tests.

5. **Update `tests/test_config.py`.**
   - In `TestConfigDefaults.test_defaults`, add `monkeypatch.delenv("GIT_AI_SYNC_LOG_FILE", raising=False)` and assert `config.log_file is None`.
   - Add a `TestConfigFromEnv` test `test_log_file_from_env` setting `GIT_AI_SYNC_LOG_FILE` to a path and asserting the field is populated.

6. **Update `README.md`.** Add a row to the Configuration table:
   `| GIT_AI_SYNC_LOG_FILE | Log file path for the rotating file handler (5 MB x 5 backups) | unset (stderr only) |`.

7. **Update `CHANGELOG.md`.** Add the entry under the `## Unreleased` section (create it directly above the highest `## vX.Y.Z` heading if it does not exist yet — the sibling prompt may have added it):
   ```
   - feat: Log output can be written to a rotating file (`RotatingFileHandler`, 5 MB x 5 backups) via `GIT_AI_SYNC_LOG_FILE` so long-running watch services cannot grow logs unbounded
   ```

8. **Self-check before finishing:** re-run the `<verification>` block and confirm every command passes; then confirm the rotation test actually forces at least one rotation (live file cap and backup-count cap are the spec AC 6 contract).
</requirements>

<constraints>
- Rotation parameters are EXACTLY `RotatingFileHandler(maxBytes=5_000_000, backupCount=5)` (spec AC 6) — no other values, no configurable maxBytes/backupCount knobs
- When `log_file` is unset, behavior is fully unchanged: stderr-only logging, no file handler, no file created
- No absolute paths in code — the log path is a `Path` parameter; the operator supplies the actual path via `GIT_AI_SYNC_LOG_FILE`
- Do NOT modify the existing `logging.basicConfig` format, `datefmt`, or level resolution — the five existing logging tests assert them
- Do NOT rewrite `docs/launchd-service.md` / `docs/systemd-user-service.md` in this prompt
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Type hints required on all functions (mypy strict); docstrings on public functions per `docs/dod.md`; ruff line-length 100
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).

Additional verification:
- `uv run pytest tests/test_logging_setup.py -v` — existing + new rotation tests pass.
- `uv run pytest tests/test_config.py -v` — config defaults + env tests pass.
- `grep -n 'RotatingFileHandler' src/git_ai_sync/logging_setup.py` — handler present.
- `grep -n 'maxBytes=5_000_000' src/git_ai_sync/logging_setup.py` — exact rotation cap present (spec AC 6).
</verification>
