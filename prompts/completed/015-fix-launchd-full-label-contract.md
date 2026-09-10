---
status: completed
spec: [003-setup-launchd-and-brew-cask]
summary: 'Fixed launchd service addressing to use the full label (com.github.bborbe.git-ai-sync-<label>) in the print probe, kickstart, bootout, and manual-command messages; updated unit/e2e tests and the e2e fake launchctl shim to encode the contract, updated docs/launchd-service.md and added a fix: CHANGELOG Unreleased entry'
execution_id: git-ai-sync-exec-015-fix-launchd-full-label-contract
dark-factory-version: dev
created: "2026-09-10T20:54:54Z"
queued: "2026-09-10T20:57:26Z"
started: "2026-09-10T21:05:00Z"
completed: "2026-09-10T21:06:12Z"
---

# Fix launchd service addressing to use the full label

<summary>
- `setup-launchd` and `remove-launchd` now address launchd services by the FULL label (`com.github.bborbe.git-ai-sync-<label>`) instead of the short label (`<label>`) in all three launchctl calls: the already-loaded print probe, the kickstart, and the bootout
- The error message and log lines that print launchctl commands (the manual fallback `launchctl kickstart -k ...` and the "Agent already loaded" / "kickstart failed for" / "Registered launchd agent" lines) also show the full label, so the printed commands actually work when an operator runs them by hand
- Fresh setup on a real macOS launchd GUI domain no longer exits 1 with `Bad request. Could not find service "personal" in domain for user gui: 501` — the already-loaded tolerance now resolves the real job instead of probing a label that never exists
- `remove-launchd` bootouts the real service (`launchctl bootout gui/<uid> com.github.bborbe.git-ai-sync-<label>`), so teardown actually stops the agent
- The unit tests assert the full-label argv for the print probe, kickstart, and bootout (the old short-label assertions are gone)
- The e2e fake `launchctl` shim now only accepts full-label print/kickstart/bootout targets and fails short-label targets with a launchd-like `Could not find service` error, so the e2e suite encodes the real launchd contract and will fail if this regression ever returns
- The documented manual fallback kickstart command uses the full label too
- A `## Unreleased` CHANGELOG entry with a `fix:` bullet records the contract fix
- No new config fields, no new functions, no behavior knobs — the launchctl control flow, timeouts, and error handling are unchanged
</summary>

<objective>
Make `setup-launchd`/`remove-launchd` actually work against real launchd by addressing services with the full label (`com.github.bborbe.git-ai-sync-<label>`) — the only form launchd resolves — and lock that contract into the unit and e2e tests so the short-label bug cannot silently return.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff line-length 100, mypy strict, pytest + pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention (insert after the frozen preamble, directly above the highest `## vX.Y.Z`; flat list; conventional prefixes — use `fix:` for this bugfix) and `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow.
Read `docs/dod.md` — the Definition of Done that `make precommit` validates (type hints, docstrings, functions over classes, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code).
Read `src/git_ai_sync/launchd.py` — the module to fix. The launchctl calls live in `setup_launchd()` (`gui_domain = f"gui/{os.getuid()}"`; the bootstrap call; the print probe on the bootstrap-failure path; the kickstart call) and `remove_launchd()` (the bootout call). The plist `Label` value is already the full label `f"com.github.bborbe.git-ai-sync-{label}"` in `generate_plist()` — that is the value launchd registers under and the value the launchctl targets must match.
Read `tests/test_launchd.py` — the unit tests asserting launchctl argv (`_patch_subprocess_run`, `TestSetupLaunchd`, `TestRemoveLaunchd`).
Read `tests/test_launchd_e2e.py` — the fake launchctl shim (`_FAKE_LAUNCHCTL_SOURCE`) and the log-line assertions.
Read `docs/launchd-service.md` and `CHANGELOG.md` (the frozen preamble ends at `* PATCH version when you make backwards-compatible bug fixes.`; the highest released section is `## v0.11.0` — there is currently NO `## Unreleased` section).

<!-- OPEN QUESTIONS (reviewer decision points, resolved below — flag if you disagree):
     1. SCOPE — docs/launchd-service.md line ~134 documents the manual fallback kickstart as `launchctl kickstart -k gui/$(id -u)/<label>` (short label). That is the same defect: run manually, it fails exactly like the code did. It is fixed here as requirement 5 (one line, same contract) so the docs never tell an operator to run a command that fails. Strike requirement 5 if you want docs out of scope.
     2. APPROACH — the fix inlines the full-label f-strings at each call/message site rather than introducing a shared `_LABEL_PREFIX` constant or helper. Rationale: minimal diff on the audited launchd.py; `generate_plist()` and the frozen plist shape stay byte-untouched. The repetition is acceptable for a bugfix; a constant can come in a later refactor if wanted.
     3. OPERATOR RUNG — the live re-verification (run `git-ai-sync setup-launchd ~/Documents/Obsidian/Personal` twice → exit 0, then `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` shows the job) is deliberately NOT in this prompt. It lives in spec 003's Verification ladder and runs on the host after merge — the container has no launchd GUI domain. -->
