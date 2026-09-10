---
status: completed
spec: [003-setup-launchd-and-brew-cask]
summary: Added idempotent git-ai-sync setup-launchd/remove-launchd subcommands backed by a new launchd module reproducing the frozen live-plist shape, with full unit tests and a CHANGELOG Unreleased entry
execution_id: git-ai-sync-exec-011-spec-003-setup-launchd-subcommands
dark-factory-version: dev
created: "2026-09-10T15:33:07Z"
queued: "2026-09-10T16:55:55Z"
started: "2026-09-10T16:59:21Z"
completed: "2026-09-10T17:03:33Z"
---

# Add setup-launchd and remove-launchd subcommands

<summary>
- Users can register a per-vault launchd agent with `git-ai-sync setup-launchd <vault-dir>` and tear it down with `git-ai-sync remove-launchd <vault-dir>`
- The generated plist reproduces the exact frozen shape of the live agents: `watch <vault-dir> --interval 30 --strategy merge`, KeepAlive + RunAtLoad, stdout/stderr to `/tmp/git-ai-sync-<label>.log`, the fixed soft/hard resource limits, and the three `GIT_AI_SYNC_PUSHGATEWAY_*` environment variables passed through only when set
- The agent label derives from the last path component: lowercased, every run of non-alphanumeric characters collapsed to a single hyphen (`Personal` → `personal`, `Path With.Spaces` → `path-with-spaces`)
- The plist always points at a real binary: an explicit absolute invocation path is used when given, otherwise PATH lookup with a `~/.local/bin/git-ai-sync` fallback
- Setup is idempotent: re-runs write a byte-identical plist and tolerate an already-loaded job; remove is idempotent too (no plist, no job, no error)
- A failed setup names the failing step and, for launchd-domain failures, prints the exact manual `launchctl bootstrap` / `kickstart` commands to run
- Invalid vault input (missing/non-directory) fails before anything is written
- The subcommand logic lives in a new launchd module instead of the CLI entry file, matching the project's functions-over-classes style
</summary>

<objective>
Add idempotent `setup-launchd` and `remove-launchd` CLI subcommands that generate and tear down per-vault macOS launchd agents reproducing the frozen live-plist shape, so the hand-written plist ritual becomes a tested, reproducible command — the foundation the Homebrew cask and docs (later prompts) build on.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, ruff line-length 100, mypy strict, pytest + pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/python-cli-arguments-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the argparse and red-green test workflow.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention (insert after the frozen preamble, directly above the highest `## vX.Y.Z`; flat list; conventional prefixes).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates (type hints, docstrings, functions over classes, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code).
Read `src/git_ai_sync/__main__.py` — `parse_args()` (the `subparsers = parser.add_subparsers(dest="command", required=True)` block and the existing `watch`/`sync`/`resolve`/`status`/`config`/`version`/`doctor` parser definitions), `main()` (the `if args.command == "watch":` dispatch chain), and `cmd_watch` for the local-import + `sys.exit(1)` error style to mirror.
Read `src/git_ai_sync/git_operations.py` — the functions-over-classes style and the `GitError(Exception)` error-raise pattern to mirror with a new `LaunchdError`.
Read `tests/test_main.py` — the `TestParseArgs` class with `patch("sys.argv", [...])` for new subcommand parse tests, and the patch-constant style for cmd dispatch tests.
Read `tests/test_config.py` — the `monkeypatch.setenv`/`delenv` patterns for environment-dependent tests.
Read `CHANGELOG.md` — the frozen preamble ends at the `* PATCH version when you make backwards-compatible bug fixes.` line; the highest released section is `## v0.10.1`.

