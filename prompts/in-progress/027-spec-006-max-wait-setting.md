---
status: approved
spec: [006-watch-quiet-window-decay]
created: "2026-10-23T16:00:34Z"
queued: "2026-10-23T16:09:35Z"
---

# Declare the maximum-wait setting, pass it to a launchd-managed vault, and document the debounce

<summary>
- Operators get one new setting that bounds how long the syncer may go without a successful sync on a vault that never goes quiet
- The setting has a documented default of 300 seconds and is read from the environment like every other setting
- An unset or odd value is honoured rather than rejected, so a bad value can never stop the syncer from syncing
- A launchd-managed vault can carry the setting through `setup-launchd`, exactly as it already carries the pushgateway and stall-alert settings
- The README describes what the debounce actually does now: a window that halves on every consecutive skip, plus a hard bound
- The project's design notes and the launchd service guide name the new setting, and the change carries a changelog entry
- Nothing about the watch loop changes in this step
</summary>

<objective>
Add the `max_wait` setting (environment variable `GIT_AI_SYNC_MAX_WAIT`, default `300`) to `src/git_ai_sync/config.py`, add it to the launchd passthrough allowlist in `src/git_ai_sync/launchd.py`, and document it in `README.md`, `CLAUDE.md`, and `docs/launchd-service.md`, with a `## Unreleased` CHANGELOG bullet and unit tests for the setting and the passthrough. **Why:** the decaying window needs a configurable hard bound, and a bound an operator cannot set on a managed service is not a bound.
</objective>

