---
status: completed
spec: [004-bug-unsafe-path-refusal-skips-pull]
summary: Added test_commit_with_marker_recovery_marker_path_unchanged to lock the conflict-marker recovery path against the unsafe-path recovery path
execution_id: git-ai-sync-exec-020-spec-004-marker-path-regression-lock
dark-factory-version: v0.196.0
created: "2026-10-11T14:30:00Z"
queued: "2026-10-11T12:54:21Z"
started: "2026-10-11T13:10:55Z"
completed: "2026-10-11T13:12:07Z"
---

<summary>
- Adds a regression test proving the earlier conflict-marker recovery is untouched by this spec's new unsafe-path recovery
- The test drives a marker refusal through the shared commit-recovery function and asserts the AI resolver is invoked and the unsafe-path rename path is never touched
- The two refusal classes stay provably disjoint — one never triggers the other's recovery
- The five existing marker-recovery tests and the two existing marker-refusal exit tests keep passing unchanged, so the sibling bug's behaviour is locked in
- Test-only change: no production code is modified
</summary>

<objective>
Add a regression test to `tests/test_conflict_resolver.py` that proves a conflict-marker refusal still takes the existing spec-002 recovery path (AI resolver invoked, `MarkerRefusalError` raised on final failure) and never triggers the unsafe-path rename path, so the two recovery classes stay disjoint.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest-asyncio `asyncio_mode = "auto"`).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/conflict_resolver.py` — `commit_with_marker_recovery` (now carrying BOTH the marker branch and the unsafe-path branch added by prompt 2 of this spec), `_recover_unsafe_paths`, and `resolve_marker_flagged_files`.
Read `src/git_ai_sync/git_operations.py` — `MarkerRefusalError`, `UnsafePathRefusalError`, `is_marker_refusal`, `is_unsafe_path_refusal`.
Read `tests/test_conflict_resolver.py` — `_marker_git_ops()` (sets `mock.GitError` and `mock.MarkerRefusalError`), and the five existing marker-recovery tests (`test_commit_with_marker_recovery_success`, `…_bounded`, `…_early_stop`, `…_non_marker_error`, `…_no_flagged_files`).
Read `tests/test_main.py` — `test_sync_marker_refusal_exits` and `test_watch_marker_refusal_exits` (the exit-code half of the sibling behaviour).
</context>

<requirements>
1. Add one module-level test to `tests/test_conflict_resolver.py`: `test_commit_with_marker_recovery_marker_path_unchanged`. It must:
   - Build the mock git operations with the real classes attached, including the new one. Add `mock.UnsafePathRefusalError = UnsafePathRefusalError` to the existing `_marker_git_ops()` helper. This is defence in depth: requirement 3 of prompt 2 makes the recovery loop catch the module-level `UnsafePathRefusalError` name, so `git_operations.UnsafePathRefusalError` is not normally evaluated — but the helper is shared by eight tests, and if the clause ever reads the patched module attribute instead, a MagicMock auto-attribute is not a catchable exception class and the five marker-recovery tests die with `TypeError: catching classes that do not inherit from BaseException is not allowed`. Setting it on the helper (not on the mock returned inside this one test) is what covers all of them. Add `UnsafePathRefusalError` to the test module's `from git_ai_sync.git_operations import ...` line.
   - Set `mock_git.commit.side_effect = [MarkerRefusalError("refused"), None]` and `mock_git.get_marker_flagged_files.return_value = ["conflict.md"]`.
   - Patch `git_ai_sync.conflict_resolver.git_operations` with that mock, patch `git_ai_sync.conflict_resolver.resolve_marker_flagged_files` with an `AsyncMock`, and patch `git_ai_sync.conflict_resolver._recover_unsafe_paths` with a `MagicMock`.
   - `await commit_with_marker_recovery(temp_dir, "msg")`.
   - Assert the marker path ran: `mock_git.commit.call_count == 2` (initial + retry) and `mock_git.get_marker_flagged_files.assert_called_once()`. For the resolver, do NOT use `assert_awaited_once_with(temp_dir, ["conflict.md"])` — `resolve_marker_flagged_files` is called with three positional args `(repo_path, flagged_files, model)`, so that assertion fails on the model argument. Assert `mock_resolver.assert_awaited_once()` and then check `mock_resolver.await_args.args[0] == temp_dir` and `mock_resolver.await_args.args[1] == ["conflict.md"]`, exactly as `test_commit_with_marker_recovery_success` does.
   - Assert the unsafe-path path did NOT run: the `_recover_unsafe_paths` mock was NOT called, and `mock_git.get_blocked_paths` was NOT called.

2. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; confirm the five pre-existing marker-recovery tests and the two marker-refusal exit tests are unmodified.
</requirements>

<constraints>
- Test-only change — do NOT modify production code in `git_operations.py`, `conflict_resolver.py`, or `__main__.py`
- The existing marker-recovery tests MUST keep passing without modification
- Type hints on the new test functions (mypy strict); pytest conventions only (`asyncio_mode = "auto"`); no `unittest`
- New tests are module-level functions, and the new test's name must be selected by AC 10's evidence selector `-k marker_recovery` (so it must contain `marker_recovery`)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the new regression test passes:
`uv run pytest tests/test_conflict_resolver.py -k marker_path_unchanged -v`
Confirm the whole marker-recovery selector (five existing tests + the new one) passes:
`uv run pytest tests/test_conflict_resolver.py -k marker_recovery -v`
Confirm the marker-refusal exit tests pass:
`uv run pytest tests/test_main.py::test_sync_marker_refusal_exits tests/test_main.py::test_watch_marker_refusal_exits -v`
</verification>