The frozen plist shape (spec Constraint, mirrored by the live `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-obsidian-brogrammers.plist`): Label `com.github.bborbe.git-ai-sync-<label>`; ProgramArguments `[<binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`; KeepAlive true; RunAtLoad true; StandardOutPath == StandardErrorPath == `/tmp/git-ai-sync-<label>.log`; SoftResourceLimits `{NumberOfFiles: 1024, ResidentSetSize: 536870912}`; HardResourceLimits `{NumberOfFiles: 2048, ResidentSetSize: 1073741824}`; EnvironmentVariables carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` / `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` verbatim, each key present only when set in the setup environment.

<!-- OPEN QUESTIONS (reviewer decision points, resolved below — flag if you disagree):
     1. remove-launchd does NOT validate that the vault dir exists — it derives the label from the last path component of whatever string is given, so a never-set-up or non-existent dir still exits 0 (spec AC 4 requires exit 0 on never-set-up dirs). Only setup-launchd validates the dir.
     2. On a setup failure after the plist was written (e.g. launchctl bootstrap fails in a sandboxed context), the plist is LEFT on disk — this matches the spec failure-mode row "Crash mid-setup": a plist without a loaded job is inert, and re-running setup converges. The error message carries the manual bootstrap/kickstart commands. -->
</context>

<requirements>
1. Create `src/git_ai_sync/launchd.py` with a module docstring. It must use only stdlib imports (`plistlib`, `os`, `re`, `shutil`, `subprocess`, `sys`, `logging`, `collections.abc.Mapping`, `pathlib.Path`). Define:

   ```python
   class LaunchdError(Exception):
       """Raised when a launchd setup or removal step fails."""
   ```

   and a module-level `logger = logging.getLogger(__name__)`.

2. Label derivation — `def derive_label(vault_dir: Path) -> str`. Contract: use only the last path component (`vault_dir.name`), lowercase it, replace every run of non-alphanumeric characters with a single hyphen, then strip leading/trailing hyphens. Equivalent to `re.sub(r"[^a-z0-9]+", "-", vault_dir.name.lower()).strip("-")`. Must map `Path("/Users/bborbe/Documents/Obsidian/Personal")` → `"personal"` and `Path("/x/Path With.Spaces")` → `"path-with-spaces"`. Because only the last component and an alphanumeric-only output are used, a hostile dir name cannot inject directory separators or plist syntax into the label.

3. Executable resolution — `def resolve_binary(argv0: str | None = None) -> Path`. Contract, in order:
   - `argv0 = argv0 if argv0 else sys.argv[0]`
   - If `Path(argv0).is_absolute()`, `Path(argv0).exists()`, and `Path(argv0).name == "git-ai-sync"` — the invocation came from an explicit absolute binary path (e.g. the brew cask postflight) — return `Path(argv0).resolve()`. The `name == "git-ai-sync"` guard is REQUIRED so that `python -m git_ai_sync` (argv0 = the module file, name `__main__.py`) falls through to PATH lookup instead of pointing the plist at the module file.
   - Else `shutil.which("git-ai-sync")`; if found, return `Path(found).resolve()`.
   - Else fall back to `(Path.home() / ".local" / "bin" / "git-ai-sync").resolve()` (the `uv tool install` location).

4. Plist generation — `def generate_plist(binary: Path, vault_dir: Path, label: str, env: Mapping[str, str] | None = None) -> dict[str, object]`. Contract: `env` defaults to `os.environ`; the returned dict is EXACTLY (the frozen live-template values — do not add, remove, or alter any field):

   ```python
   {
       "Label": f"com.github.bborbe.git-ai-sync-{label}",
       "ProgramArguments": [
           str(binary), "watch", str(vault_dir),
           "--interval", "30", "--strategy", "merge",
       ],
       "KeepAlive": True,
       "RunAtLoad": True,
       "StandardOutPath": f"/tmp/git-ai-sync-{label}.log",
       "StandardErrorPath": f"/tmp/git-ai-sync-{label}.log",
       "SoftResourceLimits": {"NumberOfFiles": 1024, "ResidentSetSize": 536870912},
       "HardResourceLimits": {"NumberOfFiles": 2048, "ResidentSetSize": 1073741824},
       "EnvironmentVariables": {
           key: env[key]
           for key in (
               "GIT_AI_SYNC_PUSHGATEWAY_URL",
               "GIT_AI_SYNC_PUSHGATEWAY_USERNAME",
               "GIT_AI_SYNC_PUSHGATEWAY_PASSWORD",
           )
           if key in env
       },
   }
   ```

   The caller passes `vault_dir` already resolved to an absolute path (see step 6). `watch` and the literal `--interval 30 --strategy merge` defaults MUST be hardcoded — do not read them from `Config` or any environment variable.

5. Plist writing — `def write_plist(plist_path: Path, plist: dict[str, object]) -> None`. Contract: `plist_path.parent.mkdir(parents=True, exist_ok=True)`; serialize with `plistlib.dumps(plist, sort_keys=True)` (deterministic XML, byte-identical across re-runs); write atomically (temp file in the same directory + `os.replace`) so a crash never leaves a truncated plist; raise `LaunchdError` wrapping any write failure.

6. Setup — `def setup_launchd(vault_dir: Path) -> None`. Sequence and error contract:
   a. `vault_dir = vault_dir.resolve()`; if not `vault_dir.is_dir()`: raise `LaunchdError` with a message naming the "dir validation" step and stating that the vault dir must exist and be a directory. This MUST happen before anything is written.
   b. `label = derive_label(vault_dir)`; `binary = resolve_binary()`; log the resolved binary and label.
   c. `plist = generate_plist(binary, vault_dir, label)` (env from `os.environ`).
   d. `plist_path = Path.home() / "Library" / "LaunchAgents" / f"com.github.bborbe.git-ai-sync-{label}.plist"`; `write_plist(plist_path, plist)`; on failure raise `LaunchdError` naming the "plist write" step.
   e. `gui_domain = f"gui/{os.getuid()}"`. Run `subprocess.run(["launchctl", "bootstrap", gui_domain, str(plist_path)], check=False, capture_output=True, text=True, timeout=30)`. If returncode != 0: run the probe `subprocess.run(["launchctl", "print", f"{gui_domain}/{label}"], check=False, capture_output=True, text=True, timeout=30)`. If the print probe returns 0, the job is already loaded — log an info line and treat the bootstrap as success (spec failure-mode row "Plist already exists / agent already loaded"). If the print probe also fails, raise `LaunchdError` whose message names the "launchctl" step AND includes the two full manual commands:
      ```
      launchctl bootstrap gui/<uid> <plist_path>
      launchctl kickstart -k gui/<uid>/<label>
      ```
      A `subprocess.TimeoutExpired` on any launchctl call is also a `LaunchdError` naming the "launchctl" step.
   f. Run `subprocess.run(["launchctl", "kickstart", "-k", f"{gui_domain}/{label}"], check=False, capture_output=True, text=True, timeout=30)`. If returncode != 0 (or `TimeoutExpired`): raise `LaunchdError` naming the "launchctl" step and the failed command.
   g. Log an info line confirming the agent is registered. Do NOT delete the plist on any failure (see the open-questions comment in `<context>`).

7. Removal — `def remove_launchd(vault_dir: Path) -> None`. Contract: no directory validation (works on never-set-up and non-existent dirs — spec AC 4 requires exit 0 there). Sequence: `label = derive_label(vault_dir)`; run `subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}", label], check=False, capture_output=True, text=True, timeout=30)` — BEST-EFFORT: any non-zero returncode (or `TimeoutExpired`) is logged as a warning and ignored; then `plist_path = Path.home() / "Library" / "LaunchAgents" / f"com.github.bborbe.git-ai-sync-{label}.plist"` and `plist_path.unlink(missing_ok=True)` — a failure to delete an existing plist raises `LaunchdError` naming the "plist delete" step.

