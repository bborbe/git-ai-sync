"""Tests for main entry point module."""

import argparse
import contextlib
import os
import re
import subprocess
import sys
import time
from collections.abc import Callable
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from git_ai_sync.__main__ import (
    cmd_doctor,
    cmd_status,
    cmd_sync,
    cmd_version,
    main,
    parse_args,
)
from git_ai_sync.git_operations import (
    GitError,
    MarkerRefusalError,
    UnsafePathRefusalError,
    sanitize_path,
)


class TestParseArgs:
    def test_watch_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "watch"]):
            args = parse_args()
            assert args.command == "watch"
            assert args.path == "."
            assert args.interval == 30

    def test_watch_with_path(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "watch", "/foo"]):
            args = parse_args()
            assert args.path == "/foo"

    def test_watch_with_interval(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "watch", "--interval", "60"]):
            args = parse_args()
            assert args.interval == 60

    def test_sync_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "sync"]):
            args = parse_args()
            assert args.command == "sync"
            assert args.path == "."

    def test_version_command(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "version"]):
            args = parse_args()
            assert args.command == "version"

    def test_no_command_exits(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync"]),
            pytest.raises(SystemExit),
        ):
            parse_args()

    def test_resolve_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "resolve"]):
            args = parse_args()
            assert args.command == "resolve"
            assert args.path == "."

    def test_status_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "status"]):
            args = parse_args()
            assert args.command == "status"

    def test_watch_strategy_default(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "watch"]):
            args = parse_args()
            assert args.strategy == "merge"

    def test_watch_strategy_rebase(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "watch", "--strategy", "rebase"]):
            args = parse_args()
            assert args.strategy == "rebase"

    def test_sync_strategy_default(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "sync"]):
            args = parse_args()
            assert args.strategy == "merge"

    def test_sync_strategy_rebase(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "sync", "--strategy", "rebase"]):
            args = parse_args()
            assert args.strategy == "rebase"

    def test_setup_launchd_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "setup-launchd", "/vault"]):
            args = parse_args()
            assert args.command == "setup-launchd"
            assert args.path == "/vault"

    def test_remove_launchd_defaults(self) -> None:
        with patch("sys.argv", ["git-ai-sync", "remove-launchd", "/vault"]):
            args = parse_args()
            assert args.command == "remove-launchd"
            assert args.path == "/vault"


def _sync_args(path: str = ".") -> argparse.Namespace:
    return argparse.Namespace(command="sync", path=path, strategy="merge")


def _status_args(path: str = ".") -> argparse.Namespace:
    return argparse.Namespace(command="status", path=path)


def _mock_git_ops() -> MagicMock:
    """Create mock git_operations with real GitError subclasses attached."""
    mock = MagicMock()
    mock.GitError = GitError
    mock.MarkerRefusalError = MarkerRefusalError
    mock.UnsafePathRefusalError = UnsafePathRefusalError
    return mock


def _configure_watch_config(
    mock_config: MagicMock,
    *,
    stall_threshold: int = 10,
    attention_script: str = "/nonexistent/attention-ask.py",
    attention_store_url: str = "http://localhost:18080",
    pushgateway_url: str | None = None,
) -> MagicMock:
    """Set every Config attribute cmd_watch reads on a patched Config mock."""
    mock_config.return_value.pushgateway_url = pushgateway_url
    mock_config.return_value.stall_threshold = stall_threshold
    mock_config.return_value.attention_script = attention_script
    mock_config.return_value.attention_store_url = attention_store_url
    return mock_config


# Patch target for late-imported git_operations
_GIT_OPS = "git_ai_sync.git_operations"


@pytest.fixture(autouse=True)
def _preimport_conflict_resolver() -> None:
    """Import conflict_resolver before any test patches git_operations.

    conflict_resolver binds ``git_operations`` at import time. If it were first
    imported while ``_GIT_OPS`` is patched, the mock would stay bound for the
    rest of the session and later real-repo tests would silently no-op.
    """
    import git_ai_sync.conflict_resolver  # noqa: F401  (import is the point)


_CONFIG = "git_ai_sync.config.Config"
_ACQUIRE_LOCK = "git_ai_sync.instance_lock.acquire_lock"


class TestCmdSync:
    def test_no_repo_exits(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = None
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            pytest.raises(SystemExit),
        ):
            cmd_sync(_sync_args())

    def test_no_changes_returns(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            patch(_ACQUIRE_LOCK),
        ):
            cmd_sync(_sync_args())
            mock_git.push.assert_not_called()

    def test_full_sync(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        args = argparse.Namespace(command="sync", path="/repo", strategy="rebase")
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            patch(_ACQUIRE_LOCK),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ) as mock_commit_recovery,
        ):
            cmd_sync(args)
            mock_git.stage_all.assert_called_once()
            mock_commit_recovery.assert_awaited_once()
            mock_git.pull_rebase.assert_called_once()
            mock_git.push.assert_called_once()

    def test_full_sync_merge_strategy(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        args = argparse.Namespace(command="sync", path="/repo", strategy="merge")
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            patch(_ACQUIRE_LOCK),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ),
        ):
            cmd_sync(args)
            mock_git.pull_merge.assert_called_once()
            mock_git.pull_rebase.assert_not_called()

    def test_full_sync_rebase_strategy(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        args = argparse.Namespace(command="sync", path="/repo", strategy="rebase")
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            patch(_ACQUIRE_LOCK),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ),
        ):
            cmd_sync(args)
            mock_git.pull_rebase.assert_called_once()
            mock_git.pull_merge.assert_not_called()

    def test_conflict_exits(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        mock_git.pull_rebase.side_effect = GitError("conflicts detected")
        args = argparse.Namespace(command="sync", path="/repo", strategy="rebase")
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            patch(_ACQUIRE_LOCK),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ),
            pytest.raises(SystemExit),
        ):
            cmd_sync(args)


