---
status: approved
spec: [002-bug-marker-refusal-recovery]
created: "2026-09-02T21:20:00Z"
queued: "2026-09-02T21:25:33Z"
---

<summary>
- Files flagged by the pre-commit hook for conflict markers are resolved with the existing AI resolver, written back to disk, and re-staged
- The refused commit is retried after resolution, with total attempts bounded to 3 (1 initial + 2 recovery rounds)
- If a flagged file cannot be resolved, recovery stops immediately and raises an error naming that file
- If markers persist after re-staging, each further refusal counts against the bound and at 3 attempts the final error names the still-flagged files
- Non-marker commit failures propagate unchanged, and a successful retry returns cleanly so the normal sync flow continues
</summary>

<objective>
Add the resolution and bounded-recovery loop to `src/git_ai_sync/conflict_resolver.py`: `resolve_marker_flagged_files` (resolve + write + re-stage, stopping early on failure) and `commit_with_marker_recovery` (bounded retry around `git_operations.commit`).
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/conflict_resolver.py` — `resolve_all_conflicts()` (the read → resolve → write → stage pattern to mirror), `resolve_conflict_with_claude()`, `ConflictError`, and the module-level `logger`.
Read `src/git_ai_sync/git_operations.py` — `MarkerRefusalError`, `get_marker_flagged_files`, `commit`, `stage_file` (created/updated by the prior prompt of this spec).
Read `tests/test_conflict_resolver.py` — `CONFLICT_CONTENT`, `_patch_git_ops()` (sets `mock.GitError = GitError`), and the `TestResolveAllConflicts` style (patch `git_ai_sync.conflict_resolver.git_operations` with a mock and patch `resolve_conflict_with_claude` with an AsyncMock).
</context>

<requirements>
1. Add `async def resolve_marker_flagged_files(repo_path: Path, flagged_files: list[str], model: str = "claude-sonnet-4-5-20250929") -> None` to `src/git_ai_sync/conflict_resolver.py`. It must:
   - Iterate `flagged_files` in order; for each `file_path`:
     - `content = (repo_path / file_path).read_text(encoding="utf-8")`
     - `resolved_content = await resolve_conflict_with_claude(file_path, content, model)` — reuse the existing resolver, do NOT write a second resolution mechanism
     - `(repo_path / file_path).write_text(resolved_content, encoding="utf-8")`
     - `git_operations.stage_file(repo_path, file_path)`
     - `logger.info(f"Resolved and staged {file_path}")` (mirror `resolve_all_conflicts`)
   - STOP EARLY on failure: on `git_operations.GitError` or `ConflictError` for any file, immediately `raise ConflictError(f"Failed to resolve marker-flagged file {file_path}: {e}") from e` — do NOT continue to later files.
   - Docstring: "Resolve conflict markers in files flagged by the pre-commit hook and re-stage them." with a `Raises:` section: `ConflictError — if resolution or staging fails on any file (stops early)`.
   - Only write to files listed in `flagged_files` (already-staged repo files returned by the hook's own pattern) — no new trust boundary.

2. Add `async def commit_with_marker_recovery(repo_path: Path, message: str, model: str = "claude-sonnet-4-5-20250929") -> None` to the same module. Behavior:
   - `max_attempts = 3` (1 initial attempt + 2 recovery rounds — the spec's hard bound; NOT configurable). Loop structure:
     ```python
     max_attempts = 3
     attempt = 0
     while True:
         attempt += 1
         try:
             git_operations.commit(repo_path, message)
             return
         except git_operations.MarkerRefusalError:
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
     still_flagged = git_operations.get_marker_flagged_files(repo_path)
     raise git_operations.MarkerRefusalError(
         f"Commit refused by pre-commit hook after {max_attempts} attempts; "
         f"still-flagged files: {', '.join(still_flagged) or 'none'}. "
         "Run 'git-ai-sync resolve <path>' to resolve"
     )
     ```
   - The final error MUST name the still-flagged files and contain the exact pointer `Run 'git-ai-sync resolve <path>' to resolve`.
   - If `resolve_marker_flagged_files` raises `ConflictError`, recovery stops immediately — the raised `MarkerRefusalError` names the failing file (the `ConflictError` message embeds it) and no further retry happens.
   - Any non-marker `git_operations.GitError` from `commit()` propagates unchanged (it is NOT caught by the `MarkerRefusalError` handler) — non-marker commit failures behave exactly as today.
   - Docstring: "Commit with bounded auto-recovery when the pre-commit hook refuses the commit over conflict-marker content." with `Raises:` sections for `MarkerRefusalError` (final refusal, names still-flagged files) and `GitError` (non-marker failures, propagated unchanged).

3. Add a module-level mock helper to `tests/test_conflict_resolver.py`:

   ```python
   def _marker_git_ops() -> MagicMock:
       """Create a mock git_operations with real GitError and MarkerRefusalError."""
       mock = MagicMock()
       mock.GitError = GitError
       mock.MarkerRefusalError = MarkerRefusalError
       return mock
   ```

   Add module-level test functions (module-level, NOT inside a class — the node ids must match the spec's acceptance-criteria evidence paths exactly). Extend the imports: from `git_ai_sync.conflict_resolver` add `commit_with_marker_recovery` and `resolve_marker_flagged_files`; from `git_ai_sync.git_operations` add `MarkerRefusalError` (keep `GitError`). All async tests run under `asyncio_mode = "auto"` — no decorators needed.

   a. `test_resolve_marker_flagged_files` — write `CONFLICT_CONTENT` to `temp_dir / "file.md"`; patch `git_ai_sync.conflict_resolver.git_operations` with a mock and `git_ai_sync.conflict_resolver.resolve_conflict_with_claude` (AsyncMock, `return_value="resolved"`). After `await resolve_marker_flagged_files(temp_dir, ["file.md"])`: the file on disk is `"resolved"` and `mock_git.stage_file.assert_called_once_with(temp_dir, "file.md")`.

   b. `test_resolve_marker_flagged_files_multiple` — two flagged files (`a.md`, `b.md`), each with `CONFLICT_CONTENT`; `resolve_conflict_with_claude` side_effect `["resolved-a", "resolved-b"]`; assert both files rewritten to their respective resolved content and `mock_git.stage_file.call_count == 2` (all flagged files resolved in one round — spec failure-mode "Multiple flagged files").

   c. `test_resolve_marker_flagged_files_stops_on_failure` — `resolve_conflict_with_claude` side_effect `ConflictError("Claude API call failed")`; `pytest.raises(ConflictError, match="file.md")`; assert `mock_git.stage_file.assert_not_called()` (no write/stage after the failure — early stop).

   d. `test_commit_with_marker_recovery_success` — `mock_git = _marker_git_ops()` with `commit.side_effect = [MarkerRefusalError("refused"), None]` and `get_marker_flagged_files.return_value = ["file.md"]`; patch `git_ai_sync.conflict_resolver.resolve_marker_flagged_files` (AsyncMock). `await commit_with_marker_recovery(temp_dir, "msg")` returns without raising; assert `mock_git.commit.call_count == 2` and the resolver mock was awaited once with first arg `temp_dir` and second arg `["file.md"]`.

   e. `test_commit_with_marker_recovery_bounded` — `commit.side_effect = MarkerRefusalError("refused")` (always raises), `get_marker_flagged_files.return_value = ["file.md"]`; patch the resolver (AsyncMock). `pytest.raises(MarkerRefusalError) as exc_info`; assert `mock_git.commit.call_count == 3` — the initial attempt plus 2 recovery rounds, NO 4th attempt — and `"Run 'git-ai-sync resolve" in str(exc_info.value)`.

   f. `test_commit_with_marker_recovery_early_stop` — `commit.side_effect = MarkerRefusalError("refused")`, `get_marker_flagged_files.return_value = ["file.md"]`; patch the resolver (AsyncMock, `side_effect=ConflictError("Failed to resolve marker-flagged file file.md: Claude API call failed")` — embed the file name, mirroring what the real `resolve_marker_flagged_files` raises, or the final error cannot name it). `pytest.raises(MarkerRefusalError, match="file.md")`; assert `mock_git.commit.call_count == 1` — no retry after resolution failure.

   g. `test_commit_with_marker_recovery_non_marker_error` — `commit.side_effect = GitError("Failed to commit: index.lock exists")`. `pytest.raises(GitError, match="index.lock")`; assert `get_marker_flagged_files` was never called (non-marker failure propagates with no recovery attempt).

   h. `test_commit_with_marker_recovery_no_flagged_files` — `commit.side_effect = MarkerRefusalError("refused")`, `get_marker_flagged_files.return_value = []`; patch the resolver (AsyncMock). `pytest.raises(MarkerRefusalError)`; assert the resolver was never awaited (nothing flagged → no pointless resolution round).

4. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk each acceptance criterion from the spec (AC 3 auto-recovery success, AC 4 bounded retries, AC 5 early stop) against the change.
</requirements>

<constraints>
- Reuse the existing `resolve_conflict_with_claude` — do NOT write a second resolution mechanism
- Flagged-file detection must use the hook's own pattern via `git_operations.get_marker_flagged_files` (created in the prior prompt of this spec)
- Recovery is bounded: exactly 3 total commit attempts (1 initial + 2 recovery rounds) — no configurable retry count
- On resolution failure, stop early and name the failing file — do not continue to other files
- Non-marker commit failures and all pull/push behavior must be unchanged — `commit_with_marker_recovery` propagates non-marker `GitError` untouched
- Preserve existing error-handling shape: `GitError` subclasses before broad `except Exception`
- Only write to files returned by `get_marker_flagged_files` (already-staged repo files); no new trust boundary
- Type hints on all new functions (mypy strict); pytest conventions only
- All new tests are module-level functions (node ids must match the spec's AC evidence paths)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test).
Confirm the acceptance-criteria tests pass:
`uv run pytest tests/test_conflict_resolver.py::test_commit_with_marker_recovery_success tests/test_conflict_resolver.py::test_commit_with_marker_recovery_bounded tests/test_conflict_resolver.py::test_commit_with_marker_recovery_early_stop -v`
Confirm all marker tests: `uv run pytest tests/test_conflict_resolver.py -k "marker" -v`
</verification>