8. Wire the subcommands in `src/git_ai_sync/__main__.py`:
   a. In `parse_args()`, after the `doctor` parser definition, add:
      ```python
      setup_launchd_parser = subparsers.add_parser(
          "setup-launchd", help="Install a launchd agent for a vault directory"
      )
      setup_launchd_parser.add_argument("path", help="Vault directory to watch")

      remove_launchd_parser = subparsers.add_parser(
          "remove-launchd", help="Remove the launchd agent for a vault directory"
      )
      remove_launchd_parser.add_argument("path", help="Vault directory")
      ```
   b. Add `cmd_setup_launchd(args: argparse.Namespace) -> None` and `cmd_remove_launchd(args: argparse.Namespace) -> None` module-level functions following the existing `cmd_*` local-import style: import `Path` from `pathlib` and `LaunchdError, setup_launchd` / `LaunchdError, remove_launchd` from `git_ai_sync.launchd` inside the function body, call the function with `Path(args.path)`, log an info line on success, and on `LaunchdError` log `logger.error(str(e))` and `sys.exit(1)`.
   c. In `main()`, add to the dispatch chain:
      ```python
      elif args.command == "setup-launchd":
          cmd_setup_launchd(args)
      elif args.command == "remove-launchd":
          cmd_remove_launchd(args)
      ```
   Do not modify any existing subcommand, the `watch` parser defaults, or the existing `main()` version/strategy handling.

