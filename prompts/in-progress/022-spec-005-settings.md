---
status: approved
spec: [005-consecutive-failure-attention-alert]
created: "2026-10-11T16:26:06Z"
queued: "2026-10-11T18:27:07Z"
---

# Declare the stall-alert settings and thread them through setup-launchd

<summary>
- Operators can configure how many consecutive failed cycles count as a stall, with a sensible default
- Operators can point the notifier at a different attention-ask script or a different attention store
- The attention store URL resolves the very same environment variable the posting script itself reads, so a post and its close can never disagree about which store they are talking to
- The default script path is resolved from the home directory when the settings are read, not pinned to a version-numbered plugin path that changes on every plugin update
- A launchd-managed vault can set all three settings, exactly as it can already set the pushgateway settings
- Nothing about the threshold is enforced when the configuration is constructed — sync, status, and doctor are unaffected by a threshold they never read
- Existing behaviour is unchanged: with none of the new variables set, every existing default still holds
</summary>

<objective>
Add the three stall-alert settings to the pydantic settings model in `src/git_ai_sync/config.py`, and thread them through `setup-launchd`'s environment passthrough in `src/git_ai_sync/launchd.py`, with unit tests for both. The threshold's `>= 2` check is deliberately NOT here — it belongs to the `watch` startup path in a later prompt, so `sync`, `status`, and `doctor` stay unaffected by a value they never read.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff line-length 100, mypy strict, pytest with `asyncio_mode = "auto"`).

Read `docs/dod.md` — the Definition of Done that `make precommit` validates (docstrings on public functions, type hints everywhere, no absolute paths — all paths relative or via `Path`).

Read `src/git_ai_sync/config.py` — the whole file. Note `model_config = SettingsConfigDict(env_prefix="GIT_AI_SYNC_", env_file=".env", env_file_encoding="utf-8", extra="ignore")`, and the two precedents for reading a NON-prefixed environment variable: `anthropic_api_key` uses `alias="ANTHROPIC_API_KEY"` and `model` uses `validation_alias=AliasChoices("ANTHROPIC_MODEL", "GIT_AI_SYNC_MODEL")`. Add the new fields after the pushgateway block and before the logging block.

Read `src/git_ai_sync/launchd.py` — `_PUSHGATEWAY_ENV_KEYS` (module-level tuple), `generate_plist(binary, vault_dir, label, env=None)` (its `EnvironmentVariables` key comprehension is the passthrough), and the module docstring's last clause ("passes the pushgateway environment variables through only when set in the setup environment"). `setup_launchd(vault_dir)` calls `generate_plist(binary, vault_dir, label)` with `env` defaulting to `os.environ`, so no change is needed in `setup_launchd` itself — only the tuple and the docstrings.

Read `tests/test_config.py` — `TestConfigDefaults.test_defaults` (which deletes each env var with `monkeypatch.delenv(..., raising=False)` before asserting defaults) and `TestConfigFromEnv` (which sets one env var per test). Follow both shapes exactly; the defaults test must also delete the new variables.

Read `tests/test_launchd.py` — `TestGeneratePlist.test_full_plist_round_trip` (asserts the exact decoded plist dict for an env carrying the three pushgateway keys — this test must keep passing unchanged), `test_env_subset_url_only`, `test_env_none_set` (asserts `EnvironmentVariables == {}`), and `test_byte_identical_dumps`.

Read `CHANGELOG.md` — a `## Unreleased` section exists (created by the notifier prompt); append a bullet to it, do NOT create a second `## Unreleased`.
</context>

<requirements>
1. In `src/git_ai_sync/config.py`, add `from pathlib import Path` to the imports and a module-level private helper above `class Config`:

   ```python
   def _default_attention_script() -> str:
       """The supervisor plugin's attention-ask.py, resolved from the home directory at runtime.

       Deliberately the marketplace path, not the version-numbered plugin cache path,
       which changes on every plugin update.
       """
       return str(
           Path.home()
           / ".claude"
           / "plugins"
           / "marketplaces"
           / "claude-supervisor"
           / "scripts"
           / "attention-ask.py"
       )
   ```

   No user path literal anywhere: the path is built from `Path.home()` at the moment the settings are read.