</context>

<requirements>
The defect, proven live: launchd only resolves services by their FULL label (`com.github.bborbe.git-ai-sync-<label>`, the plist `Label` value that `generate_plist()` already produces). The current code addresses services by the SHORT label (`<label>`) in the print probe, kickstart, and bootout, so on real launchd the probe fails with `Bad request. Could not find service "personal" in domain for user gui: 501`, fresh setup exits 1 (both the fresh-setup path and the already-loaded tolerance path), and bootout targets the wrong service.

1. In `src/git_ai_sync/launchd.py`, change ONLY the service-target strings and the message strings that embed a service target to the full label. The launchctl control flow, the `timeout=30` values, the `check=False, capture_output=True, text=True` kwargs, and the error-handling semantics (probe-on-bootstrap-failure, `LaunchdError` on kickstart failure, best-effort bootout warning) must NOT change. Do NOT modify `generate_plist()` or the frozen plist shape.

   a. In `setup_launchd()` — the print probe (currently ~line 201):
   ```python
   # old
   ["launchctl", "print", f"{gui_domain}/{label}"],
   # new
   ["launchctl", "print", f"{gui_domain}/com.github.bborbe.git-ai-sync-{label}"],
   ```

   b. In `setup_launchd()` — the manual fallback kickstart command in the `LaunchdError` message (currently ~line 215):
   ```python
   # old
   f"  launchctl kickstart -k {gui_domain}/{label}"
   # new
   f"  launchctl kickstart -k {gui_domain}/com.github.bborbe.git-ai-sync-{label}"
   ```
   (ruff format may wrap this line — the wrapped form is fine; the string content above is the contract.)

   c. In `setup_launchd()` — the "Agent already loaded" log line (currently ~line 217):
   ```python
   # old
   logger.info(f"Agent already loaded: {gui_domain}/{label}")
   # new
   logger.info(f"Agent already loaded: {gui_domain}/com.github.bborbe.git-ai-sync-{label}")
   ```

   d. In `setup_launchd()` — the kickstart call (currently ~line 221):
   ```python
   # old
   ["launchctl", "kickstart", "-k", f"{gui_domain}/{label}"],
   # new
   ["launchctl", "kickstart", "-k", f"{gui_domain}/com.github.bborbe.git-ai-sync-{label}"],
   ```

   e. In `setup_launchd()` — the kickstart-failure message (currently ~line 232):
   ```python
   # old
   f"launchctl kickstart failed for {gui_domain}/{label}: {kickstart.stderr.strip()}"
   # new
   f"launchctl kickstart failed for {gui_domain}/com.github.bborbe.git-ai-sync-{label}: {kickstart.stderr.strip()}"
   ```

   f. In `setup_launchd()` — the "Registered launchd agent" log line (currently ~line 235):
   ```python
   # old
   logger.info(f"Registered launchd agent: {gui_domain}/{label}")
   # new
   logger.info(f"Registered launchd agent: {gui_domain}/com.github.bborbe.git-ai-sync-{label}")
   ```

   g. In `remove_launchd()` — the bootout call (currently ~line 256):
   ```python
   # old
   ["launchctl", "bootout", gui_domain, label],
   # new
   ["launchctl", "bootout", gui_domain, f"com.github.bborbe.git-ai-sync-{label}"],
   ```

   After this step, `grep -n '{gui_domain}/{label}' src/git_ai_sync/launchd.py` must return zero matches.

