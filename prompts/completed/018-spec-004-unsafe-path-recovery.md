---
status: completed
spec: [004-bug-unsafe-path-refusal-skips-pull]
summary: Extended commit_with_marker_recovery to rename unsafe-path refusals to Windows-safe names and retry up to 3 attempts, adding unstage_paths and collision-safe renaming with tests
execution_id: git-ai-sync-exec-018-spec-004-unsafe-path-recovery
dark-factory-version: v0.196.0
created: "2026-10-11T14:30:00Z"
queued: "2026-10-11T12:54:21Z"
started: "2026-10-11T12:56:59Z"
completed: "2026-10-11T13:00:34Z"
---

<summary>
- A commit refused over an unsafe filename now heals itself: the offending path is renamed to a Windows-safe name, re-staged, and the commit is retried
- Recovery is bounded — at most three commit attempts — so a path that cannot be fixed never loops forever
- When a sanitized name would land on top of an existing file, a numeric suffix is used instead of overwriting it
- If the rename itself fails, the tool does not give up on the cycle: it reports the path and the failure and continues
- When recovery runs out of attempts, the offending paths are unstaged so the working tree is clean enough to pull, and the final error names every still-offending path
- The existing conflict-marker recovery path is untouched — a marker refusal still resolves via the AI resolver, and the two recovery paths never cross
</summary>

