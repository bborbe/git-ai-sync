---
status: completed
spec: [001-instance-locking]
summary: Created src/git_ai_sync/instance_lock.py with OS-level exclusive flock and tests/test_instance_lock.py with 6 tests covering acquisition, permissions, contention, stale lock, and state cleanup.
execution_id: git-ai-sync-003-spec-001-instance-lock-module
dark-factory-version: v0.59.5-dirty
created: "2026-03-20T00:00:00Z"
queued: "2026-03-20T14:04:51Z"
started: "2026-03-20T14:04:52Z"
completed: "2026-03-20T14:07:06Z"
branch: dark-factory/instance-locking
---

<summary>
- A new module provides OS-level exclusive file locking to prevent concurrent git-ai-sync instances
- Lock is acquired by opening a file and calling flock — not by checking file presence
- If the lock is already held, a clear error names the PID of the competing process
- The lock file is created with 0600 permissions in the repository root
- Writing the current PID into the lock file lets operators identify the owning process
- The kernel automatically releases the lock when the process exits or crashes — no stale lock problem
- The locking logic is isolated in its own module so it can be tested without invoking CLI commands
- Tests verify: successful acquisition, contention rejection (with correct PID in error), and that a stale lock file does not block a new acquisition
</summary>

<objective>
Create `src/git_ai_sync/instance_lock.py` — a standalone module that acquires and holds an exclusive OS-level flock on `.git-ai-sync.lock` in the repository root. The module must be independently testable and must not depend on the CLI layer.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff, pytest-asyncio).
Read `src/git_ai_sync/git_operations.py` for code style: module-level functions, full docstrings, type hints.
Read `tests/test_git_operations.py` for test style: pytest, fixtures, mocking with `unittest.mock`.
Read `src/git_ai_sync/__main__.py` for context on the three commands that will use the lock (watch, sync, resolve).
</context>

<requirements>
1. Create `src/git_ai_sync/instance_lock.py` with the following:

   a. `LOCK_FILE_NAME: str = ".git-ai-sync.lock"`

   b. `class LockError(Exception)` — raised when the lock is already held. Docstring: "Raised when another instance already holds the repository lock."

   c. Module-level private variable to keep the lock file descriptor alive:
      ```python
      _lock_file: IO[bytes] | None = None
      ```
      Import `IO` from `typing`. This must be module-level (not local) so the fd stays open until the process exits or `release_lock()` is called.

   d. `acquire_lock(repo_root: Path) -> None` — public function:
      - Docstring: "Acquire an exclusive non-blocking file lock on the repository lock file. Writes the current PID to the lock file. Raises LockError if another instance already holds the lock."
      - Opens (or creates) `repo_root / LOCK_FILE_NAME` in binary read/write mode (`"r+b"` if exists, `"w+b"` if not) using `open(..., mode)` with `os.O_CREAT` logic. Simplest approach: use `open(lock_path, "a+b")` which creates if missing and positions at end.
      - Sets file permissions to `0o600` using `os.chmod(lock_path, 0o600)` immediately after opening.
      - Tries `fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)`.
      - If `BlockingIOError` is raised (lock already held by another process):
        - Seek to start and read the file content: `f.seek(0); pid_str = f.read().decode("utf-8", errors="replace").strip()`
        - Raise `LockError(f"another instance is already running (pid {pid_str})")` if `pid_str` is non-empty, otherwise `LockError("another instance is already running")`.
      - On successful lock acquisition:
        - Truncate the file and write the current PID: `f.seek(0); f.truncate(); f.write(str(os.getpid()).encode()); f.flush()`
        - Store the open file object in `_lock_file` (the module-level variable) to keep the fd alive.

   e. `release_lock() -> None` — public function:
      - Docstring: "Release the repository lock if held. Called automatically on process exit; call explicitly only in tests."
      - Uses `global _lock_file`.
      - If `_lock_file` is not None: calls `fcntl.flock(_lock_file.fileno(), fcntl.LOCK_UN)`, then `_lock_file.close()`, then sets `_lock_file = None`.

   f. Imports needed: `fcntl`, `os`, `from pathlib import Path`, `from typing import IO`.

