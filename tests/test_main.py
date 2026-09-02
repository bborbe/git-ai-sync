"""Tests for main entry point module."""

import argparse
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from git_ai_sync.__main__ import (
    cmd_doctor,
    cmd_status,
    cmd_sync,
    cmd_version,
    main,
    parse_args,
)
from git_ai_sync.git_operations import GitError


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


def _sync_args(path: str = ".") -> argparse.Namespace:
    return argparse.Namespace(command="sync", path=path, strategy="merge")


def _status_args(path: str = ".") -> argparse.Namespace:
    return argparse.Namespace(command="status", path=path)


def _mock_git_ops() -> MagicMock:
    """Create mock git_operations with real GitError."""
    mock = MagicMock()
    mock.GitError = GitError
    return mock


# Patch target for late-imported git_operations
_GIT_OPS = "git_ai_sync.git_operations"
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
        ):
            cmd_sync(args)
            mock_git.stage_all.assert_called_once()
            mock_git.commit.assert_called_once()
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
            mock_config.return_value.pushgateway_url = None
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
            mock_config.return_value.pushgateway_url = None
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
            mock_config.return_value.pushgateway_url = "https://pushgateway.test"
            with contextlib.suppress(KeyboardInterrupt):
                cmd_watch(args)
            mock_heartbeat.assert_called_once()
            assert mock_heartbeat.call_args.args[0] == "https://pushgateway.test"
            assert mock_heartbeat.call_args.args[-1] == Path("/repo")
            mock_last_success.assert_not_called()
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
            mock_config.return_value.pushgateway_url = "https://pushgateway.test"
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
        ):
            mock_config.return_value.pushgateway_url = "https://pushgateway.test"
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
        ):
            mock_config.return_value.pushgateway_url = "https://pushgateway.test"
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
            mock_config.return_value.pushgateway_url = None
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
