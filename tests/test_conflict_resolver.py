"""Tests for conflict resolver module."""

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from claude_code_sdk import ClaudeSDKError

from git_ai_sync.conflict_resolver import (
    ConflictError,
    _parse_message_tolerant,
    commit_with_marker_recovery,
    do_continue_rebase,
    parse_conflict_markers,
    resolve_all_conflicts,
    resolve_conflict_with_claude,
    resolve_marker_flagged_files,
)
from git_ai_sync.git_operations import GitError, MarkerRefusalError

CONFLICT_CONTENT = """\
some text before
<<<<<<< HEAD
our line
=======
their line
>>>>>>> branch
some text after
"""

MULTI_CONFLICT = """\
<<<<<<< HEAD
ours1
=======
theirs1
>>>>>>> branch
middle
<<<<<<< HEAD
ours2
=======
theirs2
>>>>>>> branch
"""


class TestParseConflictMarkers:
    def test_no_conflicts(self) -> None:
        assert parse_conflict_markers("clean content") == []

    def test_single_conflict(self) -> None:
        result = parse_conflict_markers(CONFLICT_CONTENT)
        assert len(result) == 1
        assert result[0]["ours"] == "our line"
        assert result[0]["theirs"] == "their line"
        assert "full_match" in result[0]

    def test_multiple_conflicts(self) -> None:
        result = parse_conflict_markers(MULTI_CONFLICT)
        assert len(result) == 2
        assert result[0]["ours"] == "ours1"
        assert result[1]["theirs"] == "theirs2"

    def test_multiline_conflict(self) -> None:
        content = """\
<<<<<<< HEAD
line1
line2
=======
line3
line4
>>>>>>> branch
"""
        result = parse_conflict_markers(content)
        assert len(result) == 1
        assert result[0]["ours"] == "line1\nline2"
        assert result[0]["theirs"] == "line3\nline4"


class _AsyncIter:
    """Async iterator wrapper for testing."""

    def __init__(self, items: list[object]) -> None:
        self._items = items
        self._index = 0

    def __aiter__(self) -> _AsyncIter:
        return self

    async def __anext__(self) -> object:
        if self._index >= len(self._items):
            raise StopAsyncIteration
        item = self._items[self._index]
        self._index += 1
        return item


def _mock_claude_client(response_text: str) -> MagicMock:
    """Create a mock ClaudeSDKClient that returns given text."""
    from claude_code_sdk import AssistantMessage, TextBlock

    mock_text_block = MagicMock(spec=TextBlock)
    mock_text_block.text = response_text
    mock_text_block.__class__ = TextBlock

    mock_message = MagicMock(spec=AssistantMessage)
    mock_message.content = [mock_text_block]
    mock_message.__class__ = AssistantMessage

    mock_client = AsyncMock()
    mock_client.query = AsyncMock()
    mock_client.receive_response = MagicMock(return_value=_AsyncIter([mock_message]))

    mock_client_cls = MagicMock()
    mock_client_cls.return_value.__aenter__ = AsyncMock(return_value=mock_client)
    mock_client_cls.return_value.__aexit__ = AsyncMock(return_value=False)

    return mock_client_cls


def _mock_claude_client_skipping_first(skipped: object, response_text: str) -> MagicMock:
    """Create a mock ClaudeSDKClient that yields a skipped message then the response."""
    from claude_code_sdk import AssistantMessage, TextBlock

    mock_text_block = MagicMock(spec=TextBlock)
    mock_text_block.text = response_text
    mock_text_block.__class__ = TextBlock

    mock_message = MagicMock(spec=AssistantMessage)
    mock_message.content = [mock_text_block]
    mock_message.__class__ = AssistantMessage

    mock_client = AsyncMock()
    mock_client.query = AsyncMock()
    mock_client.receive_response = MagicMock(return_value=_AsyncIter([skipped, mock_message]))

    mock_client_cls = MagicMock()
    mock_client_cls.return_value.__aenter__ = AsyncMock(return_value=mock_client)
    mock_client_cls.return_value.__aexit__ = AsyncMock(return_value=False)

    return mock_client_cls


