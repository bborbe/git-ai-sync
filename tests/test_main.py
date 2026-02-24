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


def _sync_args(path: str = ".") -> argparse.Namespace:
    return argparse.Namespace(command="sync", path=path)


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
        ):
            cmd_sync(_sync_args())
            mock_git.push.assert_not_called()

    def test_full_sync(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
        ):
            cmd_sync(_sync_args())
            mock_git.stage_all.assert_called_once()
            mock_git.commit.assert_called_once()
            mock_git.pull_rebase.assert_called_once()
            mock_git.push.assert_called_once()

    def test_conflict_exits(self) -> None:
        mock_git = _mock_git_ops()
        mock_git.find_git_repo.return_value = Path("/repo")
        mock_git.get_current_branch.return_value = "master"
        mock_git.has_changes.return_value = True
        mock_git.generate_commit_message.return_value = "auto: 2026-01-01"
        mock_git.pull_rebase.side_effect = GitError("conflicts detected")
        with (
            patch(_GIT_OPS, mock_git),
            patch(_CONFIG),
            pytest.raises(SystemExit),
        ):
            cmd_sync(_sync_args())


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
