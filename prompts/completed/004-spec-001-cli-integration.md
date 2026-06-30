---
status: completed
spec: [001-instance-locking]
summary: Wired acquire_lock into cmd_watch, cmd_sync, and cmd_resolve in __main__.py with LockError handling; appended .git-ai-sync.lock to .gitignore; added three lock-error tests and patched acquire_lock in existing TestCmdSync tests that used a non-existent /repo path.
execution_id: git-ai-sync-004-spec-001-cli-integration
dark-factory-version: v0.59.5-dirty
created: "2026-03-20T00:00:00Z"
queued: "2026-03-20T14:04:51Z"
started: "2026-03-20T14:07:10Z"
completed: "2026-03-20T14:09:25Z"
branch: dark-factory/instance-locking
---

<summary>
- The watch, sync, and resolve commands each acquire the instance lock before executing
- A second instance on the same repository fails immediately with exit code 1 and a message naming the competing process's PID
- The lock is released automatically on clean exit, SIGINT, or SIGTERM (kernel releases on crash)
- The status, config, version, and doctor commands are unaffected — they do not acquire the lock
- `.git-ai-sync.lock` is added to `.gitignore` so the lock file is never accidentally committed
- Tests confirm all three locked commands reject a second invocation, and that unlocked commands are unaffected
</summary>

<objective>
Wire `instance_lock.acquire_lock` into `cmd_watch`, `cmd_sync`, and `cmd_resolve` in `src/git_ai_sync/__main__.py`, and add `.git-ai-sync.lock` to `.gitignore`. After this prompt, only one git-ai-sync instance can operate on a repository at a time.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff, pytest-asyncio).
Read `src/git_ai_sync/__main__.py` — the full file, to understand where git_repo is resolved in each command and where to insert the lock call.
Read `src/git_ai_sync/instance_lock.py` — the lock module created by the prior prompt (spec 001, prompt 1). It exports `acquire_lock(repo_root: Path) -> None` and `LockError`.
Read `tests/test_main.py` — to understand the existing test style for CLI commands.
Read `.gitignore` — to append the lock file entry correctly.
</context>

<requirements>
1. In `src/git_ai_sync/__main__.py`, add lock acquisition to three commands. For each command, the pattern is the same: after `git_repo` is resolved (i.e., after the `find_git_repo` call that validates the repository and assigns `git_repo`), import and call `acquire_lock`, and handle `LockError` with `sys.exit(1)`.

   **`cmd_watch`** — insert after line `logger.info(f"Watching: {git_repo}")`:
   ```python
   from git_ai_sync.instance_lock import LockError, acquire_lock
   try:
       acquire_lock(git_repo)
   except LockError as e:
       logger.error(str(e))
       sys.exit(1)
   ```

   **`cmd_sync`** — insert after `logger.info(f"Git repository: {git_repo}")`:
   ```python
   from git_ai_sync.instance_lock import LockError, acquire_lock
   try:
       acquire_lock(git_repo)
   except LockError as e:
       logger.error(str(e))
       sys.exit(1)
   ```

   **`cmd_resolve`** — insert after `git_repo = git_operations.find_git_repo(repo_path)` / the `if not git_repo` guard (i.e., before the `is_in_conflict_state` check):
   ```python
   from git_ai_sync.instance_lock import LockError, acquire_lock
   try:
       acquire_lock(git_repo)
   except LockError as e:
       logger.error(str(e))
       sys.exit(1)
   ```

   Note: Place the imports inside the function body (consistent with the existing style in `__main__.py` where all imports are local to cmd_* functions).

2. Append to `.gitignore` (at the end of the file, after the existing Dark Factory block):
   ```
   # Instance lock
   /.git-ai-sync.lock
   ```

3. Add tests in `tests/test_main.py` for lock acquisition in the three commands. Use `unittest.mock.patch` to mock `git_ai_sync.instance_lock.acquire_lock` (or import it via `__main__`). Each test verifies that when `acquire_lock` raises `LockError`, the command calls `sys.exit(1)`.

   Add the following test cases (follow the existing test structure and fixtures in `test_main.py`):

   a. **`test_cmd_watch_exits_on_lock_error`** — patches `acquire_lock` to raise `LockError("another instance is already running (pid 99999)")`, patches `find_git_repo` to return a valid path, and asserts `sys.exit(1)` is raised:
      ```python
      def test_cmd_watch_exits_on_lock_error(tmp_path, monkeypatch):
          import argparse
          from unittest.mock import patch, MagicMock
          from git_ai_sync.__main__ import cmd_watch
          from git_ai_sync.instance_lock import LockError

          args = argparse.Namespace(path=str(tmp_path), interval=30)
          with patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path), \
               patch("git_ai_sync.instance_lock.acquire_lock", side_effect=LockError("another instance is already running (pid 99999)")), \
               patch("git_ai_sync.file_watcher.ChangeTracker"), \
               pytest.raises(SystemExit) as exc_info:
              cmd_watch(args)
          assert exc_info.value.code == 1
      ```

   b. **`test_cmd_sync_exits_on_lock_error`**:
      ```python
      def test_cmd_sync_exits_on_lock_error(tmp_path, monkeypatch):
          import argparse
          from unittest.mock import patch
          from git_ai_sync.__main__ import cmd_sync
          from git_ai_sync.instance_lock import LockError

          args = argparse.Namespace(path=str(tmp_path))
          with patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path), \
               patch("git_ai_sync.git_operations.get_current_branch", return_value="main"), \
               patch("git_ai_sync.instance_lock.acquire_lock", side_effect=LockError("another instance is already running (pid 99999)")), \
               pytest.raises(SystemExit) as exc_info:
              cmd_sync(args)
          assert exc_info.value.code == 1
      ```

   c. **`test_cmd_resolve_exits_on_lock_error`**:
      ```python
      def test_cmd_resolve_exits_on_lock_error(tmp_path, monkeypatch):
          import argparse
          from unittest.mock import patch
          from git_ai_sync.__main__ import cmd_resolve
          from git_ai_sync.instance_lock import LockError

          args = argparse.Namespace(path=str(tmp_path))
          with patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path), \
               patch("git_ai_sync.instance_lock.acquire_lock", side_effect=LockError("another instance is already running (pid 99999)")), \
               pytest.raises(SystemExit) as exc_info:
              cmd_resolve(args)
          assert exc_info.value.code == 1
      ```

   Note: The patch path for `acquire_lock` must match where it is imported inside the cmd_* function. Since the import is `from git_ai_sync.instance_lock import LockError, acquire_lock` inside the function body, patch the source: `"git_ai_sync.instance_lock.acquire_lock"`.
</requirements>

<constraints>
- Use OS-level exclusive non-blocking flock from `instance_lock.py` — do NOT re-implement locking logic in `__main__.py`
- Lock file location is always the git repo root (`git_repo`, not `repo_path`) — must be the resolved repo root returned by `find_git_repo`
- Lock is acquired AFTER the git repo is validated (after `find_git_repo` succeeds and `git_repo` is set) — do NOT attempt to lock before knowing the repo root
- The status, config, version, and doctor commands must NOT acquire the lock
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Type hints required (mypy strict mode)
- Follow the local-import style already used in `cmd_watch`, `cmd_sync`, `cmd_resolve` (all imports inside the function body)
</constraints>

<verification>
Run `make precommit` — must pass.
Confirm three new tests pass: `uv run pytest tests/test_main.py -v -k "lock"`
Confirm `.git-ai-sync.lock` appears in `.gitignore`: `grep git-ai-sync.lock .gitignore`
</verification>
