---
status: completed
spec: [003-setup-launchd-and-brew-cask]
summary: 'Switched remove-launchd''s bootout to the single service-target form (gui/<uid>/com.github.bborbe.git-ai-sync-<label>), made the e2e fake launchctl shim reject the broken two-arg form like real launchd, updated all unit/e2e assertions, and added a fix: CHANGELOG entry'
execution_id: git-ai-sync-exec-016-fix-launchd-bootout-single-target
dark-factory-version: dev
created: "2026-09-10T21:41:35Z"
queued: "2026-09-10T21:47:50Z"
started: "2026-09-10T21:47:58Z"
completed: "2026-09-10T21:49:54Z"
---

# Fix launchd bootout to the single service-target form

<summary>
- `remove-launchd` now unloads the launchd agent for real: the bootout invocation uses the single service-target form (`gui/<uid>/com.github.bborbe.git-ai-sync-<label>`), the only form real launchd accepts for gui-domain user agents
- The two-arg bootout form (separate domain and label), which real launchd rejects with `Boot-out failed: 5: Input/output error` while `remove-launchd` still exits 0, is gone from the production code and from every test assertion
- Removing an agent now actually stops it — the agent no longer keeps running after a successful-looking remove, which the spec's operator ladder requires
- The e2e fake `launchctl` shim now rejects the two-arg bootout form exactly like real launchd, so the regression is locked at the e2e layer too (the old shim accepted it because the label argument still contained the full label prefix)
- A new direct e2e test exercises the fake's two-arg rejection, so a future weakening of the shim fails loudly
- The four `TestRemoveLaunchd` unit-test bootout-argv assertions and the e2e bootout log-line assertion move to the single service-target form
- Tests are written red-first per the repo's TDD rule: the fake's rejection makes the never-set-up remove test fail until the production code is fixed
- A `## Unreleased` CHANGELOG entry with a `fix:` bullet records the change
- No new config fields, no new functions, no behavior knobs — only the bootout argv changes; the launchctl control flow, `timeout=30`, and the best-effort bootout warning semantics are unchanged
</summary>

<objective>
Make `remove-launchd` actually unload the launchd agent on real macOS by switching the bootout invocation to the single service-target form (the only form launchd accepts for gui-domain user agents), and lock that contract into the unit and e2e tests — including making the e2e fake reject the broken two-arg form exactly like real launchd — so the agent-still-running-after-remove defect cannot silently return.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff, mypy strict, pytest + pytest-asyncio `asyncio_mode = "auto"`; all code changes go through dark-factory, never commit directly).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention (insert after the frozen preamble, directly above the highest `## vX.Y.Z`; flat list; conventional prefixes — use `fix:` for this bugfix) and `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing test first, then make it pass).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates (type hints, docstrings, functions over classes, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code).
Read `src/git_ai_sync/launchd.py` — `remove_launchd()` (lines 248-286) is the ONLY function that calls bootout; `gui_domain = f"gui/{os.getuid()}"` at line 263; the two-arg bootout call at line 266. The already-loaded print probe (line 201) and the kickstart (line 224) already use the single service-target form `f"{gui_domain}/com.github.bborbe.git-ai-sync-{label}"` — the bootout must match them. The previous fix (commit ef48789) fixed probe/kickstart to the full label but left bootout as `["launchctl", "bootout", gui_domain, f"com.github.bborbe.git-ai-sync-{label}"]` — the two-arg form that real launchd on Darwin 25.6.0 rejects with `Boot-out failed: 5: Input/output error` (proven live, 3/3 reproductions; the single-target form works, 2/2).
Read `tests/test_launchd.py` — the `_patch_subprocess_run` helper (lines 150-174) matches on `argv[1]` command name only, so it needs NO change; the four `TestRemoveLaunchd` bootout-argv assertions are at lines 338, 353, 368, 386.
Read `tests/test_launchd_e2e.py` — the fake shim `_FAKE_LAUNCHCTL_SOURCE` (lines 20-46) has only a full-label containment check on `sys.argv[-1]`, which does NOT reject the two-arg bootout form (the label argument still contains `com.github.bborbe.git-ai-sync-`), so the e2e never caught this defect; the bootout log-line assertion in `test_remove_deletes_plist_and_bootouts` is at line 255; the short-label rejection test `test_fake_launchctl_rejects_short_label` (lines 258-267) is the pattern to mirror for the new two-arg rejection test.
Read `CHANGELOG.md` — the frozen preamble ends at `* PATCH version when you make backwards-compatible bug fixes.`; the highest released section is `## v0.11.1`; there is currently NO `## Unreleased` section.