class TestCmdStatus:
    def test_shows_clean(self, caplog: pytest.LogCaptureFixture) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_in_rebase.return_value = False
        with (
            patch(_GIT_OPS, mock_git),
            caplog.at_level("INFO"),
        ):
            cmd_status(_status_args())
            assert any("Clean" in r.message for r in caplog.records)

    def test_shows_changes(self, caplog: pytest.LogCaptureFixture) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.is_in_rebase.return_value = False
        with (
            patch(_GIT_OPS, mock_git),
            caplog.at_level("INFO"),
        ):
            cmd_status(_status_args())
            assert any("Uncommitted" in r.message for r in caplog.records)

    def test_rebase_warning(self, caplog: pytest.LogCaptureFixture) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_in_rebase.return_value = True
        with (
            patch(_GIT_OPS, mock_git),
            caplog.at_level("WARNING"),
        ):
            cmd_status(_status_args())
            assert any("rebase" in r.message.lower() for r in caplog.records)


class TestCmdVersion:
    def test_prints_version(self, capsys: pytest.CaptureFixture[str]) -> None:
        cmd_version()
        captured = capsys.readouterr()
        assert "git-ai-sync" in captured.out


class TestMainDispatch:
    def test_dispatches_sync(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "sync"]),
            patch("git_ai_sync.__main__.cmd_sync") as mock_cmd,
            patch("git_ai_sync.__main__.setup_signal_handlers"),
        ):
            main()
            mock_cmd.assert_called_once()

    def test_dispatches_version(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "version"]),
            patch("git_ai_sync.__main__.cmd_version") as mock_cmd,
            patch("git_ai_sync.__main__.setup_signal_handlers"),
        ):
            main()
            mock_cmd.assert_called_once()

    def test_dispatches_status(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "status"]),
            patch("git_ai_sync.__main__.cmd_status") as mock_cmd,
            patch("git_ai_sync.__main__.setup_signal_handlers"),
        ):
            main()
            mock_cmd.assert_called_once()

    def test_dispatches_setup_launchd(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "setup-launchd", "/vault"]),
            patch("git_ai_sync.__main__.cmd_setup_launchd") as mock_cmd,
            patch("git_ai_sync.__main__.setup_signal_handlers"),
        ):
            main()
            mock_cmd.assert_called_once()

    def test_dispatches_remove_launchd(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "remove-launchd", "/vault"]),
            patch("git_ai_sync.__main__.cmd_remove_launchd") as mock_cmd,
            patch("git_ai_sync.__main__.setup_signal_handlers"),
        ):
            main()
            mock_cmd.assert_called_once()

    def test_main_wires_log_file_into_configure_logging(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "version"]),
            patch("git_ai_sync.__main__.setup_signal_handlers"),
            patch("git_ai_sync.config.Config") as mock_config,
            patch("git_ai_sync.__main__.configure_logging") as mock_configure_logging,
        ):
            mock_config.return_value.log_file = "/tmp/x/git-ai-sync.log"
            main()
            mock_configure_logging.assert_called_once()
            assert mock_configure_logging.call_args.args[1] == Path("/tmp/x/git-ai-sync.log")

    def test_main_wires_none_when_log_file_unset(self) -> None:
        with (
            patch("sys.argv", ["git-ai-sync", "version"]),
            patch("git_ai_sync.__main__.setup_signal_handlers"),
            patch("git_ai_sync.config.Config") as mock_config,
            patch("git_ai_sync.__main__.configure_logging") as mock_configure_logging,
        ):
            mock_config.return_value.log_file = None
            main()
            mock_configure_logging.assert_called_once()
            assert mock_configure_logging.call_args.args[1] is None