2. In `src/git_ai_sync/config.py`, add a new settings block after the pushgateway fields (keep the existing `# Pushgateway metrics` comment and its fields exactly as they are):

   ```python
   # Consecutive-failure attention alert (watch mode only)
   stall_threshold: int = Field(
       default=10,
       description=(
           "Consecutive failed watch cycles before one attention card is posted"
           " (must be >= 2; the check is applied when watch starts)"
       ),
   )
   attention_script: str = Field(
       default_factory=_default_attention_script,
       description="Path to the supervisor attention-ask.py script (GIT_AI_SYNC_ATTENTION_SCRIPT)",
   )
   attention_store_url: str = Field(
       default="http://localhost:18080",
       alias="ATTENTION_STORE_URL",
       description="Attention store base URL — the same variable the posting script reads",
   )
   ```

   Exactly as written, with these properties:
   - `stall_threshold` is read from `GIT_AI_SYNC_STALL_THRESHOLD` through the model's `env_prefix`, default `10`. Do NOT add a pydantic `ge=2` constraint, a `field_validator`, or any other construction-time check — the `>= 2` rule is enforced only when `watch` starts, so `sync`, `status`, and `doctor` remain unaffected by a value they never read.
   - `attention_script` is read from `GIT_AI_SYNC_ATTENTION_SCRIPT` through the prefix, defaulting to the resolved `_default_attention_script()` value via `default_factory` (so `Path.home()` is evaluated per instantiation, not once at import).
   - `attention_store_url` deliberately carries NO `GIT_AI_SYNC_` prefix: it must resolve `ATTENTION_STORE_URL`, the very variable the posting script itself reads (`STORE = os.environ.get("ATTENTION_STORE_URL", "http://localhost:18080")` in `attention-ask.py`), defaulting to `http://localhost:18080` — so the post and the close can never disagree about which store they are talking to. Use `alias="ATTENTION_STORE_URL"`, mirroring `anthropic_api_key`. If the installed pydantic-settings version does not honour a plain `alias` under an `env_prefix` (the new env test in requirement 4 will tell you), switch the field to `validation_alias="ATTENTION_STORE_URL"` — that form is unambiguous and is the one `model` already uses for its non-prefixed variable.

3. In `src/git_ai_sync/launchd.py`, extend the passthrough so all three new settings reach a launchd-managed vault:

   - Replace the `_PUSHGATEWAY_ENV_KEYS` tuple with a single tuple covering every passed-through key, in this order, and update the `generate_plist` comprehension and both docstrings that name the pushgateway keys to match:

     ```python
     _PASSTHROUGH_ENV_KEYS = (
         "GIT_AI_SYNC_PUSHGATEWAY_URL",
         "GIT_AI_SYNC_PUSHGATEWAY_USERNAME",
         "GIT_AI_SYNC_PUSHGATEWAY_PASSWORD",
         "GIT_AI_SYNC_STALL_THRESHOLD",
         "GIT_AI_SYNC_ATTENTION_SCRIPT",
         "ATTENTION_STORE_URL",
     )
     ```

     Note the last key has no `GIT_AI_SYNC_` prefix — it is the same variable the posting script reads, so it must be passed through verbatim under its own name.
   - `generate_plist`'s `"EnvironmentVariables": {key: env[key] for key in _PASSTHROUGH_ENV_KEYS if key in env}` — each key present only when it was set in the setup environment (unchanged semantics, wider key set).
   - Update the module docstring's final clause (which today says only the pushgateway variables are passed through) and `generate_plist`'s docstring, to say the pushgateway and stall-alert variables are carried verbatim, each key present only when set in `env`. Keep the frozen plist shape (label, `ProgramArguments`, `KeepAlive`, `RunAtLoad`, the two log paths, and the soft/hard resource limits) exactly as it is — interval and strategy stay hardcoded.
   - Do NOT change `setup_launchd` or `remove_launchd`: `generate_plist` is already called with `env` defaulting to `os.environ`.