<!-- OPEN QUESTIONS (reviewer decision points — flag if you disagree):
     1. SCOPE — docs/launchd-service.md mentions bootout only descriptively ("boots the job out of launchd", line 50; "a failed `launchctl bootout` is logged as a warning", line 57) and never prints the raw bootout argv, so there is no doc command to correct. No docs change is made here.
     2. CHANGELOG — a fresh `## Unreleased` section is created above `## v0.11.1` (`autoRelease: false` — the bullet waits for the next manual release cut, matching the previous fix's flow). Strike requirement 5 if you want CHANGELOG out of scope.
     3. OPERATOR RUNG — the live re-verification (run `git-ai-sync remove-launchd ~/Documents/Obsidian/Personal` on the host, then `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` fails = job gone, then re-run `setup-launchd` to restore the Personal agent) is deliberately NOT in this prompt. It lives in spec 003's Verification ladder (operator-executable rung) and runs on the host after merge — the container has no launchd GUI domain.
     4. INLINE COMMENT — a short comment is added above the bootout `subprocess.run` explaining why the single service-target form is mandatory, so a future contributor does not "simplify" it back to the two-arg form. Strike it if you want a zero-comment diff. -->
</context>

<requirements>
The defect, proven live on Darwin 25.6.0 (3/3 reproductions): `remove_launchd()` invokes `launchctl bootout gui/<uid> com.github.bborbe.git-ai-sync-<label>` (separate domain and label as two arguments), which real launchd rejects with `Boot-out failed: 5: Input/output error`. Because the bootout is best-effort (non-zero return is logged as a warning and ignored), `remove-launchd` still exits 0 and deletes the plist — but the agent keeps running, violating the spec's operator ladder ("job gone after remove"). The single service-target form `launchctl bootout gui/<uid>/com.github.bborbe.git-ai-sync-<label>` works (2/2, job unloaded) and matches the form the print probe and kickstart already use. The e2e fake does not catch this because its full-label containment check on `sys.argv[-1]` passes for the two-arg form.

Follow TDD (repo rule: failing test first): requirements 1-3 make the tests red against the current two-arg production code; requirement 4 turns them green. Run the affected suites between steps to confirm the red, then the green.

1. In `tests/test_launchd_e2e.py`, make the fake shim reject the two-arg bootout form exactly like real launchd.

   a. Replace the entire `_FAKE_LAUNCHCTL_SOURCE` string (lines 20-46) with this version — it keeps the log line, the two env-var failure toggles, and the full-label containment check, and adds a bootout-specific arity check:
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
   # Real launchd on current macOS rejects the separate-domain two-arg bootout
   # form ("launchctl bootout gui/<uid> <label>") with an I/O error; only the
   # single service-target form ("launchctl bootout gui/<uid>/<label>") unloads
   # a gui-domain user agent.
   if command == "bootout" and len(sys.argv) == 4:
       sys.stderr.write("Boot-out failed: 5: Input/output error\n")
       sys.exit(1)
   sys.exit(0)
   """
   ```
   Note the order: the existing full-label check runs first (so a two-arg bootout with a SHORT label fails with `Could not find service`), then the new arity check catches the two-arg bootout with a FULL label (`argv[-1]` contains the prefix, so only `len(sys.argv) == 4` distinguishes it).

   b. Add a direct test of the fake's two-arg rejection directly after `test_fake_launchctl_rejects_short_label` (currently ends at line 267), mirroring its shape — this locks the fake's new rejection branch so a future weakening of the shim fails loudly:
   ```python
   def test_fake_launchctl_rejects_two_arg_bootout(fake_launchctl: FakeLaunchctl) -> None:
       """The fake shim fails the separate-domain two-arg bootout form like real launchd."""
       proc = subprocess.run(
           [
               str(fake_launchctl.shim / "launchctl"),
               "bootout",
               f"gui/{os.getuid()}",
               "com.github.bborbe.git-ai-sync-personal",
           ],
           capture_output=True,
           text=True,
           check=False,
       )
       assert proc.returncode != 0
       assert "Input/output error" in proc.stderr
   ```

   c. Expected red after this step: `test_remove_never_setup_exits_0` FAILS — production still invokes the two-arg bootout form, the fake now rejects it, `remove_launchd` logs `launchctl bootout returned 1: Boot-out failed: 5: Input/output error`, and that test asserts `"error" not in output.lower()`. This red is the TDD signal. Do NOT fix the production code yet.

2. In `tests/test_launchd.py`, update the four `TestRemoveLaunchd` bootout-argv assertions (lines 338, 353, 368, 386 — `test_removes_job_and_plist`, `test_bootout_nonzero_best_effort`, `test_never_setup_no_exception`, `test_plist_delete_failure_names_step`) to the single service-target form:
   ```python
   # old
   assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "com.github.bborbe.git-ai-sync-vault"]]
   # new
   assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}/com.github.bborbe.git-ai-sync-vault"]]
   ```
   The `_patch_subprocess_run` helper (matches on `argv[1]` command name only) needs NO change. Expected red after this step: the four `TestRemoveLaunchd` tests fail against the two-arg production argv.

3. In `tests/test_launchd_e2e.py`, update the bootout log-line assertion in `test_remove_deletes_plist_and_bootouts` (line 255):
   ```python
   # old
   assert lines[-1] == f"bootout gui/{os.getuid()} com.github.bborbe.git-ai-sync-personal"
   # new
   assert lines[-1] == f"bootout gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal"
   ```
   Expected red after this step: this assertion fails against the two-arg argv production logs. `test_remove_never_setup_exits_0` (no bootout log assertion) and `test_remove_deletes_plist_and_bootouts`'s other assertions need no change.

4. In `src/git_ai_sync/launchd.py`, fix `remove_launchd()` (currently lines 263-271): change ONLY the bootout argv to the single service-target form and add one explanatory comment. The launchctl control flow, `timeout=30`, the `check=False, capture_output=True, text=True` kwargs, the `TimeoutExpired` warning, the non-zero-returncode warning, and the best-effort semantics must NOT change. The docstring stays as-is (best-effort semantics are preserved).
   ```python
   # old
       gui_domain = f"gui/{os.getuid()}"
       try:
           result = subprocess.run(
               ["launchctl", "bootout", gui_domain, f"com.github.bborbe.git-ai-sync-{label}"],
               check=False,
               capture_output=True,
               text=True,
               timeout=30,
           )
   # new
       gui_domain = f"gui/{os.getuid()}"
       # launchd rejects the separate-domain two-arg bootout form ("bootout
       # gui/<uid> <label>") with "Boot-out failed: 5: Input/output error"; the
       # single service-target form is the only one that unloads a gui-domain
       # user agent.
       try:
           result = subprocess.run(
               [
                   "launchctl",
                   "bootout",
                   f"{gui_domain}/com.github.bborbe.git-ai-sync-{label}",
               ],
               check=False,
               capture_output=True,
               text=True,
               timeout=30,
           )
   ```
   (ruff format may reflow the list — the string content above is the contract.) After this step the affected tests turn green. Do NOT touch any other launchctl call in the file — the bootstrap (two-arg, correct), the print probe, and the kickstart already use the correct forms.

5. Update `CHANGELOG.md`: insert a `## Unreleased` section between the frozen preamble (ending at `* PATCH version when you make backwards-compatible bug fixes.`) and `## v0.11.1` (there is currently no `## Unreleased` — create it) with a single flat `fix:` bullet:
   ```
   - fix: `remove-launchd` bootouts the agent with the single service-target form `launchctl bootout gui/<uid>/com.github.bborbe.git-ai-sync-<label>` instead of the separate-domain two-arg form (`bootout gui/<uid> <label>`), which real launchd rejects with `Boot-out failed: 5: Input/output error` and left the agent running after a successful-looking remove
   ```
   Do NOT modify anything above the `# Changelog` title, the preamble, or the `## v0.11.1` section.

6. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk the bootout call in `remove_launchd()` against the single service-target contract and confirm no two-arg bootout form remains in `src/git_ai_sync/launchd.py` or the tests.
</requirements>

<constraints>
- The plist shape is a FROZEN constraint and must match the live template exactly: Label format `com.github.bborbe.git-ai-sync-<label>`; ProgramArguments `[<binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`; KeepAlive true; RunAtLoad true; StandardOutPath == StandardErrorPath == `/tmp/git-ai-sync-<label>.log`; SoftResourceLimits 1024 / 536870912; HardResourceLimits 2048 / 1073741824; EnvironmentVariables carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `_USERNAME` / `_PASSWORD` from the setup environment. `watch` and its defaults (`--interval 30 --strategy merge`) must not change. Do NOT modify `generate_plist()` or any plist content.
- The label rule must reproduce `personal` for `/Users/bborbe/Documents/Obsidian/Personal`. It deliberately does NOT reproduce the other hand-written labels (e.g. `Brogrammers` derives `brogrammers`, not the live `obsidian-brogrammers`) — those agents are out of scope.
- Existing CLI subcommands (`watch`, `sync`, `resolve`, `status`, `config`, `version`, `doctor`) and their behavior are unchanged.
- Do NOT add a launchd-setup opt-out flag, a configurable interval/strategy for `setup-launchd`, a label-prefix knob, or any tunable threshold — the frozen plist shape is fixed (spec Non-goal, hard veto).
- The launchctl control flow is unchanged: bootstrap → print-probe-on-bootstrap-failure → kickstart in `setup_launchd`; best-effort bootout warning in `remove_launchd`; every call keeps `check=False, capture_output=True, text=True, timeout=30`. ONLY the bootout argv changes.
- Project DoD (`docs/dod.md`): `make precommit` green (ruff format + lint + mypy strict, pytest, pytest-asyncio `asyncio_mode = "auto"`), type hints and docstrings on all public functions, functions-over-classes for stateless operations, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code (all paths via `Path`).
- Never log the plist EnvironmentVariables values (the pushgateway password) — the only plist-derived logging is the resolved binary and label.
- Never call the real `launchctl` from tests — the e2e goes through the fake shim; tests must not require a macOS launchd domain.
- This prompt runs in a container with no launchd GUI domain and no git access guarantees — the live host re-verification (run `git-ai-sync remove-launchd ~/Documents/Obsidian/Personal` on the host, `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` fails = job gone, then re-run `setup-launchd` to restore) is the spec's operator rung, NOT part of this prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the affected suites pass:
`uv run pytest tests/test_launchd.py tests/test_launchd_e2e.py -v`
Confirm no two-arg bootout form remains:
`grep -n 'bootout", gui_domain, f"com.github.bborbe' src/git_ai_sync/launchd.py` — must print 0 matches
`grep -n 'bootout", f"gui/{os.getuid()}", "com.github.bborbe' tests/test_launchd.py` — must print 0 matches
`grep -n 'bootout gui/{os.getuid()} com.github.bborbe' tests/test_launchd_e2e.py` — must print 0 matches
Confirm the single service-target forms are in place:
`grep -n 'bootout", f"{gui_domain}/com.github.bborbe' src/git_ai_sync/launchd.py` — at least 1 match
`grep -n 'bootout", f"gui/{os.getuid()}/com.github.bborbe' tests/test_launchd.py` — at least 4 matches
`grep -n 'bootout gui/{os.getuid()}/com.github.bborbe' tests/test_launchd_e2e.py` — at least 1 match
`grep -n 'Input/output error' tests/test_launchd_e2e.py` — at least 2 matches (the fake source and the direct rejection test)
Confirm the CHANGELOG structure: `grep -n '^## ' CHANGELOG.md` — must show `## Unreleased` directly above `## v0.11.1`, with nothing inserted above the `# Changelog` title or inside the preamble.
</verification>
