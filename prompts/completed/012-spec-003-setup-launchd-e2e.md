---
status: completed
spec: [003-setup-launchd-and-brew-cask]
summary: Added an 8-test e2e suite (tests/test_launchd_e2e.py) driving the real CLI with a fake launchctl shim and scratch HOME, covering plist write, bootstrap/kickstart/bootout argv, idempotence, already-loaded tolerance, hard-failure exit codes, and missing-dir handling; updated CHANGELOG Unreleased.
execution_id: git-ai-sync-exec-012-spec-003-setup-launchd-e2e
dark-factory-version: dev
created: "2026-09-10T15:33:07Z"
queued: "2026-09-10T16:55:56Z"
started: "2026-09-10T17:03:34Z"
completed: "2026-09-10T17:04:55Z"
---

# E2E tests for setup-launchd and remove-launchd with a fake launchctl

<summary>
- An e2e test suite drives the real `git-ai-sync` CLI (via `python -m git_ai_sync`) against scratch directories, with a scripted fake `launchctl` shimmed onto PATH and `HOME` pointed at a scratch dir
- Running `setup-launchd` on a scratch vault writes the plist under `scratch/Library/LaunchAgents/` and invokes the fake `launchctl bootstrap gui/<uid> <plist>` then `kickstart -k gui/<uid>/<label>`
- A scratch vault named `Path With.Spaces` derives label `path-with-spaces` at the e2e layer too (the CLI boundary, not just the unit function)
- A re-run of setup is byte-identical and exits 0; an already-loaded job (bootstrap fails but the print probe succeeds) is tolerated and exits 0
- A hard launchd failure (bootstrap and print both fail) makes setup exit non-zero with the manual `launchctl bootstrap` / `kickstart` commands in its output
- `remove-launchd` invokes `launchctl bootout gui/<uid> <label>` and deletes the plist; on a never-set-up dir it exits 0
- `setup-launchd` on a missing dir exits non-zero without writing anything or invoking launchctl
</summary>

<objective>
Lock the subcommand flow end-to-end — real CLI entry, real plist serialization, real `Path.home()` via scratch HOME, and a scripted fake `launchctl` standing in for the macOS launchd domain — so the launchctl argv shape, idempotence, and failure/exit-code behavior are regression-protected where unit tests cannot reach.
</objective>

<context>
Read CLAUDE.md for project conventions (pytest, `asyncio_mode = "auto"`, uv).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow.
Read `docs/dod.md` — the Definition of Done (`make precommit` green; pytest conventions, no unittest).
Read `src/git_ai_sync/launchd.py` — the module created by prompt 1 of this spec: `derive_label`, `resolve_binary`, `generate_plist`, `write_plist`, `setup_launchd`, `remove_launchd`, `LaunchdError`. The e2e tests must NOT change any of these functions — they exercise the real CLI boundary.
Read `src/git_ai_sync/__main__.py` — `main()` dispatch: `git-ai-sync setup-launchd <path>` / `remove-launchd <path>`.
Read `tests/conftest.py` — the existing `temp_dir` fixture and its style.

The command shapes under test (spec AC 4): setup runs `launchctl bootstrap gui/<uid> <plist>` then `launchctl kickstart -k gui/<uid>/<label>`; an already-loaded job is detected via a `launchctl print gui/<uid>/<label>` probe; remove runs `launchctl bootout gui/<uid> <label>`; the plist lives at `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-<label>.plist` (so with scratch `HOME`, under `scratch/Library/LaunchAgents/`).

<!-- OPEN QUESTIONS (reviewer decision points):
     1. The fake launchctl is behavior-controlled by two env vars: FAKE_LAUNCHCTL_FAIL_BOOTSTRAP and FAKE_LAUNCHCTL_FAIL_PRINT. Only the combinations the spec failure modes need are exercised — no extra knobs.
     2. The e2e invokes the real CLI via `[sys.executable, "-m", "git_ai_sync", ...]` rather than the `git-ai-sync` console script, so the tests do not depend on the venv bin being on PATH inside the test process. This still exercises the real `main()` entry path and argparse wiring. -->
</context>

<requirements>
1. Create `tests/test_launchd_e2e.py` (pytest conventions, type-hinted). Production code under `src/git_ai_sync/` must NOT be modified by this prompt.