4. Extend `tests/test_config.py`:

   a. In `TestConfigDefaults.test_defaults`, delete the new variables too (`GIT_AI_SYNC_STALL_THRESHOLD`, `GIT_AI_SYNC_ATTENTION_SCRIPT`, `ATTENTION_STORE_URL`, each with `raising=False`) and assert the defaults: `config.stall_threshold == 10`, `config.attention_store_url == "http://localhost:18080"`, and `config.attention_script == str(Path.home() / ".claude" / "plugins" / "marketplaces" / "claude-supervisor" / "scripts" / "attention-ask.py")` — assert it against the same expression, never against a hardcoded `/Users/...` literal.

   b. In `TestConfigFromEnv`, add:
      - `test_stall_threshold_from_env` — `GIT_AI_SYNC_STALL_THRESHOLD=3` yields `config.stall_threshold == 3`.
      - `test_stall_threshold_one_is_not_rejected_at_construction` — `GIT_AI_SYNC_STALL_THRESHOLD=1` still constructs and yields `1`. This pins that the `>= 2` rule is NOT a construction-time constraint.
      - `test_attention_script_from_env` — `GIT_AI_SYNC_ATTENTION_SCRIPT=/tmp/attention-ask.py` yields that value.
      - `test_attention_store_url_from_env` — `ATTENTION_STORE_URL=http://store.test:9000` yields that value. Delete any pre-existing value for the variable first (`monkeypatch.delenv(..., raising=False)` where another test may have set it), and set only `ATTENTION_STORE_URL` so the test proves the non-prefixed name is the one that resolves.

5. Extend `tests/test_launchd.py` (`TestGeneratePlist`):

   - Keep `test_full_plist_round_trip`, `test_env_subset_url_only`, `test_env_none_set`, and `test_byte_identical_dumps` passing unchanged.
   - Add `test_full_plist_includes_stall_alert_env` — an env carrying all six keys produces an `EnvironmentVariables` dict with all six key/value pairs, byte-identical across two `generate_plist` calls with the same env.
   - Add `test_stall_alert_env_subset` — an env carrying only `GIT_AI_SYNC_STALL_THRESHOLD` and `ATTENTION_STORE_URL` produces exactly those two keys, and an env carrying only the three pushgateway keys still produces exactly those three (the pre-existing subset behaviour is not widened by accident).

6. Append this bullet to the existing `## Unreleased` section of `CHANGELOG.md` (do not create a second `## Unreleased`):

   ```
   - feat: Add `GIT_AI_SYNC_STALL_THRESHOLD`, `GIT_AI_SYNC_ATTENTION_SCRIPT`, and `ATTENTION_STORE_URL` settings and pass them through `setup-launchd` to the installed agent, so a launchd-managed vault can configure the consecutive-failure alert
   ```
</requirements>

<constraints>
- Python 3.14, `uv`, ruff, mypy **strict**, pytest with `asyncio_mode = "auto"`; `make precommit` stays green.
- No absolute paths hardcoded in source: the default script path is resolved from `Path.home()` at runtime, never written as a literal user path.
- The new configuration fields live in the existing pydantic settings model and are prefixed `GIT_AI_SYNC_`, EXCEPT the attention store URL, which must resolve `ATTENTION_STORE_URL` (default `http://localhost:18080`) — the same environment variable the posting script reads.
- Do NOT add a construction-time `>= 2` constraint, validator, or model-level check on the threshold: the check is applied only when `watch` starts (a later prompt).
- Do NOT make the dedup key, the vault label, or the producer id configurable.
- Do NOT add an enable/disable flag for the notifier — a missing script path is the disable path, and an escape hatch on the goal is itself a regression.
- Do NOT add a refresh-interval knob, a retry knob, or any other new setting beyond the three above.
- Do NOT change the frozen launchd plist shape (label, `ProgramArguments`, `KeepAlive`, `RunAtLoad`, log paths, resource limits) — only the passthrough key set and the docstrings.
- Do NOT change the pushgateway behaviour, metric names, or their per-vault grouping key.
- Do NOT modify `src/git_ai_sync/__main__.py` or `src/git_ai_sync/notifier.py` in this prompt.
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the settings tests pass:
`uv run pytest tests/test_config.py tests/test_launchd.py -v`
Confirm the three settings are declared in the source and documented in the launchd passthrough:
`grep -nE 'GIT_AI_SYNC_STALL_THRESHOLD|GIT_AI_SYNC_ATTENTION_SCRIPT|ATTENTION_STORE_URL' src/git_ai_sync/config.py src/git_ai_sync/launchd.py`
Confirm the settings resolve from the environment:
`uv run python -c "from git_ai_sync.config import Config; c = Config(); print(c.stall_threshold, c.attention_store_url, c.attention_script)"`
Confirm no construction-time threshold check was added:
`! grep -n 'ge=2' src/git_ai_sync/config.py`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change.
</verification>
