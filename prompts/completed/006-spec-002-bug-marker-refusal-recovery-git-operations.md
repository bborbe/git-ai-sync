---
status: completed
spec: [002-bug-marker-refusal-recovery]
summary: Added marker-refusal detection to git_operations.py (MarkerRefusalError, is_marker_refusal, get_marker_flagged_files) and classified commit refusals, with 8 module-level tests and a CHANGELOG entry; also added hatch-vcs fallback-version so the build works under hideGit
execution_id: git-ai-sync-marker-refusal-exec-006-spec-002-bug-marker-refusal-recovery-git-operations
dark-factory-version: dev
created: "2026-09-02T21:20:00Z"
queued: "2026-09-02T21:25:33Z"
started: "2026-09-02T21:25:34Z"
completed: "2026-09-02T21:30:03Z"
---

<summary>
- Commits refused by the global pre-commit hook over conflict-marker content are now classified as a dedicated marker-refusal error instead of a generic git error
- A case-insensitive classifier recognizes the hook's refusal signature in stderr output
- A new git operation lists exactly the staged files the hook flags, using the hook's own `git diff` pattern (not a re-implementation of the check)
- Non-marker commit failures still raise the plain generic git error — nothing else changes
- The marker-refusal error is a subclass of the existing git error, so all current error handling keeps working unchanged
</summary>