2. Fixture — fake `launchctl` shim. Given a scratch dir, write an executable Python script named `launchctl` into `scratch/bin/` starting with a `#!/usr/bin/env python3` shebang (required — the file is exec'd via PATH with no `.py` extension), `chmod`-it executable, and return the shim dir path plus the invocation-log path. The script:
   - reads env var `FAKE_LAUNCHCTL_LOG` (a file path) and appends one line per invocation: `" ".join(sys.argv[1:])` (the full argv, space-joined);
   - if `FAKE_LAUNCHCTL_FAIL_BOOTSTRAP` is set (non-empty) and the first argument is `bootstrap`, exits 1;
   - if `FAKE_LAUNCHCTL_FAIL_PRINT` is set and the first argument is `print`, exits 1;
   - otherwise exits 0.

3. Helper — `run_cli(*args: str, scratch: Path, shim: Path, log: Path, extra_env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]`. Contract: runs `[sys.executable, "-m", "git_ai_sync", *args]` via `subprocess.run(..., capture_output=True, text=True, env=...)` with `env = {**os.environ, "HOME": str(scratch), "PATH": f"{shim}:{os.environ['PATH']}", "FAKE_LAUNCHCTL_LOG": str(log), **(extra_env or {})}`. Inheriting `os.environ` keeps the venv's `git-ai-sync` on PATH so `resolve_binary` finds a real binary. Return the completed process.

4. Fixture — scratch vaults: create `scratch/vaults/Personal` (and, in the spaced test, `scratch/vaults/Path With.Spaces`) as real directories. The `HOME` env makes `Path.home()` resolve to `scratch`, so the plist lands under `scratch/Library/LaunchAgents/`.

5. E2E test: `test_setup_writes_plist_and_bootstraps` (AC 4). Run `setup-launchd <scratch>/vaults/Personal`. Assert:
   - returncode 0;
   - `scratch/Library/LaunchAgents/com.github.bborbe.git-ai-sync-personal.plist` exists and decodes via `plistlib.loads` with `Label == "com.github.bborbe.git-ai-sync-personal"`, `ProgramArguments[0]` absolute, `ProgramArguments[1] == "watch"`, and `ProgramArguments[2] == <resolved scratch vault path>`;
   - the fake launchctl log's first line is `bootstrap gui/<uid> <plist_path>` and its second line is `kickstart -k gui/<uid>/personal`, with `uid = os.getuid()` — the exact argv the real CLI passes (subprocess boundary).

6. E2E test: `test_setup_spaced_dir_derives_label_at_cli_layer` (AC 3 + AC 4). Run `setup-launchd "<scratch>/vaults/Path With.Spaces"` (the dir genuinely contains spaces). Assert returncode 0, the plist exists at `scratch/Library/LaunchAgents/com.github.bborbe.git-ai-sync-path-with-spaces.plist`, and the log's kickstart line references `gui/<uid>/path-with-spaces`.

7. E2E test: `test_setup_rerun_is_identical_and_exits_0` (AC 4 idempotence). Run setup twice on the same vault. Assert both returncode 0 and the plist bytes are identical across the two runs.

8. E2E test: `test_setup_tolerates_already_loaded_job` (spec failure-mode row 1 at the e2e layer). Run setup with `extra_env={"FAKE_LAUNCHCTL_FAIL_BOOTSTRAP": "1"}` (bootstrap fails, print probe succeeds). Assert returncode 0, the plist exists, and the log contains a `print gui/<uid>/personal` line (the probe ran) AND a `kickstart -k gui/<uid>/personal` line (kickstart still executed).

9. E2E test: `test_setup_hard_launchd_failure_names_manual_commands` (spec failure-mode row 2). Run setup with `extra_env={"FAKE_LAUNCHCTL_FAIL_BOOTSTRAP": "1", "FAKE_LAUNCHCTL_FAIL_PRINT": "1"}`. Assert returncode != 0 and `(proc.stdout + proc.stderr)` contains the text `launchctl bootstrap` and the text `launchctl kickstart` (the manual-command message). The plist may remain on disk (spec failure-mode row "Crash mid-setup" — recovery is re-running setup).

10. E2E test: `test_setup_missing_dir_exits_nonzero_before_any_write`. Run `setup-launchd <scratch>/vaults/DoesNotExist`. Assert returncode != 0, `scratch/Library/LaunchAgents/` does not exist (nothing written), and the fake launchctl log is empty (no launchctl invocation).

11. E2E test: `test_remove_deletes_plist_and_bootouts` (AC 4). After a successful setup (reuse the step-5 flow), run `remove-launchd <scratch>/vaults/Personal`. Assert returncode 0, the plist file is gone, and the fake log's last line is `bootout gui/<uid> personal`.

12. E2E test: `test_remove_never_setup_exits_0` (AC 4). Run `remove-launchd <scratch>/vaults/SomeVault` where no setup ever ran. Assert returncode 0, no plist exists, and no error text in the output.

13. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk spec AC 4 against the tests you added.
</requirements>

<constraints>
- Tests only — do NOT modify any file under `src/git_ai_sync/`; the e2e exercises the CLI produced by prompt 1 of this spec.
- Never call the real `launchctl` — every launchctl invocation must go through the fake shim; tests must not require a macOS launchd domain.
- No absolute paths in test code — scratch dirs come from `tmp_path`/fixtures; `Path.home()` behavior is controlled via the subprocess `HOME` env.
- Project DoD (`docs/dod.md`): `make precommit` green, pytest conventions (no unittest), type hints on all functions.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the e2e suite passes:
`uv run pytest tests/test_launchd_e2e.py -v`
Confirm the whole test suite still passes: `uv run pytest -v`
</verification>
