"""Instance locking to prevent concurrent git-ai-sync processes."""

import fcntl
import os
from pathlib import Path
from typing import IO

LOCK_FILE_NAME: str = ".git-ai-sync.lock"

_lock_file: IO[bytes] | None = None


class LockError(Exception):
    """Raised when another instance already holds the repository lock."""

    pass


def acquire_lock(repo_root: Path) -> None:
    """Acquire an exclusive non-blocking file lock on the repository lock file.

    Writes the current PID to the lock file. Raises LockError if another
    instance already holds the lock.

    Args:
        repo_root: Path to the git repository root where the lock file is created.

    Raises:
        LockError: If another instance already holds the lock.
    """
    global _lock_file

    lock_path = repo_root / LOCK_FILE_NAME
    f: IO[bytes] = open(lock_path, "a+b")  # noqa: SIM115
    os.chmod(lock_path, 0o600)

    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        f.seek(0)
        pid_str = f.read().decode("utf-8", errors="replace").strip()
        f.close()
        if pid_str:
            raise LockError(f"another instance is already running (pid {pid_str})") from None
        raise LockError("another instance is already running") from None

    f.seek(0)
    f.truncate()
    f.write(str(os.getpid()).encode())
    f.flush()
    _lock_file = f


def release_lock() -> None:
    """Release the repository lock if held.

    Called automatically on process exit; call explicitly only in tests.
    """
    global _lock_file

    if _lock_file is not None:
        fcntl.flock(_lock_file.fileno(), fcntl.LOCK_UN)
        _lock_file.close()
        _lock_file = None