2. Create `tests/test_instance_lock.py` with the following tests (all in `class TestAcquireLock` / plain functions — follow the existing test file style):

   a. **`test_acquire_lock_succeeds`** — acquire on a fresh temp dir succeeds without raising, lock file is created, PID written matches `os.getpid()`:
      ```python
      def test_acquire_lock_succeeds(tmp_path):
          acquire_lock(tmp_path)
          lock_file = tmp_path / LOCK_FILE_NAME
          assert lock_file.exists()
          assert lock_file.read_text().strip() == str(os.getpid())
          release_lock()
      ```

   b. **`test_acquire_lock_permissions`** — lock file has mode 0600 after acquisition:
      ```python
      def test_acquire_lock_permissions(tmp_path):
          acquire_lock(tmp_path)
          lock_file = tmp_path / LOCK_FILE_NAME
          mode = oct(lock_file.stat().st_mode & 0o777)
          assert mode == oct(0o600)
          release_lock()
      ```

   c. **`test_acquire_lock_contention`** — when `fcntl.flock` raises `BlockingIOError`, `acquire_lock` raises `LockError` with the PID from the file:
      ```python
      def test_acquire_lock_contention(tmp_path, monkeypatch):
          lock_path = tmp_path / LOCK_FILE_NAME
          lock_path.write_text("12345")
          lock_path.chmod(0o600)
          import fcntl as fcntl_mod
          monkeypatch.setattr(fcntl_mod, "flock", lambda fd, op: (_ for _ in ()).throw(BlockingIOError()))
          with pytest.raises(LockError, match="12345"):
              acquire_lock(tmp_path)
      ```

   d. **`test_acquire_lock_contention_empty_file`** — when flock raises `BlockingIOError` and lock file is empty, error message contains "another instance is already running" without crashing:
      ```python
      def test_acquire_lock_contention_empty_file(tmp_path, monkeypatch):
          lock_path = tmp_path / LOCK_FILE_NAME
          lock_path.touch()
          import fcntl as fcntl_mod
          monkeypatch.setattr(fcntl_mod, "flock", lambda fd, op: (_ for _ in ()).throw(BlockingIOError()))
          with pytest.raises(LockError, match="another instance is already running"):
              acquire_lock(tmp_path)
      ```

   e. **`test_acquire_stale_lock_file`** — a lock file left on disk from a dead process (no flock held) does NOT block a new acquisition. This verifies kernel-level flock semantics: acquiring on a file that exists but has no flock succeeds:
      ```python
      def test_acquire_stale_lock_file(tmp_path):
          lock_path = tmp_path / LOCK_FILE_NAME
          lock_path.write_text("99999")  # stale PID
          acquire_lock(tmp_path)  # must not raise
          assert lock_path.read_text().strip() == str(os.getpid())
          release_lock()
      ```

   f. **`test_release_lock_clears_state`** — after `release_lock()`, the module-level `_lock_file` is None:
      ```python
      def test_release_lock_clears_state(tmp_path):
          import git_ai_sync.instance_lock as il
          acquire_lock(tmp_path)
          assert il._lock_file is not None
          release_lock()
          assert il._lock_file is None
      ```

   g. Use an `autouse` fixture to call `release_lock()` after each test to reset module state:
      ```python
      import pytest
      from git_ai_sync.instance_lock import LOCK_FILE_NAME, LockError, acquire_lock, release_lock

      @pytest.fixture(autouse=True)
      def cleanup_lock():
          yield
          release_lock()
      ```
</requirements>

<constraints>
- Use OS-level exclusive non-blocking flock (`fcntl.LOCK_EX | fcntl.LOCK_NB`) — NOT advisory file-presence checks, NOT lockfile packages
- Lock file location is always the git repo root passed as argument — not configurable
- Lock file permissions must be 0600
- The module-level `_lock_file` variable is intentional — it keeps the fd alive so the kernel flock persists until process exit
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Type hints required (mypy strict mode)
- Do NOT add linting suppressions unless absolutely necessary
</constraints>

<verification>
Run `make precommit` — must pass.
Confirm new tests exist: `uv run pytest tests/test_instance_lock.py -v`
Check coverage: `uv run pytest tests/test_instance_lock.py --cov=git_ai_sync.instance_lock --cov-report=term-missing`
</verification>
