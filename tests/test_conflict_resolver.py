"""Tests for conflict resolver module."""

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from claude_code_sdk import ClaudeSDKError

from git_ai_sync.conflict_resolver import (
    ConflictError,
    do_continue_rebase,
    parse_conflict_markers,
    resolve_all_conflicts,
    resolve_conflict_with_claude,
)
from git_ai_sync.git_operations import GitError

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


class TestResolveConflictWithClaude:
    async def test_no_conflicts_returns_content(self) -> None:
        result = await resolve_conflict_with_claude("file.md", "clean content")
        assert result == "clean content"

    async def test_returns_resolved_content(self) -> None:
        mock_cls = _mock_claude_client("resolved content")
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