<objective>
Add the marker-refusal detection layer to `src/git_ai_sync/git_operations.py`: a `MarkerRefusalError` exception, an `is_marker_refusal` classifier, and a `get_marker_flagged_files` operation, so the later prompts of this spec can distinguish hook-refused commits from ordinary commit failures.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest + pytest-asyncio).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/git_operations.py` — `commit()` (the function to extend), `get_conflicted_files()` (the pattern to mirror for output parsing), and `GitError`.
Read `tests/test_git_operations.py` — `_mock_result()` helper, `REPO` constant, and `TestCommit` (the existing test pattern).
</context>

<requirements>
1. In `src/git_ai_sync/git_operations.py`, add a new exception class directly after the existing `GitError` class:

   ```python
   class MarkerRefusalError(GitError):
       """Commit refused by the pre-commit hook because staged content contains conflict markers."""

       pass
   ```

2. Add `is_marker_refusal(message: str) -> bool`:

   ```python
   def is_marker_refusal(message: str) -> bool:
       """Check whether an error message matches the pre-commit hook's marker-refusal signature.

       Args:
           message: Error message text (typically git stderr)

       Returns:
           True if the message contains "Refusing commit" or "conflict markers" (case-insensitive)
       """
       lowered = message.lower()
       return "refusing commit" in lowered or "conflict markers" in lowered
   ```

3. Add `get_marker_flagged_files(repo_path: Path) -> list[str]` — mirrors `get_conflicted_files()`'s structure and output parsing, but runs the hook's own verbatim pattern. The `subprocess.run` argument list MUST be exactly:

   ```python
   ["git", "diff", "--cached", "--diff-filter=AM", "-G", "^<<<<<<< ", "--name-only"]
   ```

   (The regex `^<<<<<<< ` ends with a single space — preserve it character-for-character; this is the hook's pattern pinned by the spec.) Behavior:
   - `subprocess.run(...)` with `cwd=repo_path`, `capture_output=True`, `text=True`, `check=False` (the established error-handling shape).
   - On non-zero returncode: `raise GitError(f"Failed to get marker-flagged files: {result.stderr}")`.
   - On success: parse exactly like `get_conflicted_files()` — `[line.strip() for line in result.stdout.strip().split("\n") if line.strip()]` — and return the list.
   - Full docstring in the existing style: "List staged files whose hunks contain conflict markers (the pre-commit hook's own pattern)." with a `Raises:` section for `GitError`.

4. Modify `commit()` so it classifies the refusal. Replace the failure branch:

   OLD:
   ```python
       if result.returncode != 0:
           raise GitError(f"Failed to commit: {result.stderr}")
   ```

   NEW:
   ```python
       if result.returncode != 0:
           if is_marker_refusal(result.stderr):
               raise MarkerRefusalError(f"Failed to commit: {result.stderr}")
           raise GitError(f"Failed to commit: {result.stderr}")
   ```

   Update the `commit()` docstring's `Raises:` section to mention `MarkerRefusalError` for marker refusals alongside `GitError`. Because `MarkerRefusalError` subclasses `GitError`, no existing caller or test changes behavior until the wiring prompt of this spec adds the specific catch.

5. Add module-level test functions to `tests/test_git_operations.py` (module-level, NOT inside a class — the node ids must match the spec's acceptance-criteria evidence paths exactly). Add the new names (`MarkerRefusalError`, `get_marker_flagged_files`, `is_marker_refusal`) to the import block from `git_ai_sync.git_operations`.

   a. `test_is_marker_refusal` — asserts True for the hook's actual message `"✋ Refusing commit: staged changes contain git merge conflict markers"`, for each signature token alone (`"Refusing commit"`, `"conflict markers"`), and for case-insensitive variants (`"REFUSING COMMIT"`, `"Conflict Markers"`).

   b. `test_not_marker_refusal` — asserts False for an unrelated commit failure (`"Failed to commit: index.lock exists"`) and for `"Merge conflicts detected - use 'git-ai-sync resolve' to resolve"` (contains "conflict" but NOT "conflict markers" — this is the pinned negative case: the pull-conflict message must NOT be classified as a marker refusal).

   c. `test_get_marker_flagged_files` — with `patch("subprocess.run", return_value=_mock_result("conflict.md\nclean.md\n"))` asserts the return value is `["conflict.md", "clean.md"]` AND that the exact subprocess argv was used:

      ```python
      mock.call_args[0][0] == ["git", "diff", "--cached", "--diff-filter=AM", "-G", "^<<<<<<< ", "--name-only"]
      ```

   d. `test_get_marker_flagged_files_empty` — `_mock_result("")` returns `[]`.

   e. `test_get_marker_flagged_files_raises` — `_mock_result(returncode=1, stderr="bad")` raises `GitError`.

   f. `test_commit_raises_marker_refusal` — `commit(REPO, "msg")` with `_mock_result(returncode=1, stderr="✋ Refusing commit: staged changes contain git merge conflict markers")` raises `MarkerRefusalError`.

   g. `test_commit_raises_generic_error_for_non_marker` — `commit(REPO, "msg")` with `_mock_result(returncode=1, stderr="Failed to commit: index.lock exists")` raises `GitError` and the raised instance is NOT a `MarkerRefusalError`.

   h. `test_marker_refusal_error_is_git_error` — asserts `issubclass(MarkerRefusalError, GitError)` (this is what lets existing `except git_operations.GitError` handlers keep working).

6. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk each acceptance criterion from the spec (AC 1 classification, AC 2 flagged-file listing, AC 8 non-marker/regression) against the change.
</requirements>

<constraints>
- Do NOT weaken, bypass, or extend the global pre-commit hook — the fix belongs in git-ai-sync's recovery
- Flagged-file detection must use the hook's own pattern verbatim: `git diff --cached --diff-filter=AM -G '^<<<<<<< ' --name-only`
- Preserve existing error-handling shape: `subprocess.run` with `check=False`, `GitError` subclasses before broad `except Exception`
- Non-marker commit failures and all pull/push behavior must be unchanged
- Type hints on all new functions (mypy strict); pytest conventions only
- All new tests are module-level functions (node ids must match the spec's AC evidence paths)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Follow the existing code style in `git_operations.py`: module-level functions, full docstrings, `subprocess.run` with `check=False`
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the acceptance-criteria tests pass:
`uv run pytest tests/test_git_operations.py::test_is_marker_refusal tests/test_git_operations.py::test_not_marker_refusal tests/test_git_operations.py::test_get_marker_flagged_files -v`
Confirm the commit classification: `uv run pytest tests/test_git_operations.py -k "marker" -v`
</verification>