9. Add `tests/test_launchd.py` (pytest conventions, type-hinted). Cover:
   a. `derive_label` table test: `Personal` → `personal`; `Path With.Spaces` → `path-with-spaces`; last-component use (`/x/y/Zoo` → `zoo`); lowercasing (`/X/Y/ZooBAR` → `zoobar`); run-collapse (`Foo  Bar__Baz` → `foo-bar-baz`); leading/trailing non-alphanumerics (`.hidden.` → `hidden`).
   b. `resolve_binary` tests passing `argv0` explicitly: (i) an existing absolute file named `git-ai-sync` is returned; (ii) a bare `git-ai-sync` argv0 with `shutil.which` returning a path uses that path (monkeypatch `git_ai_sync.launchd.shutil.which`); (iii) bare argv0 with `which` returning `None` falls back to `~/.local/bin/git-ai-sync` (monkeypatch `HOME`); (iv) an absolute argv0 named `__main__.py` falls through to the `which` lookup (the `python -m` case).
   c. `generate_plist` — the AC 2 test: for a fixture vault dir and binary with `env` containing all three pushgateway keys, assert the decoded dict (`plistlib.loads(plistlib.dumps(plist))`, i.e. a full serialization round-trip) equals exactly the required dict from step 4 with the env values verbatim — all fields present and exact, including the `EnvironmentVariables` values.
   d. `generate_plist` env-subset test: only `GIT_AI_SYNC_PUSHGATEWAY_URL` set → only that key in `EnvironmentVariables`; none set → empty `EnvironmentVariables` dict.
   e. Byte-identical idempotence (AC 3): two `generate_plist` calls with the same inputs produce identical `plistlib.dumps` bytes; and `write_plist` twice to the same path yields a byte-identical file.
   f. `setup_launchd` unit tests (monkeypatch `git_ai_sync.launchd.subprocess.run` with a fake capturing the argv list and returning a fake completed-process object with the desired `returncode`, and monkeypatch `HOME` to a scratch dir so `Path.home()` points at scratch):
      - happy path: plist file appears under `scratch/Library/LaunchAgents/com.github.bborbe.git-ai-sync-personal.plist`; `subprocess.run` called first with `["launchctl", "bootstrap", "gui/<uid>", <plist_path>]` then `["launchctl", "kickstart", "-k", "gui/<uid>/personal"]` (uid = `os.getuid()`); no `print` probe on the success path.
      - already-loaded tolerance (spec failure-mode row 1): bootstrap returns 1, the print probe returns 0 → no exception, kickstart still runs.
      - hard launchd failure (spec failure-mode row 2): bootstrap returns 1 AND the print probe returns 1 → `LaunchdError` raised, message contains the `launchctl bootstrap gui/<uid> <plist>` and `launchctl kickstart -k gui/<uid>/<label>` manual commands.
      - dir validation: non-existent dir and a plain-file dir → `LaunchdError` raised, no plist written, no subprocess call.
   g. `remove_launchd` unit tests: bootout called with `["launchctl", "bootout", "gui/<uid>", "personal"]`; plist deleted; bootout non-zero → no exception (best-effort); never-set-up (no plist) → no exception.