<objective>
Extend `commit_with_marker_recovery` in `src/git_ai_sync/conflict_resolver.py` so it also recovers from an unsafe-path refusal: rename each offending path to its sanitized name, re-stage, retry the commit up to three attempts, and on exhaustion unstage the offending paths and raise `UnsafePathRefusalError` naming them. Add the `unstage_paths` git operation it needs.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention.
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/conflict_resolver.py` — `commit_with_marker_recovery` (the function to extend), `resolve_marker_flagged_files`, `ConflictError`, and the module-level `logger`.
Read `src/git_ai_sync/git_operations.py` — `UnsafePathRefusalError`, `is_unsafe_path_refusal`, `get_blocked_paths`, `sanitize_path` (created by prompt 1 of this spec), plus `commit`, `stage_file`, `GitError`.
Read `tests/test_conflict_resolver.py` — `_marker_git_ops()` (sets `mock.GitError` and `mock.MarkerRefusalError`), `CONFLICT_CONTENT`, and the five existing marker-recovery tests (`test_commit_with_marker_recovery_success`, `…_bounded`, `…_early_stop`, `…_non_marker_error`, `…_no_flagged_files`) — these MUST keep passing unmodified.
Read `tests/conftest.py` — the `temp_dir` fixture.
</context>

<requirements>
1. In `src/git_ai_sync/git_operations.py`, add `unstage_paths(repo_path: Path, paths: list[str]) -> None` after `stage_file`:

   ```python
   def unstage_paths(repo_path: Path, paths: list[str]) -> None:
       """Unstage the given paths, keeping the files on disk.

       Uses `git reset -- <paths>` rather than `git restore --staged -- <paths>`:
       reset is lenient about a repository with no commits and about paths that are
       not in the index (both exit 0), whereas restore fails with
       `fatal: could not resolve 'HEAD'` (exit 128) or a pathspec error (exit 1).
       A brand-new repo whose very first commit is refused over an unsafe path is a
       real case, and it must not abort recovery with a `GitError`.

       Args:
           repo_path: Path to git repository
           paths: Relative paths to unstage

       Raises:
           GitError: If unstaging fails
       """
       if not paths:
           # Load-bearing: `git reset --` with no pathspec is a full mixed reset.
           return
       result = subprocess.run(
           ["git", "reset", "--", *paths],
           cwd=repo_path,
           capture_output=True,
           text=True,
           check=False,
       )
       if result.returncode != 0:
           raise GitError(f"Failed to unstage paths: {result.stderr}")
   ```

2. In `src/git_ai_sync/conflict_resolver.py`, add a module-level helper `_recover_unsafe_paths(repo_path: Path, blocked_paths: list[str]) -> list[str]` that renames each offending path to its sanitized name and returns the paths it could NOT fix:

   ```python
   def _recover_unsafe_paths(repo_path: Path, blocked_paths: list[str]) -> list[str]:
       """Rename each offending path to a Windows-safe name and re-stage it.

       Args:
           repo_path: Path to git repository
           blocked_paths: Paths the pre-commit hook refused as not safe on Windows

       Returns:
           Paths that could not be renamed (still offending)
       """
       failed: list[str] = []
       for old_path in blocked_paths:
           try:
               new_path = git_operations.sanitize_path(old_path)
               # Disambiguate against a file already on disk: never overwrite.
               # Operate on the FINAL path component only — rpartition on the whole
               # path would find a dot inside a directory name and rename the directory.
               if (repo_path / new_path).exists():
                   parent, _, leaf = new_path.rpartition("/")
                   stem, dot, ext = leaf.rpartition(".")
                   if not dot:
                       stem, dot, ext = leaf, "", ""
                   counter = 2
                   while True:
                       candidate_leaf = f"{stem}-{counter}{dot}{ext}"
                       candidate = f"{parent}/{candidate_leaf}" if parent else candidate_leaf
                       if not (repo_path / candidate).exists():
                           break
                       counter += 1
                   new_path = candidate
               (repo_path / new_path).parent.mkdir(parents=True, exist_ok=True)
               (repo_path / old_path).rename(repo_path / new_path)
               git_operations.stage_file(repo_path, old_path)
               git_operations.stage_file(repo_path, new_path)
               logger.info(f"Renamed unsafe path: {old_path} -> {new_path}")
           except (OSError, git_operations.GitError) as e:
               logger.error(f"Failed to rename unsafe path {old_path}: {e}")
               failed.append(old_path)
       return failed
   ```

   The collision disambiguation lives here (filesystem-driven) rather than being passed into `sanitize_path`, so the pure function stays pure and the recovery handles the real-world "does this name already exist" question. `sanitize_path` still supports an optional `existing` set (used by prompt 1's unit test) — pass no `existing` here. The disambiguation MUST key off the final path component (`leaf`), never the whole path: a dot inside a directory name (`my.dir/unsafe`) would otherwise make `rpartition` pick the directory and rename it.

   Add a module-level unit test `test_unstage_paths` for `git_operations.unstage_paths` in `tests/test_git_operations.py` (it tests a `git_operations` function, not one in `conflict_resolver.py`; follow that module's existing `_mock_result()` / `patch("subprocess.run")` style): assert it runs `git reset -- <paths>` for a non-empty list, and that an empty list is a no-op that runs no subprocess at all — the early-return branch is otherwise never exercised.

3. In `src/git_ai_sync/conflict_resolver.py`, add `from git_ai_sync.git_operations import UnsafePathRefusalError` to the module imports (the unsafe-path `except` clause must use this module-level name, NOT `git_operations.UnsafePathRefusalError` — the existing marker tests patch `conflict_resolver.git_operations` with a mock that does not define `UnsafePathRefusalError`, and `except <MagicMock instance>` raises `TypeError: catching classes that do not inherit from BaseException is not allowed`). Then extend `commit_with_marker_recovery` with a second recovery branch for unsafe-path refusals, keeping the existing marker branch's behaviour byte-for-byte — including its final `MarkerRefusalError` raise. The loop becomes:

   ```python
   max_attempts = 3
   attempt = 0
   blocked_paths: list[str] = []
   refusal = ""
   while True:
       attempt += 1
       try:
           git_operations.commit(repo_path, message)
           return
       except UnsafePathRefusalError as e:
           refusal = "unsafe"
           if attempt >= max_attempts:
               break
           blocked_paths = git_operations.get_blocked_paths(str(e))
           if not blocked_paths:
               break
           logger.info(
               f"Unsafe-path refusal detected; renaming {len(blocked_paths)} "
               f"offending path(s): {', '.join(blocked_paths)}"
           )
           still_failing = _recover_unsafe_paths(repo_path, blocked_paths)
           if still_failing:
               git_operations.unstage_paths(repo_path, still_failing)
               blocked_paths = still_failing
               logger.error(
                   f"Could not rename {len(still_failing)} offending path(s); "
                   f"unstaged them: {', '.join(still_failing)}"
               )
           logger.info("Recovery round complete; retrying commit")
       except git_operations.MarkerRefusalError:
           refusal = "marker"
           if attempt >= max_attempts:
               break
           flagged_files = git_operations.get_marker_flagged_files(repo_path)
           if not flagged_files:
               break
           logger.info(
               f"Marker refusal detected; resolving {len(flagged_files)} flagged "
               f"file(s): {', '.join(flagged_files)}"
           )
           try:
               await resolve_marker_flagged_files(repo_path, flagged_files, model)
           except ConflictError as e:
               raise git_operations.MarkerRefusalError(
                   f"Failed to resolve marker-flagged files: {e}. "
                   "Run 'git-ai-sync resolve <path>' to resolve"
               ) from e
           logger.info("Recovery round complete; retrying commit")

   if refusal == "marker":
       still_flagged = git_operations.get_marker_flagged_files(repo_path)
       raise git_operations.MarkerRefusalError(
           f"Commit refused by pre-commit hook after {max_attempts} attempts; "
           f"still-flagged files: {', '.join(still_flagged) or 'none'}. "
           "Run 'git-ai-sync resolve <path>' to resolve"
       )

   git_operations.unstage_paths(repo_path, blocked_paths)
   raise UnsafePathRefusalError(
       f"Commit refused by pre-commit hook after {max_attempts} attempts; "
       f"still-offending paths: {', '.join(blocked_paths) or 'none'}. "
       "Rename them to Windows-safe names and commit again"
   )
   ```

   Notes on the exact behavior the tests pin:
   - The `except UnsafePathRefusalError` clause MUST come BEFORE `except git_operations.MarkerRefusalError` (both are `GitError` subclasses; they are disjoint, but the order documents the intended priority).
   - There are FOUR `break` exits. The two unsafe-path `break`s fall through to the `unstage_paths` + `UnsafePathRefusalError` tail; the two marker `break`s fall through to the `MarkerRefusalError` tail. The `refusal` discriminator is what keeps the two tails separate — routing both through one tail would make a marker refusal raise `UnsafePathRefusalError` and break the five pre-existing marker tests.
   - When `blocked_paths` is empty the unstage is a no-op (`unstage_paths` returns early on an empty list) and the message reads `still-offending paths: none` — this is the "refusal with no parseable paths" case, and it still raises so the caller's pull guarantee applies.
   - On success the function `return`s before reaching either tail, so the marker tests' `assert mock_git.commit.call_count == 2` still holds.
   - The unsafe-path final error names the still-offending paths and does NOT contain `Run 'git-ai-sync resolve` (that pointer belongs to the marker path only).