class TestCmdLaunchd:
    def test_cmd_setup_launchd_calls_module(self) -> None:
        from git_ai_sync.__main__ import cmd_setup_launchd

        args = argparse.Namespace(command="setup-launchd", path="/vault")
        with patch("git_ai_sync.launchd.setup_launchd") as mock_setup:
            cmd_setup_launchd(args)
            mock_setup.assert_called_once_with(Path("/vault"))

    def test_cmd_setup_launchd_exits_on_launchd_error(self) -> None:
        from git_ai_sync.__main__ import cmd_setup_launchd
        from git_ai_sync.launchd import LaunchdError

        args = argparse.Namespace(command="setup-launchd", path="/vault")
        with (
            patch(
                "git_ai_sync.launchd.setup_launchd",
                side_effect=LaunchdError("dir validation failed"),
            ),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_setup_launchd(args)
        assert exc_info.value.code == 1

    def test_cmd_remove_launchd_calls_module(self) -> None:
        from git_ai_sync.__main__ import cmd_remove_launchd

        args = argparse.Namespace(command="remove-launchd", path="/vault")
        with patch("git_ai_sync.launchd.remove_launchd") as mock_remove:
            cmd_remove_launchd(args)
            mock_remove.assert_called_once_with(Path("/vault"))

    def test_cmd_remove_launchd_exits_on_launchd_error(self) -> None:
        from git_ai_sync.__main__ import cmd_remove_launchd
        from git_ai_sync.launchd import LaunchdError

        args = argparse.Namespace(command="remove-launchd", path="/vault")
        with (
            patch(
                "git_ai_sync.launchd.remove_launchd",
                side_effect=LaunchdError("plist delete failed"),
            ),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_remove_launchd(args)
        assert exc_info.value.code == 1


class TestLockAcquisition:
    def test_cmd_watch_exits_on_lock_error(self, tmp_path: Path) -> None:
        import argparse
        from unittest.mock import MagicMock, patch

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.instance_lock import LockError

        args = argparse.Namespace(path=str(tmp_path), interval=30)
        with (
            patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path),
            patch(
                "git_ai_sync.instance_lock.acquire_lock",
                side_effect=LockError("another instance is already running (pid 99999)"),
            ),
            patch("git_ai_sync.file_watcher.ChangeTracker", MagicMock()),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_watch(args)
        assert exc_info.value.code == 1

    def test_cmd_sync_exits_on_lock_error(self, tmp_path: Path) -> None:
        import argparse
        from unittest.mock import patch

        from git_ai_sync.__main__ import cmd_sync
        from git_ai_sync.instance_lock import LockError

        args = argparse.Namespace(path=str(tmp_path))
        with (
            patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path),
            patch("git_ai_sync.git_operations.get_current_branch", return_value="main"),
            patch(
                "git_ai_sync.instance_lock.acquire_lock",
                side_effect=LockError("another instance is already running (pid 99999)"),
            ),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_sync(args)
        assert exc_info.value.code == 1

    def test_cmd_resolve_exits_on_lock_error(self, tmp_path: Path) -> None:
        import argparse
        from unittest.mock import patch

        from git_ai_sync.__main__ import cmd_resolve
        from git_ai_sync.instance_lock import LockError

        args = argparse.Namespace(path=str(tmp_path))
        with (
            patch("git_ai_sync.git_operations.find_git_repo", return_value=tmp_path),
            patch(
                "git_ai_sync.instance_lock.acquire_lock",
                side_effect=LockError("another instance is already running (pid 99999)"),
            ),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_resolve(args)
        assert exc_info.value.code == 1


class TestCmdWatchDispatch:
    def test_cmd_watch_merge_strategy(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
        ):
            _configure_watch_config(mock_config, pushgateway_url=None)
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_git.pull_merge.assert_called_once()
            mock_git.pull_rebase.assert_not_called()
            mock_heartbeat.assert_not_called()
            mock_last_success.assert_not_called()

    def test_cmd_watch_rebase_strategy(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="rebase")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
        ):
            _configure_watch_config(mock_config, pushgateway_url=None)
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_git.pull_rebase.assert_called_once()
            mock_git.pull_merge.assert_not_called()
            mock_heartbeat.assert_not_called()
            mock_last_success.assert_not_called()

    def test_watch_heartbeat_one_per_cycle(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
        ):
            _configure_watch_config(mock_config, pushgateway_url="https://pushgateway.test")
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            assert mock_heartbeat.call_args.args[0] == "https://pushgateway.test"
            assert mock_heartbeat.call_args.args[-1] == Path("/repo")
            mock_last_success.assert_called_once()
            mock_git.push.assert_not_called()

    def test_watch_last_success_on_no_local_changes_cycle(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
        ):
            _configure_watch_config(mock_config, pushgateway_url="https://pushgateway.test")
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            mock_last_success.assert_called_once()
            assert mock_last_success.call_args.args[0] == "https://pushgateway.test"
            assert mock_last_success.call_args.args[-1] == Path("/repo")
            mock_git.push.assert_not_called()

    def test_watch_heartbeat_on_debounce_skip_cycle(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            # Files changed 5s ago (below 30s interval) -> debounce-skip the cycle
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=5),
            patch("time.sleep", side_effect=sleep_side_effect),
        ):
            _configure_watch_config(mock_config, pushgateway_url="https://pushgateway.test")
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            mock_last_success.assert_not_called()

    def test_watch_last_success_after_successful_sync(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ),
        ):
            _configure_watch_config(mock_config, pushgateway_url="https://pushgateway.test")
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            assert mock_heartbeat.call_args.args[0] == "https://pushgateway.test"
            mock_last_success.assert_called_once()
            assert mock_last_success.call_args.args[0] == "https://pushgateway.test"
            mock_git.push.assert_called_once()

    def test_watch_last_success_zero_after_failed_sync(self) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        mock_git.is_ahead_of_remote.return_value = False
        mock_git.push.side_effect = GitError("Failed to push: boom")
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
            patch(
                "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
                new_callable=AsyncMock,
            ),
        ):
            _configure_watch_config(mock_config, pushgateway_url="https://pushgateway.test")
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            mock_last_success.assert_not_called()

    def test_watch_startup_warning_when_unconfigured(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        import contextlib

        from git_ai_sync.__main__ import cmd_watch
        from git_ai_sync.file_watcher import ChangeTracker

        args = argparse.Namespace(path=".", interval=30, strategy="merge")
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = False
        mock_git.is_ahead_of_remote.return_value = False
        # First sleep returns None (loop body runs once), second raises to break loop
        sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG) as mock_config,
            patch(_ACQUIRE_LOCK),
            patch("git_ai_sync.metrics.push_heartbeat") as mock_heartbeat,
            patch("git_ai_sync.metrics.push_last_success") as mock_last_success,
            patch.object(ChangeTracker, "start"),
            patch.object(ChangeTracker, "stop"),
            patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
            patch("time.sleep", side_effect=sleep_side_effect),
            caplog.at_level("WARNING"),
        ):
            _configure_watch_config(mock_config, pushgateway_url=None)
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            assert any(
                r.message == "pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL"
                and r.levelname == "WARNING"
                for r in caplog.records
            )
            assert not any("metric push failed" in r.message for r in caplog.records)
            mock_heartbeat.assert_not_called()
            mock_last_success.assert_not_called()