def _rate_limit_event_mock() -> MagicMock:
    """Create a mock non-assistant message shaped like the CLI's rate_limit_event."""
    event = MagicMock()
    event.type = "rate_limit_event"
    event.rate_limit_info = {"status": "allowed", "isUsingOverage": False}
    return event


class TestParseMessageTolerance:
    def test_parse_message_tolerates_rate_limit_event(self) -> None:
        payload = {
            "type": "rate_limit_event",
            "uuid": "test-uuid",
            "rate_limit_info": {"status": "allowed", "isUsingOverage": False},
        }
        assert _parse_message_tolerant(payload) is None

    def test_wrapper_installed_on_sdk_module(self) -> None:
        from claude_code_sdk._internal import message_parser as _message_parser

        assert _message_parser.parse_message is _parse_message_tolerant


class TestResolveConflictWithClaude:
    async def test_no_conflicts_returns_content(self) -> None:
        result = await resolve_conflict_with_claude("file.md", "clean content")
        assert result == "clean content"

    async def test_returns_resolved_content(self) -> None:
        mock_cls = _mock_claude_client("resolved content")
        with patch("git_ai_sync.conflict_resolver.ClaudeSDKClient", mock_cls):
            result = await resolve_conflict_with_claude("file.md", CONFLICT_CONTENT)
            assert result == "resolved content"

    async def test_resolve_conflict_with_claude_skips_rate_limit_event(self) -> None:
        mock_cls = _mock_claude_client_skipping_first(_rate_limit_event_mock(), "resolved content")
        with patch("git_ai_sync.conflict_resolver.ClaudeSDKClient", mock_cls):
            result = await resolve_conflict_with_claude("file.md", CONFLICT_CONTENT)
            assert result == "resolved content"

    async def test_strips_code_fences(self) -> None:
        mock_cls = _mock_claude_client("```markdown\nresolved\n```")
        with patch("git_ai_sync.conflict_resolver.ClaudeSDKClient", mock_cls):
            result = await resolve_conflict_with_claude("file.md", CONFLICT_CONTENT)
            assert result == "resolved"

    async def test_raises_on_empty_response(self) -> None:
        mock_cls = _mock_claude_client("")
        with (
            patch("git_ai_sync.conflict_resolver.ClaudeSDKClient", mock_cls),
            pytest.raises(ConflictError, match="empty response"),
        ):
            await resolve_conflict_with_claude("file.md", CONFLICT_CONTENT)

    async def test_raises_on_sdk_error(self) -> None:
        mock_client_cls = MagicMock()
        mock_client = AsyncMock()
        mock_client_cls.return_value.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client_cls.return_value.__aexit__ = AsyncMock(return_value=False)
        mock_client.query = AsyncMock(side_effect=ClaudeSDKError("api error"))

        with (
            patch("git_ai_sync.conflict_resolver.ClaudeSDKClient", mock_client_cls),
            pytest.raises(ConflictError, match="Claude API call failed"),
        ):
            await resolve_conflict_with_claude("file.md", CONFLICT_CONTENT)