<context>
Read `CLAUDE.md` for project conventions (Python 3.14, `uv`, ruff line-length 100, mypy **strict**, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-pydantic-guide.md` for the pydantic-settings conventions this repo follows.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` before rewriting README prose.

Read `src/git_ai_sync/config.py` — the whole file. Note `model_config = SettingsConfigDict(env_prefix="GIT_AI_SYNC_", env_file=".env", env_file_encoding="utf-8", extra="ignore")` and the existing field style (`Field(default=..., description=...)`). The `interval` and `commit_prefix` fields sit in the `# Sync settings` block; the stall-alert fields sit in their own `# ... (watch mode only)` block. This repo adds no pydantic `ge=` / validator constraints to a setting whose invalid value must still be honoured — `stall_threshold` deliberately has none.

Read `src/git_ai_sync/launchd.py` — `_PASSTHROUGH_ENV_KEYS` (the module-level tuple), `generate_plist(binary, vault_dir, label, env=None)` and its `"EnvironmentVariables": {key: env[key] for key in _PASSTHROUGH_ENV_KEYS if key in env}` comprehension, the module docstring's final clause about the passed-through variables, and `generate_plist`'s own docstring. `setup_launchd(vault_dir)` calls `generate_plist(binary, vault_dir, label)` with `env` defaulting to `os.environ`, so neither `setup_launchd` nor `remove_launchd` changes.

Read `tests/test_config.py` — `TestConfigDefaults.test_defaults` (which `monkeypatch.delenv(..., raising=False)` each variable before asserting defaults) and `TestConfigFromEnv` (one variable per test, including `test_stall_threshold_one_is_not_rejected_at_construction`).

Read `tests/test_launchd.py` — `TestGeneratePlist`: `test_full_plist_round_trip`, `test_env_subset_url_only`, `test_env_none_set`, `test_byte_identical_dumps`, `test_full_plist_includes_stall_alert_env`, `test_stall_alert_env_subset`. All six must keep passing unchanged: none of their envs carries `GIT_AI_SYNC_MAX_WAIT`, so widening the tuple must not add a key to their results.

Read `README.md` — the `## How It Works` numbered list (item 2 is `**Debounce** - Skips sync when files changed recently (active editing)`), the `## Configuration` table, and the `## Troubleshooting` entry `**Changes not syncing** — Debounce may be active (files changed within the interval). ...`.

Read `docs/launchd-service.md` — `### The plist shape setup-launchd produces`, whose `EnvironmentVariables` bullet lists the passed-through keys verbatim.

Read `CHANGELOG.md` — there is NO `## Unreleased` section today; the newest section is `## v0.13.1`. Create `## Unreleased` directly above `## v0.13.1` and put the bullet in it. Do not rename, reorder, or edit any released section.

This prompt is deliberately independent of the watch loop: it must pass `make precommit` on its own, before the wiring lands.
</context>

<requirements>
1. In `src/git_ai_sync/config.py`, add one field. Put it in its own block immediately after the `# Sync settings` block (`interval`, `commit_prefix`) and before the Claude settings block:

   ```python
   # Watch debounce bound (watch mode only)
   max_wait: int = Field(
       default=300,
       description=(
           "Maximum seconds without a successful watch sync before one is forced"
           " regardless of how recently files changed (GIT_AI_SYNC_MAX_WAIT)"
       ),
   )
   ```

   Properties, exactly as written:
   - The type is `int`, not `float`: the documented default must print as `300`, not `300.0`.
   - It resolves from `GIT_AI_SYNC_MAX_WAIT` through the model's existing `env_prefix`. Do not add an `alias`, a `validation_alias`, or a second spelling.
   - No `ge=`, no `gt=`, no `field_validator`, no model-level check. A value of `0`, a negative value, or a value below the interval is honoured at load time; the watch loop is what gives it meaning, and the spec requires that a malformed or aggressive value can never stop the service from syncing.
   - Do not add any other setting. `interval`, `stall_threshold`, the pushgateway fields, and the attention fields keep their current defaults and descriptions unchanged.

2. In `src/git_ai_sync/launchd.py`, widen the passthrough so a launchd-managed vault can carry the setting:

   - Insert `"GIT_AI_SYNC_MAX_WAIT"` into `_PASSTHROUGH_ENV_KEYS` immediately after `"GIT_AI_SYNC_PUSHGATEWAY_PASSWORD"` and before `"GIT_AI_SYNC_STALL_THRESHOLD"`. Leave every existing key in its current relative order, and leave `ATTENTION_STORE_URL` last (it deliberately has no `GIT_AI_SYNC_` prefix).
   - Keep the `EnvironmentVariables` comprehension exactly as it is — each key is present only when it was set in the setup environment.
   - Update the module docstring's final clause and `generate_plist`'s docstring so they name the maximum-wait key alongside the pushgateway and stall-alert variables.
   - Do NOT change the frozen plist shape: the label, `ProgramArguments` (including the hardcoded `--interval 30 --strategy merge`), `KeepAlive`, `RunAtLoad`, the two log paths, and the soft/hard resource limits stay byte-identical.
   - Do NOT modify `setup_launchd` or `remove_launchd`.

3. Update `docs/launchd-service.md`: in `### The plist shape setup-launchd produces`, add `GIT_AI_SYNC_MAX_WAIT` to the `EnvironmentVariables` bullet's key list, keeping the existing sentence about each key being passed through only when set at setup time and the note about `ATTENTION_STORE_URL` having no prefix.

4. Update `README.md` so it describes what the debounce actually does now, and so the old one-line description is gone:

   a. Rewrite `## How It Works` item 2. It must no longer contain the phrase `Skips sync when files changed recently`. Suggested wording:

   ```
   2. **Debounce** - Waits for the vault to go quiet before syncing, halving the required quiet window on every consecutive skip and forcing a sync once the maximum wait elapses
   ```

   b. Add a new `## Debounce` section directly after `## How It Works` and before `## Configuration`. It must state, in flowing prose (no bullet-only stub), all of the following:
   - the required quiet window starts at the poll interval and halves on every consecutive skipped cycle, so a vault written continuously still becomes eligible to sync;
   - a successful sync resets the window to the full interval and restarts the wait clock;
   - `GIT_AI_SYNC_MAX_WAIT` forces a sync once that many seconds have passed since the last successful sync, regardless of how recently files changed, so the worst-case delay on a busy vault is the maximum wait rounded up to the next poll boundary (at most `max_wait + interval`);
   - a vault that goes quiet between cycles syncs exactly as it did before this behaviour existed;
   - the two mechanisms are distinguishable in the log: a skipped cycle reports the window currently in force, and a cycle forced by the bound says so.
   The words `halves` or `decay` must appear in this section.

   c. Update the `## Troubleshooting` entry `**Changes not syncing**` so it names the bound: the debounce may be active, but it cannot defer a sync beyond the maximum wait, and `git-ai-sync sync` still forces a one-time sync.

   d. Add the configuration-table row, placed with the other watch-related rows:

   ```
   | `GIT_AI_SYNC_MAX_WAIT` | Maximum seconds without a successful watch sync before one is forced regardless of how recently files changed | `300` |
   ```

   e. Do NOT touch `docs/systemd-user-service.md` — it documents no environment variables at all, and this spec explicitly leaves it alone.

5. Update `CLAUDE.md`'s `## Key Design Decisions` bullet for watch mode. It must name the decay and the bound, for example:

   ```
   - **Watch mode = poll + debounce with decay** — polls at interval, skips sync while files change, halves the required quiet window on each consecutive skip, and forces a sync once `max_wait` (`GIT_AI_SYNC_MAX_WAIT`, default 300 s) has elapsed since the last success
   ```

   The bullet must contain `decay` or `max_wait`. Leave every other `CLAUDE.md` section untouched.

6. In `CHANGELOG.md`, create a `## Unreleased` section immediately above `## v0.13.1` and add one bullet to it:

   ```
   ## Unreleased

   - feat: Bound the watch debounce — halve the required quiet window on every consecutive skipped cycle and force a sync once `GIT_AI_SYNC_MAX_WAIT` (default 300 s) has elapsed since the last success, so a vault written continuously still commits and pulls within `max_wait + interval`
   ```

   The bullet must contain `MAX_WAIT` and must sit under `## Unreleased`. Do not create a second `## Unreleased`, do not edit any released section, and do not bump the version.

7. Extend `tests/test_config.py`:

   a. In `TestConfigDefaults.test_defaults`, add `monkeypatch.delenv("GIT_AI_SYNC_MAX_WAIT", raising=False)` with the other deletions and assert `config.max_wait == 300`.

   b. In `TestConfigFromEnv`, add:
      - `test_max_wait_from_env` — `GIT_AI_SYNC_MAX_WAIT=10` yields `config.max_wait == 10`.
      - `test_max_wait_zero_is_not_rejected_at_construction` — `GIT_AI_SYNC_MAX_WAIT=0` still constructs and yields `0`, pinning that no construction-time constraint exists (the "always force" behaviour is decided by the watch loop, not by the settings model).

8. Extend `tests/test_launchd.py` (`TestGeneratePlist`) with `test_max_wait_env_passthrough` — an env carrying only `GIT_AI_SYNC_MAX_WAIT` produces `EnvironmentVariables == {"GIT_AI_SYNC_MAX_WAIT": "300"}`. Do not modify the six existing `TestGeneratePlist` tests; their envs must still produce exactly the key sets they assert today.
</requirements>

<constraints>
- Python 3.14, `uv`, ruff (line-length 100), mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- `docs/dod.md` applies in full: type hints on every function signature, docstrings on public functions, no unexplained `# type: ignore` or `noqa`, no absolute paths.
- Configuration stays on the existing pydantic-settings class with the `GIT_AI_SYNC_` env prefix.
- The setting's default is `300` and its type is `int`; the default must print as `300`.
- The value is honoured rather than rewritten or rejected — no validator, no clamping, no `ge=` constraint, no fallback to a "sane" value.
- Do NOT change the `interval` default or what `interval` means for a quiet vault.
- Do NOT modify `src/git_ai_sync/__main__.py` or `src/git_ai_sync/quiet_window.py`. The startup warning for a maximum wait below the interval, and the log lines that report the window in force, belong to the wiring prompt — they need the effective `--interval`, which only the watch startup knows.
- Do NOT change the pull strategy, the consecutive-failure counter, the stall-attention card, the pushgateway metrics, the pre-commit hook, or any behaviour of the `sync` / `resolve` / `status` / `doctor` / `version` subcommands.
- Do NOT document the setting in `docs/systemd-user-service.md`.
- Do NOT change the frozen launchd plist shape — only the passthrough key set and the docstrings that name the keys.
- Do NOT add a second setting, a decay-factor knob, a retry knob, or an enable/disable flag.
- Existing tests must still pass, including all six existing `TestGeneratePlist` tests.
- Do NOT commit — dark-factory handles git
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the setting tests and the plist passthrough tests pass:
`uv run pytest tests/test_config.py tests/test_launchd.py -v`
Confirm the documented default is an integer `300`:
`uv run python -c "from git_ai_sync.config import Config; print(Config().max_wait)"`
Confirm the setting exists in the source and in the launchd allowlist:
`grep -n 'max_wait' src/git_ai_sync/config.py`
`grep -n 'MAX_WAIT' src/git_ai_sync/launchd.py`
Confirm both docs name the variable:
`grep -n 'GIT_AI_SYNC_MAX_WAIT' README.md docs/launchd-service.md`
Confirm the debounce is described with the decay:
`grep -nE 'halves|decay' README.md`
`! grep -q 'Skips sync when files changed recently' README.md`
Confirm the design notes name the setting:
`grep -nE 'decay|max_wait' CLAUDE.md`
Confirm the changelog bullet sits under an unreleased heading:
`awk '/^## /{sec=$0} /MAX_WAIT/{print sec}' CHANGELOG.md` — must print `## Unreleased`
Confirm the systemd doc was left alone:
`! grep -q 'MAX_WAIT' docs/systemd-user-service.md`
Confirm the watch loop is untouched by this prompt:
`! grep -q 'max_wait' src/git_ai_sync/__main__.py`
Confirm the whole suite passes:
`uv run pytest tests/ -q`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change.
</verification>