def test_sync_marker_refusal_exits(caplog: pytest.LogCaptureFixture) -> None:
    """Sync exits non-zero with the still-flagged files and resolve pointer on final refusal."""
    mock_git = _mock_git_ops()
    mock_git.MarkerRefusalError = MarkerRefusalError
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    args = argparse.Namespace(command="sync", path="/repo", strategy="rebase")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG),
        patch(_ACQUIRE_LOCK),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=MarkerRefusalError(
                "Commit refused by pre-commit hook after 3 attempts; "
                "still-flagged files: conflict.md. "
                "Run 'git-ai-sync resolve /repo' to resolve"
            ),
        ),
        pytest.raises(SystemExit) as exc_info,
        caplog.at_level("ERROR"),
    ):
        cmd_sync(args)
    assert exc_info.value.code == 1
    assert any("conflict.md" in r.message for r in caplog.records)
    assert any("Run 'git-ai-sync resolve" in r.message for r in caplog.records)


def test_watch_marker_refusal_exits(caplog: pytest.LogCaptureFixture) -> None:
    """Watch exits non-zero (no silent loop) on a final marker refusal."""
    import argparse

    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.MarkerRefusalError = MarkerRefusalError
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    # First sleep returns None (loop body runs once); refusal exits before the second sleep
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch("git_ai_sync.metrics.push_heartbeat"),
        patch("git_ai_sync.metrics.push_last_success"),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=MarkerRefusalError(
                "Commit refused by pre-commit hook after 3 attempts; "
                "still-flagged files: conflict.md. "
                "Run 'git-ai-sync resolve /repo' to resolve"
            ),
        ),
        pytest.raises(SystemExit) as exc_info,
        caplog.at_level("ERROR"),
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        cmd_watch(args)
    assert exc_info.value.code == 1
    assert not any("Continuing to watch..." in r.message for r in caplog.records)
    assert any("conflict.md" in r.message for r in caplog.records)
    assert any("Run 'git-ai-sync resolve" in r.message for r in caplog.records)


def test_watch_marker_refusal_still_pulls() -> None:
    """A marker refusal exits non-zero, but only AFTER the pull has run.

    Regression guard: exiting before the pull let a failed commit suppress a remote
    operation it has nothing to do with — the bug this restructure removed.
    """
    import argparse

    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch("git_ai_sync.metrics.push_heartbeat"),
        patch("git_ai_sync.metrics.push_last_success"),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=MarkerRefusalError("refused"),
        ),
        pytest.raises(SystemExit) as exc_info,
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        cmd_watch(args)
    assert exc_info.value.code == 1
    mock_git.pull_merge.assert_called_once()


def test_watch_marker_refusal_exits_even_when_pull_fails() -> None:
    """A marker refusal still exits non-zero when the pull also fails.

    Regression guard: the pull block's `continue` handlers jumped past the marker
    exit, so a refusal plus a failing pull looped silently forever — re-breaking
    spec-002's "never loops silently forever" criterion. Reachable on this very
    path: the refusal leaves conflict-marker files staged, so `pull --rebase`
    fails with "cannot pull with rebase" (no "conflicts" substring).
    """
    import argparse

    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError(
        "Failed to pull: cannot pull with rebase: Your index contains uncommitted changes"
    )
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch("git_ai_sync.metrics.push_heartbeat"),
        patch("git_ai_sync.metrics.push_last_success"),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=MarkerRefusalError("refused"),
        ),
        pytest.raises(SystemExit) as exc_info,
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        cmd_watch(args)
    assert exc_info.value.code == 1