class TestResolveAllConflicts:
    def _patch_git_ops(self) -> MagicMock:
        """Create a mock git_operations with real GitError."""
        mock = MagicMock()
        mock.GitError = GitError
        return mock

    async def test_no_conflicted_files(self, temp_dir: Path) -> None:
        mock_git = self._patch_git_ops()
        mock_git.get_conflicted_files.return_value = []
        with patch("git_ai_sync.conflict_resolver.git_operations", mock_git):
            count, failed = await resolve_all_conflicts(temp_dir)
            assert count == 0
            assert failed == []

    async def test_resolves_file(self, temp_dir: Path) -> None:
        conflict_file = temp_dir / "file.md"
        conflict_file.write_text(CONFLICT_CONTENT)

        mock_git = self._patch_git_ops()
        mock_git.get_conflicted_files.return_value = ["file.md"]
        with (
            patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
            patch(
                "git_ai_sync.conflict_resolver.resolve_conflict_with_claude",
                new_callable=AsyncMock,
                return_value="resolved",
            ),
        ):
            count, failed = await resolve_all_conflicts(temp_dir)
            assert count == 1
            assert failed == []
            mock_git.stage_file.assert_called_once_with(temp_dir, "file.md")

    async def test_records_failed_files(self, temp_dir: Path) -> None:
        conflict_file = temp_dir / "file.md"
        conflict_file.write_text(CONFLICT_CONTENT)

        mock_git = self._patch_git_ops()
        mock_git.get_conflicted_files.return_value = ["file.md"]
        with (
            patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
            patch(
                "git_ai_sync.conflict_resolver.resolve_conflict_with_claude",
                new_callable=AsyncMock,
                side_effect=ConflictError("failed"),
            ),
        ):
            count, failed = await resolve_all_conflicts(temp_dir)
            assert count == 0
            assert failed == ["file.md"]


class TestDoContinueRebase:
    def _patch_git_ops(self) -> MagicMock:
        """Create a mock git_operations with real GitError."""
        mock = MagicMock()
        mock.GitError = GitError
        return mock

    def test_calls_continue_rebase(self, temp_dir: Path) -> None:
        mock_git = self._patch_git_ops()
        with patch("git_ai_sync.conflict_resolver.git_operations", mock_git):
            do_continue_rebase(temp_dir)
            mock_git.continue_rebase.assert_called_once_with(temp_dir)

    def test_raises_with_remaining_conflicts(self, temp_dir: Path) -> None:
        mock_git = self._patch_git_ops()
        mock_git.continue_rebase.side_effect = GitError("failed")
        mock_git.get_conflicted_files.return_value = ["file.md"]
        with (
            patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
            pytest.raises(ConflictError, match="still have conflicts"),
        ):
            do_continue_rebase(temp_dir)

    def test_raises_generic_on_no_conflicts(self, temp_dir: Path) -> None:
        mock_git = self._patch_git_ops()
        mock_git.continue_rebase.side_effect = GitError("other error")
        mock_git.get_conflicted_files.return_value = []
        with (
            patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
            pytest.raises(ConflictError, match="Failed to continue"),
        ):
            do_continue_rebase(temp_dir)


def _marker_git_ops() -> MagicMock:
    """Create a mock git_operations with real GitError and MarkerRefusalError."""
    mock = MagicMock()
    mock.GitError = GitError
    mock.MarkerRefusalError = MarkerRefusalError
    return mock


async def test_resolve_marker_flagged_files(temp_dir: Path) -> None:
    conflict_file = temp_dir / "file.md"
    conflict_file.write_text(CONFLICT_CONTENT)

    mock_git = _marker_git_ops()
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_conflict_with_claude",
            new_callable=AsyncMock,
            return_value="resolved",
        ),
    ):
        await resolve_marker_flagged_files(temp_dir, ["file.md"])
        assert conflict_file.read_text(encoding="utf-8") == "resolved"
        mock_git.stage_file.assert_called_once_with(temp_dir, "file.md")


async def test_resolve_marker_flagged_files_multiple(temp_dir: Path) -> None:
    conflict_a = temp_dir / "a.md"
    conflict_a.write_text(CONFLICT_CONTENT)
    conflict_b = temp_dir / "b.md"
    conflict_b.write_text(CONFLICT_CONTENT)

    mock_git = _marker_git_ops()
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_conflict_with_claude",
            new_callable=AsyncMock,
            side_effect=["resolved-a", "resolved-b"],
        ),
    ):
        await resolve_marker_flagged_files(temp_dir, ["a.md", "b.md"])
        assert conflict_a.read_text(encoding="utf-8") == "resolved-a"
        assert conflict_b.read_text(encoding="utf-8") == "resolved-b"
        assert mock_git.stage_file.call_count == 2