4. Update the `commit_with_marker_recovery` docstring's `Raises:` section to add `UnsafePathRefusalError: If the commit is still refused over an unsafe path after the bounded recovery rounds (the final error names the still-offending paths)` alongside the existing `MarkerRefusalError` and `GitError` entries. Mention in the summary line that recovery covers both conflict markers and unsafe paths.

5. Add two module-level tests to `tests/test_conflict_resolver.py` (module-level, NOT inside a class — the node ids must match the spec's AC evidence paths). Both use a real temp git repo so the rename, re-staging, and `git status` assertions are real, not mocked. Add a module-level helper first:

   ```python
   def _init_repo(tmp_path: Path) -> None:
       subprocess.run(["git", "init", "-q"], cwd=tmp_path, check=True)
       subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, check=True)
       subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, check=True)
       subprocess.run(["git", "config", "commit.gpgsign", "false"], cwd=tmp_path, check=True)
       subprocess.run(["git", "config", "core.hooksPath", str(tmp_path / "no-hooks")], cwd=tmp_path, check=True)
   ```

   (`core.hooksPath` points at a non-existent dir so the host's global hook never runs inside the test.) Add `import subprocess`, `UnsafePathRefusalError`, and `sanitize_path` to the test module's imports (the tests call `sanitize_path("unsafe?.md")` to derive the expected name). Optionally also add `mock.UnsafePathRefusalError = UnsafePathRefusalError` to the existing `_marker_git_ops()` helper — this is **defence in depth only**: requirement 3 makes the loop catch the module-level `UnsafePathRefusalError` name, so `git_operations.UnsafePathRefusalError` is never evaluated and the five marker-recovery tests pass without this line. It guards only against a future edit that switches the clause to `git_operations.UnsafePathRefusalError`, which requirement 3 forbids.

   a. `test_commit_with_marker_recovery_unsafe_path_recovery_success` — a real repo containing `unsafe?.md` (staged), driven through the real recovery loop with a `subprocess.run` wrapper that refuses the FIRST `git commit` and delegates every other call to the real `subprocess.run`:

      ```python
      real_run = subprocess.run
      refused: list[bool] = []

      def fake_run(*args: object, **kwargs: object) -> subprocess.CompletedProcess[str]:
          argv = args[0] if args else kwargs.get("args")
          if isinstance(argv, list) and argv[:2] == ["git", "commit"] and not refused:
              refused.append(True)
              return subprocess.CompletedProcess(args=argv, returncode=1, stdout="", stderr=BLOCKED_STDERR)
          return real_run(*args, **kwargs)  # type: ignore[arg-type]
      ```

      where `BLOCKED_STDERR` is the hook's verbatim stderr block, inlined here so this prompt is self-contained:

      ```python
      BLOCKED_STDERR = (
          "\n"
          "✋ Blocked: staged path is not safe on Windows\n"
          "\n"
          "     unsafe?.md  —  contains one of  : ? * \" < > |\n"
          "\n"
          "   Windows rejects these characters in a path component: : ? * \" < > |\n"
          "   It also rejects a component ending in a dot or a space, and a name\n"
          "   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.\n"
      )
      ```

      (The reason string carries two spaces after `of` — that is the hook's own text, `~/.git-hooks/pre-commit:137`.) Patch `"subprocess.run"` with `fake_run`, `await commit_with_marker_recovery(tmp_path, "msg")`, then assert:
      - the commit succeeded: `git log --oneline -1` shows exactly one commit, and `git ls-tree -r HEAD --name-only` contains the sanitized path and not `unsafe?.md`. Derive the expected name by calling `sanitize_path("unsafe?.md")` rather than hard-coding it — under the corrected rule set the stem's trailing dash is trimmed, so it is `unsafe.md`, but deriving it keeps this test from drifting from prompt 1's rule set
      - the rename is visible: `git status --porcelain` no longer lists `unsafe?.md`
      - the collision branch (spec AC 4): run a second case in which a pre-existing file already occupies the sanitized name before the cycle (create and stage `sanitize_path("unsafe?.md")` — i.e. `unsafe.md` — with distinct content first); assert the landed path is the disambiguated candidate (the sanitized name plus the `-2` suffix, so `unsafe-2.md`) and that the pre-existing file's content is byte-identical afterwards, proving it was not overwritten
      - caplog (captured with `caplog.at_level("INFO")`) shows the INFO rename line naming both the old and new path (a record contains `unsafe?.md` and the sanitized name)
      - the wrapper was consulted for the commit at least twice (first refused, retry committed)

   b. `test_commit_with_marker_recovery_unsafe_path_recovery_bounded` — a real repo **that already has one commit**, with `unsafe?.md` **staged**. Both preconditions are load-bearing: the recovery's rename and the tail's unstage both need an index entry for the path, and a repo with no commits has no `HEAD` for the rename-and-restage pair to resolve against. Build it as: `_init_repo(tmp_path)`; write `seed.md`; `subprocess.run(["git", "add", "seed.md"], cwd=tmp_path, check=True)`; `subprocess.run(["git", "commit", "-q", "-m", "seed"], cwd=tmp_path, check=True)` — **outside** the `patch` context so the seed commit is not counted — then write `unsafe?.md` and `git add "unsafe?.md"`, also outside the patch. With `patch("git_ai_sync.conflict_resolver._recover_unsafe_paths", return_value=["unsafe?.md"])` (so every recovery round reports the path still offending) and a wrapper that refuses EVERY `git commit`, assert:
      - `pytest.raises(UnsafePathRefusalError)`, and the raised message contains `unsafe?.md` and `still-offending paths` and does NOT contain `Run 'git-ai-sync resolve`
      - exactly 3 commit attempts occurred (`len([c for c in calls if c[:2] == ["git","commit"]]) == 3`) — no 4th
      - `git status --porcelain` shows `unsafe?.md` as untracked (`??`), proving `unstage_paths` ran (it is no longer staged)

6. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk AC 4 and AC 5 from the spec against the change, and confirm the five pre-existing marker-recovery tests still pass unmodified.
</requirements>

<constraints>
- Reuse `sanitize_path` from `git_operations` for the rename target — do NOT re-implement the rule set in `conflict_resolver.py`
- The unsafe-path branch and the marker branch MUST stay disjoint: a marker refusal never triggers the rename path, and an unsafe-path refusal never invokes the AI resolver
- Total attempts bounded at 3 (1 initial + 2 recovery rounds) — never a 4th
- On exhaustion the offending paths MUST be unstaged before raising, so the caller's pull has a clean working tree (Desired Behavior 4)
- Use a filesystem rename (`Path.rename`) followed by staging both the old and new path — `git mv` is not required and does not work for untracked files; the `git status` assertion in AC 4 is what proves the change is visible in the same cycle
- Path renaming must never touch file contents — path components only
- Preserve the existing error-handling shape: typed `GitError` subclasses, `subprocess.run(..., check=False)`
- Type hints on all new functions (mypy strict); pytest conventions only (`asyncio_mode = "auto"`); no `unittest`
- All new tests are module-level functions (node ids must match the spec's AC evidence paths: `test_commit_with_marker_recovery_unsafe_path_recovery_success`, `test_commit_with_marker_recovery_unsafe_path_recovery_bounded`)
- Extend the existing `## Unreleased` section in `CHANGELOG.md` (do not add a second one) with a `fix:` bullet. If no `## Unreleased` section exists, insert one directly above the highest `## vX.Y.Z` (currently `## v0.12.1`) rather than skipping the bullet:

  ```
  - fix: Auto-recover when the pre-commit hook refuses a commit over a path that is not safe on Windows — rename each offending path to a Windows-safe name, re-stage, and retry up to 3 attempts; on exhaustion unstage the offending paths and raise with every still-offending path named
  ```
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new acceptance-criteria tests pass:
`uv run pytest tests/test_conflict_resolver.py -k unsafe_path_recovery -v`
Confirm the five marker-recovery tests are unmodified and still pass:
`uv run pytest tests/test_conflict_resolver.py -k marker_recovery -v`
</verification>