def test_watch_unexpected_error_still_pulls() -> None:
    """A non-GitError from the commit must not skip the pull.

    Reachable in production: claude_code_sdk raises bare Exception inside an anyio
    task group, surfacing as ExceptionGroup — not a GitError.
    """
    import argparse
    import contextlib

    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch("git_ai_sync.metrics.push_heartbeat"),
        patch("git_ai_sync.metrics.push_last_success"),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=RuntimeError("boom"),
        ),
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)
    mock_git.pull_merge.assert_called_once()


def test_sync_unexpected_error_still_pulls() -> None:
    """cmd_sync pulls even when the commit raises outside the GitError hierarchy."""
    import argparse

    from git_ai_sync.__main__ import cmd_sync

    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch("git_ai_sync.metrics.push_last_success"),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=RuntimeError("boom"),
        ),
    ):
        mock_config.return_value.pushgateway_url = None
        with pytest.raises(SystemExit) as exc_info:
            cmd_sync(argparse.Namespace(command="sync", path=".", strategy="merge"))
    assert exc_info.value.code == 1
    mock_git.pull_merge.assert_called_once()


BLOCKED_STDERR = (
    "\n"
    "✋ Blocked: staged path is not safe on Windows\n"
    "\n"
    '     unsafe?.md  —  contains one of  : ? * " < > |\n'
    "\n"
    '   Windows rejects these characters in a path component: : ? * " < > |\n'
    "   It also rejects a component ending in a dot or a space, and a name\n"
    "   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.\n"
)