2. Update `tests/test_launchd.py` — every assertion that asserts a launchctl target must assert the FULL label form. The `_patch_subprocess_run` helper (which matches on `argv[1]` command name only) does NOT need to change.

   a. `TestSetupLaunchd.test_happy_path` (~line 193):
   ```python
   # old
   assert calls[1] == ["launchctl", "kickstart", "-k", f"gui/{os.getuid()}/vault"]
   # new
   assert calls[1] == [
       "launchctl", "kickstart", "-k", f"gui/{os.getuid()}/com.github.bborbe.git-ai-sync-vault"
   ]
   ```

   b. `TestSetupLaunchd.test_already_loaded_tolerated` (~lines 208-209) — this test was the one encoding the wrong contract (short-label probe in the subprocess mock):
   ```python
   # old
   assert calls[1] == ["launchctl", "print", f"gui/{os.getuid()}/vault"]
   assert calls[2] == ["launchctl", "kickstart", "-k", f"gui/{os.getuid()}/vault"]
   # new
   assert calls[1] == [
       "launchctl", "print", f"gui/{os.getuid()}/com.github.bborbe.git-ai-sync-vault"
   ]
   assert calls[2] == [
       "launchctl", "kickstart", "-k", f"gui/{os.getuid()}/com.github.bborbe.git-ai-sync-vault"
   ]
   ```

   c. `TestSetupLaunchd.test_hard_launchd_failure` (~line 225):
   ```python
   # old
   assert f"launchctl kickstart -k gui/{os.getuid()}/vault" in message
   # new
   assert f"launchctl kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-vault" in message
   ```

   d. `TestRemoveLaunchd` — all four bootout-argv assertions (`test_removes_job_and_plist` ~line 320, `test_bootout_nonzero_best_effort` ~line 333, `test_never_setup_no_exception` ~line 346, `test_plist_delete_failure_names_step` ~line 362):
   ```python
   # old
   assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "vault"]]
   # new
   assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "com.github.bborbe.git-ai-sync-vault"]]
   ```

3. Update `tests/test_launchd_e2e.py` so the e2e encodes the real launchd contract.

   a. Replace the entire `_FAKE_LAUNCHCTL_SOURCE` string with this version — it keeps the log line and the two env-var failure toggles, and adds the contract: for `print`, `kickstart`, and `bootout`, a target that is not a full label exits 1 with a launchd-like stderr message (short-label probes fail exactly like real launchd):
   ```python
   _FAKE_LAUNCHCTL_SOURCE = r"""#!/usr/bin/env python3
   import os
   import sys

   log = os.environ.get("FAKE_LAUNCHCTL_LOG")
   if log:
       with open(log, "a", encoding="utf-8") as f:
           f.write(" ".join(sys.argv[1:]) + "\n")

   command = sys.argv[1] if len(sys.argv) > 1 else ""
   if os.environ.get("FAKE_LAUNCHCTL_FAIL_BOOTSTRAP") and command == "bootstrap":
       sys.exit(1)
   if os.environ.get("FAKE_LAUNCHCTL_FAIL_PRINT") and command == "print":
       sys.exit(1)
   # Real launchd addresses services by their full label: a print/kickstart/bootout
   # target that is not a full label fails with "Could not find service".
   if (
       command in ("print", "kickstart", "bootout")
       and "com.github.bborbe.git-ai-sync-" not in sys.argv[-1]
   ):
       sys.stderr.write(
           'Bad request. Could not find service "%s" in domain for user gui: %s\n'
           % (sys.argv[-1], os.getuid())
       )
       sys.exit(1)
   sys.exit(0)
   """
   ```

   b. Update the log-line assertions to the full-label form:
      - `test_setup_writes_plist_and_bootstraps` (~lines 114-117): the second expected line becomes `f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal"` (the bootstrap line is unchanged — its target is the plist path).
      - `test_setup_spaced_dir_derives_label_at_cli_layer` (~line 136): `assert f"kickstart -k gui/{os.getuid()}/path-with-spaces" in lines` becomes `assert f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-path-with-spaces" in lines`.
      - `test_setup_tolerates_already_loaded_job` (~lines 178-179): `assert f"print gui/{os.getuid()}/personal" in lines` becomes `assert f"print gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal" in lines`; `assert f"kickstart -k gui/{os.getuid()}/personal" in lines` becomes `assert f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal" in lines`.
      - `test_remove_deletes_plist_and_bootouts` (~line 241): `assert lines[-1] == f"bootout gui/{os.getuid()} personal"` becomes `assert lines[-1] == f"bootout gui/{os.getuid()} com.github.bborbe.git-ai-sync-personal"`.
      - `test_setup_hard_launchd_failure_names_manual_commands` needs no assertion change (it asserts the manual-command text `launchctl bootstrap` / `launchctl kickstart` appears in the output, and the manual kickstart now uses the full label per requirement 1b).

   c. The existing e2e tests must pass unchanged in structure against the contract-enforcing fake: with a full-label probe/kickstart/bootout the fake exits 0, so `test_setup_tolerates_already_loaded_job`, the happy-path tests, and both remove tests still exit 0. If any test now fails with a `Could not find service` stderr line, the production target for that call is still the short label — fix it per requirement 1 before proceeding.

   d. Add one direct test of the fake's contract: run `[shim, "print", f"gui/{os.getuid()}/personal"]` as a subprocess and assert non-zero exit with `Could not find service` in stderr — locks the fake's rejection branch so a future weakening of the shim fails loudly.