10. Extend `tests/test_main.py`:
    a. In `TestParseArgs`: `patch("sys.argv", ["git-ai-sync", "setup-launchd", "/vault"])` → `args.command == "setup-launchd"` and `args.path == "/vault"`; the same for `remove-launchd`.
    b. `cmd_setup_launchd`/`cmd_remove_launchd` dispatch tests: patch `git_ai_sync.launchd.setup_launchd` (and `remove_launchd`) with a MagicMock; happy path calls the patched function with the resolved `Path` and returns normally; a `side_effect=LaunchdError("...")` raises `SystemExit` with code 1 (import `LaunchdError` from `git_ai_sync.launchd`).

11. Update `CHANGELOG.md`: insert (or extend) a `## Unreleased` section between the frozen preamble (ending at `* PATCH version when you make backwards-compatible bug fixes.`) and `## v0.10.1` with a flat `feat:` bullet:
    ```
    - feat: Add idempotent `git-ai-sync setup-launchd <vault-dir>` and `git-ai-sync remove-launchd <vault-dir>` subcommands that generate and tear down per-vault launchd agents reproducing the frozen live-plist shape (label derived from the last path component, pushgateway env vars passed through only when set, byte-identical re-runs, tolerant of already-loaded jobs)
    ```
    Do NOT modify anything above the `# Changelog` title, the preamble, or the `## v0.10.1` section.

12. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk each acceptance criterion from the spec (AC 2 plist decode, AC 3 label + idempotence) against the change.
</requirements>

<constraints>
- The plist shape is a FROZEN constraint and must match the live template exactly: Label format `com.github.bborbe.git-ai-sync-<label>`; ProgramArguments `[<binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`; KeepAlive true; RunAtLoad true; StandardOutPath == StandardErrorPath == `/tmp/git-ai-sync-<label>.log`; SoftResourceLimits 1024 / 536870912; HardResourceLimits 2048 / 1073741824; EnvironmentVariables carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `_USERNAME` / `_PASSWORD` from the setup environment. `watch` and its defaults (`--interval 30 --strategy merge`) must not change.
- The label rule must reproduce `personal` for `/Users/bborbe/Documents/Obsidian/Personal`. It deliberately does NOT reproduce the other hand-written labels (e.g. `Brogrammers` derives `brogrammers`, not the live `obsidian-brogrammers`) — those agents are out of scope and their differing labels guarantee no accidental takeover.
- Existing CLI subcommands (`watch`, `sync`, `resolve`, `status`, `config`, `version`, `doctor`) and their behavior are unchanged.
- Do NOT add a launchd-setup opt-out flag, a configurable interval/strategy for `setup-launchd`, or any tunable threshold — the frozen plist shape is fixed.
- Project DoD (`docs/dod.md`): `make precommit` green (ruff + mypy strict, pytest, pytest-asyncio `asyncio_mode = "auto"`), type hints and docstrings on all public functions, functions-over-classes for stateless operations, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code (all paths via `Path`).
- Never log the plist EnvironmentVariables values (the pushgateway password) — the only plist-derived logging is the resolved binary and label in the setup step.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the new tests pass:
`uv run pytest tests/test_launchd.py tests/test_main.py -v`
Confirm both subcommands are registered:
`grep -n 'setup-launchd' src/git_ai_sync/__main__.py` — line ≥ 1
`grep -n 'remove-launchd' src/git_ai_sync/__main__.py` — line ≥ 1
Confirm the CHANGELOG structure: `grep -n "^## " CHANGELOG.md` — must show `## Unreleased` directly above `## v0.10.1`, with nothing inserted above the `# Changelog` title or inside the preamble.
</verification>