def _build_repo_with_remote(tmp_path: Path) -> Path:
    """Create a repo whose origin is one commit ahead of the local checkout.

    Seeds a clean commit pushed to origin/master, advances origin by one commit
    the local checkout lacks, then stages an unsafe path locally.
    """
    remote = tmp_path / "remote.git"
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q", "--bare", str(remote)], check=True)
    subprocess.run(["git", "init", "-q"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=repo, check=True)
    subprocess.run(["git", "config", "commit.gpgsign", "false"], cwd=repo, check=True)
    subprocess.run(
        ["git", "config", "core.hooksPath", str(tmp_path / "no-hooks")],
        cwd=repo,
        check=True,
    )
    (repo / "clean.md").write_text("clean", encoding="utf-8")
    subprocess.run(["git", "add", "clean.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "seed"], cwd=repo, check=True)
    subprocess.run(["git", "branch", "-M", "master"], cwd=repo, check=True)
    subprocess.run(["git", "remote", "add", "origin", str(remote)], cwd=repo, check=True)
    subprocess.run(["git", "push", "-q", "-u", "origin", "master"], cwd=repo, check=True)

    # Advance origin by one commit the local checkout does not have.
    (repo / "remote.md").write_text("remote", encoding="utf-8")
    subprocess.run(["git", "add", "remote.md"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "remote"], cwd=repo, check=True)
    subprocess.run(["git", "push", "-q", "origin", "master"], cwd=repo, check=True)
    subprocess.run(["git", "reset", "-q", "--hard", "HEAD~1"], cwd=repo, check=True)

    # Stage an unsafe path locally.
    (repo / "unsafe?.md").write_text("content", encoding="utf-8")
    subprocess.run(["git", "add", "unsafe?.md"], cwd=repo, check=True)
    return repo


def _refusing_commit_run() -> Callable[..., subprocess.CompletedProcess[str]]:
    """Return a subprocess.run wrapper that refuses the first git commit."""
    real_run = subprocess.run
    refused: list[bool] = []

    def fake_run(*args: object, **kwargs: object) -> subprocess.CompletedProcess[str]:
        argv = args[0] if args else kwargs.get("args")
        if isinstance(argv, list) and argv[:2] == ["git", "commit"] and not refused:
            refused.append(True)
            return subprocess.CompletedProcess(
                args=argv, returncode=1, stdout="", stderr=BLOCKED_STDERR
            )
        return real_run(*args, **kwargs)  # type: ignore[arg-type]

    return fake_run


def _git_stdout(repo: Path, *argv: str) -> str:
    """Run a git command in ``repo`` and return its stdout."""
    return subprocess.run(
        ["git", *argv],
        cwd=repo,
        capture_output=True,
        text=True,
        check=True,
    ).stdout


def test_watch_pulls_when_commit_refused_unrecovered(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Watch pulls despite an unrecoverable refusal, then continues watching."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=UnsafePathRefusalError(
                "Commit refused by pre-commit hook after 3 attempts; "
                "still-offending paths: unsafe?.md. "
                "Rename them to Windows-safe names and commit again"
            ),
        ),
        caplog.at_level("INFO"),
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    mock_git.pull_merge.assert_called_once()
    mock_git.push.assert_called_once()
    assert not any(
        re.match(r"^Sync failed: Failed to commit:\s*$", r.getMessage(), re.MULTILINE)
        for r in caplog.records
    )
    assert any("unsafe?.md" in r.getMessage() for r in caplog.records)


def test_sync_pulls_when_commit_refused_recovered(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    """Sync recovers an unsafe path, still pulls, and exits 0."""
    repo = _build_repo_with_remote(tmp_path)
    expected = sanitize_path("unsafe?.md")
    args = argparse.Namespace(command="sync", path=str(repo), strategy="merge")
    with (
        patch("subprocess.run", side_effect=_refusing_commit_run()),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        caplog.at_level("INFO"),
    ):
        mock_config.return_value.pushgateway_url = None
        cmd_sync(args)

    tree = _git_stdout(repo, "ls-tree", "-r", "HEAD", "--name-only")
    assert expected in tree
    assert _git_stdout(repo, "rev-list", "--count", "HEAD..origin/master").strip() == "0"
    assert any("unsafe?.md" in r.getMessage() for r in caplog.records)


def test_watch_continues_after_refusal(caplog: pytest.LogCaptureFixture) -> None:
    """Watch runs a second cycle after a refusal instead of exiting or tight-looping."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.get_current_branch.return_value = "master"
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    sleep_side_effect: list[object] = [None, None, KeyboardInterrupt()]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=UnsafePathRefusalError(
                "Commit refused by pre-commit hook after 3 attempts; "
                "still-offending paths: unsafe?.md. "
                "Rename them to Windows-safe names and commit again"
            ),
        ),
        caplog.at_level("INFO"),
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert any("[2] Checking..." in r.getMessage() for r in caplog.records)
    assert mock_git.get_head_commit.call_count == 4
    assert mock_git.pull_merge.call_count == 2


def test_watch_pulls_when_commit_refused_recovered(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    """Watch recovers an unsafe path, lands a real commit, and pulls the remote commit."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    repo = _build_repo_with_remote(tmp_path)
    expected = sanitize_path("unsafe?.md")
    args = argparse.Namespace(path=str(repo), interval=30, strategy="merge")
    sleep_side_effect: list[object] = [None, KeyboardInterrupt()]
    with (
        patch("subprocess.run", side_effect=_refusing_commit_run()),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=sleep_side_effect),
        caplog.at_level("INFO"),
    ):
        _configure_watch_config(mock_config, pushgateway_url=None)
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    tree = _git_stdout(repo, "ls-tree", "-r", "HEAD", "--name-only")
    assert expected in tree
    assert _git_stdout(repo, "rev-list", "--count", "HEAD..origin/master").strip() == "0"
    assert any("unsafe?.md" in r.getMessage() for r in caplog.records)


def _argv_flag(argv: list[str], flag: str) -> str:
    """Return the value following ``flag`` in a recorded argv list."""
    return argv[argv.index(flag) + 1]


def _watch_sleep(cycles: int) -> Callable[[float], None]:
    """A ``time.sleep`` stand-in that lets ``cycles`` watch iterations run.

    The notifier spawns a real subprocess, whose internal wait loop also calls
    ``time.sleep`` with sub-second delays; those must pass through, so only the
    watch loop's own sleeps (the interval, >= 1s) are counted. The call after
    the last counted one raises ``KeyboardInterrupt`` to end the loop.
    """
    seen: list[float] = []

    def fake_sleep(seconds: float) -> None:
        if seconds < 1:
            return
        seen.append(seconds)
        if len(seen) > cycles:
            raise KeyboardInterrupt

    return fake_sleep


def test_watch_posts_card_at_threshold(stub_notifier, recording_store) -> None:
    """Two consecutive failed cycles at threshold 2 post exactly one card."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(2)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    invocations = stub.invocations()
    assert len(invocations) == 1
    payload = _argv_flag(invocations[0], "--payload")
    assert "repo" in payload
    assert "2" in payload
    assert mock_git.pull_merge.call_count == 2
    assert recording_store.requests == []


def test_watch_posts_card_at_threshold_three(stub_notifier, recording_store) -> None:
    """At threshold 3, the card carries the count 3 after three failed cycles."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(3)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=3,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    invocations = stub.invocations()
    assert len(invocations) == 1
    assert "3" in _argv_flag(invocations[0], "--payload")


def test_watch_silent_below_threshold(stub_notifier, recording_store) -> None:
    """One isolated failed cycle stays silent."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(1)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert stub.invocations() == []
    assert recording_store.requests == []


def test_watch_silent_on_isolated_failure(stub_notifier, recording_store) -> None:
    """A failure followed by a success never posts — nothing to close either."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = [GitError("Sync failed: remote unreachable"), None]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(2)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert stub.invocations() == []
    assert recording_store.requests == []


def test_watch_resets_on_success(stub_notifier, recording_store) -> None:
    """A success between two failures resets the count, so nothing is posted."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = [
        GitError("Sync failed: remote unreachable"),
        None,
        GitError("Sync failed: remote unreachable"),
    ]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(3)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert stub.invocations() == []
    assert recording_store.requests == []


def test_watch_skip_is_neutral(
    stub_notifier, recording_store, caplog: pytest.LogCaptureFixture
) -> None:
    """A debounce skip neither resets the counter nor counts as a failure."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", side_effect=[999, 5, 999]),
        patch("time.sleep", side_effect=_watch_sleep(3)),
        caplog.at_level("INFO"),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    invocations = stub.invocations()
    assert len(invocations) == 1
    assert "2" in _argv_flag(invocations[0], "--payload")
    assert any("[3] Checking..." in r.getMessage() for r in caplog.records)
    assert mock_git.pull_merge.call_count == 2
    assert recording_store.requests == []


def test_watch_one_card_until_reset(stub_notifier, recording_store) -> None:
    """Four failed cycles post exactly one card — never two while it is outstanding."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(4)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert len(stub.invocations()) == 1
    assert recording_store.requests == []


def test_watch_closes_card_on_recovery(stub_notifier, recording_store) -> None:
    """The first successful cycle after a stall closes the outstanding card."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    item_id = "0" * 32
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = [
        GitError("Sync failed: remote unreachable"),
        GitError("Sync failed: remote unreachable"),
        None,
    ]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(3)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert len(stub.invocations()) == 1
    assert recording_store.requests == [("POST", f"/api/1.0/attention/{item_id}/close")]


def test_watch_second_stall_posts_fresh_card(stub_notifier, recording_store) -> None:
    """A later stall posts a fresh card with the same dedup key."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    failure = GitError("Sync failed: remote unreachable")
    mock_git.pull_merge.side_effect = [failure, failure, None, failure, failure]
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(5)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    invocations = stub.invocations()
    assert len(invocations) == 2
    assert _argv_flag(invocations[0], "--dedup-key") == "git-ai-sync-repo-stalled"
    assert _argv_flag(invocations[1], "--dedup-key") == "git-ai-sync-repo-stalled"
    assert len(recording_store.requests) == 1


def test_watch_notifier_disabled_when_script_absent(
    stub_notifier, recording_store, caplog: pytest.LogCaptureFixture
) -> None:
    """An absent script warns once at startup and performs zero per-cycle work."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    path = "/nonexistent/attention-ask.py"
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(3)),
        caplog.at_level("WARNING"),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=path,
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert (
        sum(1 for r in caplog.records if r.levelname == "WARNING" and path in r.getMessage()) == 1
    )
    assert not any("stall card" in r.getMessage() for r in caplog.records)
    assert recording_store.requests == []