4. Update `docs/launchd-service.md` — the manual fallback kickstart command in the "Setup fails in a sandboxed or SSH context" section (~line 134):
   ```bash
   # old
   launchctl kickstart -k gui/$(id -u)/<label>
   # new
   launchctl kickstart -k gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>
   ```
   Do NOT change anything else in the file — the verification example at ~line 94 (`launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>`) already uses the full label.

5. Update `CHANGELOG.md`: insert a `## Unreleased` section between the frozen preamble (ending at `* PATCH version when you make backwards-compatible bug fixes.`) and `## v0.11.0` (there is currently no `## Unreleased` — create it) with a single flat `fix:` bullet:
   ```
   - fix: `setup-launchd`/`remove-launchd` address launchd services by their full label (`com.github.bborbe.git-ai-sync-<label>`) in the already-loaded print probe, kickstart, bootout, and the printed manual fallback commands — the short label form fails on real launchd (`Could not find service`), which made fresh setup exit 1 and the cask postflight's registration fail silently
   ```
   Do NOT modify anything above the `# Changelog` title, the preamble, or the `## v0.11.0` section.

6. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk each changed launchctl target (probe, kickstart, bootout, manual-command messages) against the full-label contract and confirm no short-label form remains in `src/git_ai_sync/launchd.py` or the tests.
</requirements>

<constraints>
- The plist shape is a FROZEN constraint and must match the live template exactly: Label format `com.github.bborbe.git-ai-sync-<label>`; ProgramArguments `[<binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`; KeepAlive true; RunAtLoad true; StandardOutPath == StandardErrorPath == `/tmp/git-ai-sync-<label>.log`; SoftResourceLimits 1024 / 536870912; HardResourceLimits 2048 / 1073741824; EnvironmentVariables carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `_USERNAME` / `_PASSWORD` from the setup environment. `watch` and its defaults (`--interval 30 --strategy merge`) must not change. Do NOT modify `generate_plist()` or any plist content.
- The label rule must reproduce `personal` for `/Users/bborbe/Documents/Obsidian/Personal`. It deliberately does NOT reproduce the other hand-written labels (e.g. `Brogrammers` derives `brogrammers`, not the live `obsidian-brogrammers`) — those agents are out of scope.
- Existing CLI subcommands (`watch`, `sync`, `resolve`, `status`, `config`, `version`, `doctor`) and their behavior are unchanged.
- Do NOT add a launchd-setup opt-out flag, a configurable interval/strategy for `setup-launchd`, a label-prefix knob, or any tunable threshold — the frozen plist shape is fixed (spec Non-goal, hard veto).
- The launchctl control flow is unchanged: bootstrap → print-probe-on-bootstrap-failure → kickstart in `setup_launchd`; best-effort bootout warning in `remove_launchd`; every call keeps `check=False, capture_output=True, text=True, timeout=30`. Only the service-target strings and the messages embedding them change.
- Project DoD (`docs/dod.md`): `make precommit` green (ruff format + lint + mypy strict, pytest, pytest-asyncio `asyncio_mode = "auto"`), type hints and docstrings on all public functions, functions-over-classes for stateless operations, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code (all paths via `Path`).
- Never log the plist EnvironmentVariables values (the pushgateway password) — the only plist-derived logging is the resolved binary and label.
- Never call the real `launchctl` from tests — the e2e goes through the fake shim; tests must not require a macOS launchd domain.
- This prompt runs in a container with no launchd GUI domain and no git access guarantees — the live host re-verification (run `setup-launchd` twice on the host → exit 0, `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` shows the job loaded) is the spec's operator rung, NOT part of this prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the affected suites pass:
`uv run pytest tests/test_launchd.py tests/test_launchd_e2e.py -v`
Confirm no short-label service form remains in the code:
`grep -n '{gui_domain}/{label}' src/git_ai_sync/launchd.py` — must print 0 matches
`grep -n 'gui/{os.getuid()}/vault' tests/test_launchd.py` — must print 0 matches
`grep -n 'gui/{os.getuid()}/personal' tests/test_launchd_e2e.py` — must print 0 matches
`grep -n 'gui/$(id -u)/<label>' docs/launchd-service.md` — must print 0 matches
Confirm the full-label forms are in place:
`grep -n 'com.github.bborbe.git-ai-sync-{label}' src/git_ai_sync/launchd.py` — at least 3 matches (probe, kickstart, bootout, plus message lines)
`grep -n 'com.github.bborbe.git-ai-sync-vault' tests/test_launchd.py` — at least 5 matches
`grep -n 'com.github.bborbe.git-ai-sync-personal' tests/test_launchd_e2e.py` — at least 4 matches
Confirm the CHANGELOG structure: `grep -n '^## ' CHANGELOG.md` — must show `## Unreleased` directly above `## v0.11.0`, with nothing inserted above the `# Changelog` title or inside the preamble.
</verification>
