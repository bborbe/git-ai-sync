"""Tests for instance_lock module."""

import os

import pytest

from git_ai_sync.instance_lock import LOCK_FILE_NAME, LockError, acquire_lock, release_lock


@pytest.fixture(autouse=True)
def cleanup_lock() -> None:  # type: ignore[return]
    yield
    release_lock()


def test_acquire_lock_succeeds(tmp_path: pytest.TempPathFactory) -> None:
    acquire_lock(tmp_path)  # type: ignore[arg-type]
    lock_file = tmp_path / LOCK_FILE_NAME  # type: ignore[operator]
    assert lock_file.exists()
    assert lock_file.read_text().strip() == str(os.getpid())
    release_lock()


def test_acquire_lock_permissions(tmp_path: pytest.TempPathFactory) -> None:
    acquire_lock(tmp_path)  # type: ignore[arg-type]
    lock_file = tmp_path / LOCK_FILE_NAME  # type: ignore[operator]
    mode = oct(lock_file.stat().st_mode & 0o777)
    assert mode == oct(0o600)
    release_lock()


def test_acquire_lock_contention(
    tmp_path: pytest.TempPathFactory, monkeypatch: pytest.MonkeyPatch
) -> None:
    lock_path = tmp_path / LOCK_FILE_NAME  # type: ignore[operator]
    lock_path.write_text("12345")
    lock_path.chmod(0o600)
    import fcntl as fcntl_mod

    monkeypatch.setattr(fcntl_mod, "flock", lambda fd, op: (_ for _ in ()).throw(BlockingIOError()))
    with pytest.raises(LockError, match="12345"):
        acquire_lock(tmp_path)  # type: ignore[arg-type]


def test_acquire_lock_contention_empty_file(
    tmp_path: pytest.TempPathFactory, monkeypatch: pytest.MonkeyPatch
) -> None:
    lock_path = tmp_path / LOCK_FILE_NAME  # type: ignore[operator]
    lock_path.touch()
    import fcntl as fcntl_mod

    monkeypatch.setattr(fcntl_mod, "flock", lambda fd, op: (_ for _ in ()).throw(BlockingIOError()))
    with pytest.raises(LockError, match="another instance is already running"):
        acquire_lock(tmp_path)  # type: ignore[arg-type]


def test_acquire_stale_lock_file(tmp_path: pytest.TempPathFactory) -> None:
    lock_path = tmp_path / LOCK_FILE_NAME  # type: ignore[operator]
    lock_path.write_text("99999")  # stale PID
    acquire_lock(tmp_path)  # type: ignore[arg-type]  # must not raise
    assert lock_path.read_text().strip() == str(os.getpid())
    release_lock()


def test_release_lock_clears_state(tmp_path: pytest.TempPathFactory) -> None:
    import git_ai_sync.instance_lock as il

    acquire_lock(tmp_path)  # type: ignore[arg-type]
    assert il._lock_file is not None
    release_lock()
    assert il._lock_file is None