def test_watch_exits_when_threshold_below_two(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    """watch refuses to start with a threshold below 2, naming the setting."""
    from git_ai_sync.__main__ import cmd_watch

    monkeypatch.setenv("GIT_AI_SYNC_STALL_THRESHOLD", "1")
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    with caplog.at_level("ERROR"), pytest.raises(SystemExit) as exc_info:
        cmd_watch(args)
    assert exc_info.value.code == 1
    assert any("GIT_AI_SYNC_STALL_THRESHOLD" in r.getMessage() for r in caplog.records)
    assert not any("[1] Checking..." in r.getMessage() for r in caplog.records)


def test_status_ignores_threshold_below_two(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """status is unaffected by a threshold value it never reads."""
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=repo, check=True)
    monkeypatch.setenv("GIT_AI_SYNC_STALL_THRESHOLD", "1")
    cmd_status(argparse.Namespace(command="status", path=str(repo)))


def test_watch_exit_path_posts_no_card(stub_notifier, recording_store) -> None:
    """A cycle that ends the process never touches the counter, so no card is posted."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = True
    mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
    mock_git.is_ahead_of_remote.return_value = False
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(1)),
        patch(
            "git_ai_sync.conflict_resolver.commit_with_marker_recovery",
            new_callable=AsyncMock,
            side_effect=MarkerRefusalError("refused"),
        ),
        pytest.raises(SystemExit),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        cmd_watch(args)

    assert stub.invocations() == []
    assert recording_store.requests == []


def test_watch_non_giterror_pull_is_a_failure(stub_notifier, recording_store) -> None:
    """A non-GitError phase failure is still exactly one cycle failure."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier()
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = RuntimeError("boom")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(2)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    invocations = stub.invocations()
    assert len(invocations) == 1
    assert "2" in _argv_flag(invocations[0], "--payload")


def test_watch_failed_post_retries_each_cycle(stub_notifier, recording_store) -> None:
    """A post that fails never sets the guard, so the next failing cycle retries."""
    from git_ai_sync.__main__ import cmd_watch
    from git_ai_sync.file_watcher import ChangeTracker

    stub = stub_notifier(exit_code=1)
    args = argparse.Namespace(path=".", interval=30, strategy="merge")
    mock_git = _mock_git_ops()
    mock_git.find_git_repo.return_value = Path("/repo")
    mock_git.has_changes.return_value = False
    mock_git.is_ahead_of_remote.return_value = False
    mock_git.pull_merge.side_effect = GitError("Sync failed: remote unreachable")
    with (
        patch(_GIT_OPS, mock_git),
        patch(_CONFIG) as mock_config,
        patch(_ACQUIRE_LOCK),
        patch.object(ChangeTracker, "start"),
        patch.object(ChangeTracker, "stop"),
        patch.object(ChangeTracker, "get_seconds_since_last_change", return_value=999),
        patch("time.sleep", side_effect=_watch_sleep(4)),
    ):
        _configure_watch_config(
            mock_config,
            stall_threshold=2,
            attention_script=str(stub.script),
            attention_store_url=recording_store.url,
        )
        with contextlib.suppress(KeyboardInterrupt):
            cmd_watch(args)

    assert len(stub.invocations()) == 3
    assert recording_store.requests == []


def test_watch_stall_alert_integration(stub_notifier, recording_store, tmp_path: Path) -> None:
    """A real watch process posts one card after the second failed cycle and keeps looping."""
    remote = tmp_path / "remote.git"
    vault = tmp_path / "vault"
    subprocess.run(["git", "init", "-q", "--bare", str(remote)], check=True)
    subprocess.run(["git", "clone", "-q", str(remote), str(vault)], check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=vault, check=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=vault, check=True)
    subprocess.run(["git", "config", "commit.gpgsign", "false"], cwd=vault, check=True)
    (vault / "seed.md").write_text("seed", encoding="utf-8")
    subprocess.run(["git", "add", "seed.md"], cwd=vault, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "seed"], cwd=vault, check=True)
    subprocess.run(["git", "push", "-q", "-u", "origin", "HEAD"], cwd=vault, check=True)
    (vault / ".obsidian").mkdir()

    # Deleting the bare remote makes the clone's origin an unreachable local remote.
    subprocess.run(["rm", "-rf", str(remote)], check=True)

    stub = stub_notifier()
    env = {
        **os.environ,
        "GIT_AI_SYNC_STALL_THRESHOLD": "2",
        "GIT_AI_SYNC_ATTENTION_SCRIPT": str(stub.script),
        "ATTENTION_STORE_URL": recording_store.url,
    }
    env.pop("GIT_AI_SYNC_PUSHGATEWAY_URL", None)

    proc = subprocess.Popen(
        [sys.executable, "-m", "git_ai_sync", "watch", "--interval", "1", str(vault)],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    try:
        deadline = time.monotonic() + 15.0
        while time.monotonic() < deadline and not stub.invocations():
            time.sleep(0.25)
        time.sleep(2.0)
        assert proc.poll() is None
        proc.terminate()
        output, _ = proc.communicate(timeout=10)
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.communicate(timeout=10)

    assert len(stub.invocations()) == 1
    assert output.count("Sync failed") >= 3


class TestCmdDoctor:
    def test_all_checks_pass(self, caplog: pytest.LogCaptureFixture) -> None:
        """Test doctor command when all checks pass."""
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/fake/repo")

        with (
            patch("shutil.which", lambda cmd: f"/usr/bin/{cmd}"),
            patch("os.access", return_value=True),
            patch(_GIT_OPS, mock_git),
            patch("asyncio.run", return_value=True),
            caplog.at_level("INFO"),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_doctor()

        assert exc_info.value.code == 0
        assert any("All checks passed" in r.message for r in caplog.records)

    def test_cli_not_found(self, caplog: pytest.LogCaptureFixture) -> None:
        """Test doctor command when Claude CLI is not found."""
        mock_git = _mock_git_ops()

        def mock_which(cmd: str) -> str | None:
            return "/usr/bin/node" if cmd == "node" else None

        with (
            patch("shutil.which", mock_which),
            patch("pathlib.Path.exists", return_value=False),
            patch(_GIT_OPS, mock_git),
            caplog.at_level("ERROR"),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_doctor()

        assert exc_info.value.code == 1
        assert any("Claude Code CLI not found" in r.message for r in caplog.records)

    def test_node_not_found(self, caplog: pytest.LogCaptureFixture) -> None:
        """Test doctor command when Node.js is not found."""
        mock_git = _mock_git_ops()

        def mock_which(cmd: str) -> str | None:
            return "/usr/bin/claude" if cmd == "claude" else None

        with (
            patch("shutil.which", mock_which),
            patch("os.access", return_value=True),
            patch(_GIT_OPS, mock_git),
            caplog.at_level("ERROR"),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_doctor()

        assert exc_info.value.code == 1
        assert any("Node.js not found" in r.message for r in caplog.records)

    def test_not_in_git_repo(self, caplog: pytest.LogCaptureFixture) -> None:
        """Test doctor command when not in a git repository."""
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = None

        with (
            patch("shutil.which", lambda cmd: f"/usr/bin/{cmd}"),
            patch("os.access", return_value=True),
            patch(_GIT_OPS, mock_git),
            patch("asyncio.run", return_value=True),
            caplog.at_level("INFO"),
            pytest.raises(SystemExit) as exc_info,
        ):
            cmd_doctor()

        # Git repo check is informational only, so all 4 required checks pass
        assert exc_info.value.code == 0
        assert any("not a git repository" in r.message for r in caplog.records)
        assert any("All checks passed" in r.message for r in caplog.records)