async def test_resolve_marker_flagged_files_stops_on_failure(temp_dir: Path) -> None:
    conflict_file = temp_dir / "file.md"
    conflict_file.write_text(CONFLICT_CONTENT)

    mock_git = _marker_git_ops()
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_conflict_with_claude",
            new_callable=AsyncMock,
            side_effect=ConflictError("Claude API call failed"),
        ),
        pytest.raises(ConflictError, match=r"file\.md"),
    ):
        await resolve_marker_flagged_files(temp_dir, ["file.md"])

    mock_git.stage_file.assert_not_called()


async def test_commit_with_marker_recovery_success(temp_dir: Path) -> None:
    mock_git = _marker_git_ops()
    mock_git.commit.side_effect = [MarkerRefusalError("refused"), None]
    mock_git.get_marker_flagged_files.return_value = ["file.md"]
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_marker_flagged_files",
            new_callable=AsyncMock,
        ) as mock_resolver,
    ):
        await commit_with_marker_recovery(temp_dir, "msg")
        assert mock_git.commit.call_count == 2
        mock_resolver.assert_awaited_once()
        args = mock_resolver.await_args.args
        assert args[0] == temp_dir
        assert args[1] == ["file.md"]


async def test_commit_with_marker_recovery_bounded(temp_dir: Path) -> None:
    mock_git = _marker_git_ops()
    mock_git.commit.side_effect = MarkerRefusalError("refused")
    mock_git.get_marker_flagged_files.return_value = ["file.md"]
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_marker_flagged_files",
            new_callable=AsyncMock,
        ),
        pytest.raises(MarkerRefusalError) as exc_info,
    ):
        await commit_with_marker_recovery(temp_dir, "msg")

    assert mock_git.commit.call_count == 3
    assert "Run 'git-ai-sync resolve" in str(exc_info.value)


async def test_commit_with_marker_recovery_early_stop(temp_dir: Path) -> None:
    mock_git = _marker_git_ops()
    mock_git.commit.side_effect = MarkerRefusalError("refused")
    mock_git.get_marker_flagged_files.return_value = ["file.md"]
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_marker_flagged_files",
            new_callable=AsyncMock,
            side_effect=ConflictError(
                "Failed to resolve marker-flagged file file.md: Claude API call failed"
            ),
        ),
        pytest.raises(MarkerRefusalError, match=r"file\.md"),
    ):
        await commit_with_marker_recovery(temp_dir, "msg")

    assert mock_git.commit.call_count == 1


async def test_commit_with_marker_recovery_non_marker_error(temp_dir: Path) -> None:
    mock_git = _marker_git_ops()
    mock_git.commit.side_effect = GitError("Failed to commit: index.lock exists")
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        pytest.raises(GitError, match=r"index\.lock"),
    ):
        await commit_with_marker_recovery(temp_dir, "msg")

    mock_git.get_marker_flagged_files.assert_not_called()


async def test_commit_with_marker_recovery_no_flagged_files(temp_dir: Path) -> None:
    mock_git = _marker_git_ops()
    mock_git.commit.side_effect = MarkerRefusalError("refused")
    mock_git.get_marker_flagged_files.return_value = []
    with (
        patch("git_ai_sync.conflict_resolver.git_operations", mock_git),
        patch(
            "git_ai_sync.conflict_resolver.resolve_marker_flagged_files",
            new_callable=AsyncMock,
        ) as mock_resolver,
        pytest.raises(MarkerRefusalError),
    ):
        await commit_with_marker_recovery(temp_dir, "msg")

    mock_resolver.assert_not_awaited()
