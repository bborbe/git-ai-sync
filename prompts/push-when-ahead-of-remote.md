---
status: created
created: "2026-03-19T12:32:53Z"
---

<summary>
- Watch mode pushes local commits to remote even when no new working tree changes exist
- External tools (e.g., Claude Code) that commit directly are no longer invisible to sync
- The sync cycle detects "clean but ahead" state and pushes without requiring local file changes
- A new check detects unpushed commits regardless of how they were created
- Existing behavior for dirty worktrees is unchanged
</summary>

<objective>
Fix watch mode so it pushes to remote when the local branch is ahead of the remote tracking branch, even if there are no uncommitted working tree changes. Currently, when an external tool commits directly (bypassing the file watcher), the commit stays local forever because the sync loop skips push when `has_changes` is False.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `src/git_ai_sync/__main__.py` — find the `cmd_watch` function, specifically the sync loop starting around the `has_local_changes` variable.
Read `src/git_ai_sync/git_operations.py` — find the existing git helper functions (`has_changes`, `push`, `get_current_branch`).
</context>

<requirements>
1. Add a new function `is_ahead_of_remote(repo_path: Path) -> bool` to `src/git_ai_sync/git_operations.py`:
   - Run `git rev-list --count @{upstream}..HEAD` to get the number of commits ahead
   - Return `True` if count > 0
   - If the command fails (e.g., no upstream configured), return `False` (don't crash). Note: this differs from other functions in the file which raise `GitError` — returning `False` is intentional here because this is a query, not an action
   - Add docstring following the existing style in the file

2. In `cmd_watch` in `src/git_ai_sync/__main__.py`, replace the `if not has_local_changes` guard (after the pull_rebase block) with an ahead-of-remote check. The current code to replace:
   ```python
   if not has_local_changes:
       logger.info("No local changes")
       continue
   ```
   Replace with logic that:
   - Calls `git_operations.is_ahead_of_remote(git_repo)`
   - If `not has_local_changes` AND NOT ahead of remote: log "No local changes" and `continue` (existing behavior)
   - If `not has_local_changes` BUT ahead of remote: log "Local branch is ahead of remote, pushing...", call `git_operations.push(git_repo)`, log "Pushed to remote", then `continue`
   - If `has_local_changes`: fall through to the existing push block (unchanged)

3. Add tests in `tests/test_git_operations.py` for the new `is_ahead_of_remote` function:
   - Test returns `False` when command fails (mock subprocess returning non-zero)
   - Test returns `False` when count is 0
   - Test returns `True` when count > 0
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All paths are repo-relative
- Follow the existing code style: functions (not classes) in `git_operations.py`, subprocess.run with `check=False`
- Type hints required (mypy strict mode)
- Keep the debounce/skip logic unchanged — only change what happens after pull when there are no local worktree changes
</constraints>

<verification>
Run `make precommit` -- must pass.
</verification>
