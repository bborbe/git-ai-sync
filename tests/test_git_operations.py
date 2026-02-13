"""Tests for git operations module."""

import subprocess
from pathlib import Path
from unittest.mock import patch

import pytest

from git_ai_sync.git_operations import (
    GitError,
    commit,
    continue_merge,
    find_git_repo,
    generate_commit_message,
    get_changed_files_short,
    get_commit_count,
    get_commit_log,
    get_current_branch,
    get_head_commit,
    has_changes,
    is_in_conflict_state,
    is_in_merge,
    is_in_rebase,
    pull_rebase,
    push,
    stage_all,
    stage_file,
)

REPO = Path("/fake/repo")


def _mock_result(
    stdout: str = "", stderr: str = "", returncode: int = 0
) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


class TestGetHeadCommit:
    def test_returns_commit_hash(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("abc123\n")):
            assert get_head_commit(REPO) == "abc123"

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="not a repo"),
            ),
            pytest.raises(GitError),
        ):
            get_head_commit(REPO)


class TestGetCommitCount:
    def test_returns_count(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("5\n")):
            assert get_commit_count(REPO, "aaa", "bbb") == 5

    def test_returns_zero(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("0\n")):
            assert get_commit_count(REPO, "aaa", "aaa") == 0

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="bad"),
            ),
            pytest.raises(GitError),
        ):
            get_commit_count(REPO, "aaa", "bbb")


class TestGetCommitLog:
    def test_returns_lines(self) -> None:
        with patch(
            "subprocess.run",
            return_value=_mock_result("abc fix\ndef add\n"),
        ):
            assert get_commit_log(REPO, "aaa", "bbb") == ["abc fix", "def add"]

    def test_returns_empty_on_no_commits(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("")):
            assert get_commit_log(REPO, "aaa", "aaa") == []

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="bad"),
            ),
            pytest.raises(GitError),
        ):
            get_commit_log(REPO, "aaa", "bbb")


class TestGetChangedFilesShort:
    def test_returns_file_list(self) -> None:
        with patch(
            "subprocess.run",
            return_value=_mock_result(" M file1.txt\n?? file2.txt\n"),
        ):
            assert get_changed_files_short(REPO) == [" M file1.txt", "?? file2.txt"]

    def test_returns_empty_on_clean(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("")):
            assert get_changed_files_short(REPO) == []

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="bad"),
            ),
            pytest.raises(GitError),
        ):
            get_changed_files_short(REPO)


class TestStageFile:
    def test_stages_file(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            stage_file(REPO, "file.txt")
            mock.assert_called_once()
            assert mock.call_args[0][0] == ["git", "add", "file.txt"]

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="bad"),
            ),
            pytest.raises(GitError),
        ):
            stage_file(REPO, "file.txt")


class TestContinueRebase:
    def test_continues_rebase(self) -> None:
        from git_ai_sync.git_operations import continue_rebase

        with patch("subprocess.run", return_value=_mock_result()) as mock:
            continue_rebase(REPO)
            assert mock.call_args[0][0] == ["git", "rebase", "--continue"]

    def test_raises_on_failure(self) -> None:
        from git_ai_sync.git_operations import continue_rebase

        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="conflicts"),
            ),
            pytest.raises(GitError),
        ):
            continue_rebase(REPO)


class TestContinueMerge:
    def test_continues_merge(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            continue_merge(REPO)
            assert mock.call_args[0][0] == ["git", "commit", "--no-edit"]

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="conflicts"),
            ),
            pytest.raises(GitError),
        ):
            continue_merge(REPO)


class TestHasChanges:
    def test_returns_true_with_changes(self) -> None:
        with patch(
            "subprocess.run",
            return_value=_mock_result(" M file.txt\n"),
        ):
            assert has_changes(REPO) is True

    def test_returns_false_when_clean(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("")):
            assert has_changes(REPO) is False

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="error"),
            ),
            pytest.raises(GitError),
        ):
            has_changes(REPO)


class TestStageAll:
    def test_stages_all(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            stage_all(REPO)
            assert mock.call_args[0][0] == ["git", "add", "."]

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="error"),
            ),
            pytest.raises(GitError),
        ):
            stage_all(REPO)


class TestCommit:
    def test_commits(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            commit(REPO, "test msg")
            assert mock.call_args[0][0] == ["git", "commit", "-m", "test msg"]

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="error"),
            ),
            pytest.raises(GitError),
        ):
            commit(REPO, "msg")


class TestPullRebase:
    def test_pulls(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            pull_rebase(REPO)
            assert mock.call_args[0][0] == ["git", "pull", "--rebase"]

    def test_raises_conflict_error(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="conflict"),
            ),
            patch("git_ai_sync.git_operations.is_in_rebase", return_value=True),
            pytest.raises(GitError, match="conflicts"),
        ):
            pull_rebase(REPO)


class TestPush:
    def test_pushes(self) -> None:
        with patch("subprocess.run", return_value=_mock_result()) as mock:
            push(REPO)
            assert mock.call_args[0][0] == ["git", "push"]

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="rejected"),
            ),
            pytest.raises(GitError),
        ):
            push(REPO)


class TestGetCurrentBranch:
    def test_returns_branch(self) -> None:
        with patch("subprocess.run", return_value=_mock_result("master\n")):
            assert get_current_branch(REPO) == "master"

    def test_raises_on_failure(self) -> None:
        with (
            patch(
                "subprocess.run",
                return_value=_mock_result(returncode=1, stderr="error"),
            ),
            pytest.raises(GitError),
        ):
            get_current_branch(REPO)


class TestIsInRebase:
    def test_true_when_rebase_merge(self, temp_dir: Path) -> None:
        (temp_dir / ".git" / "rebase-merge").mkdir(parents=True)
        assert is_in_rebase(temp_dir) is True

    def test_true_when_rebase_apply(self, temp_dir: Path) -> None:
        (temp_dir / ".git" / "rebase-apply").mkdir(parents=True)
        assert is_in_rebase(temp_dir) is True

    def test_false_when_clean(self, temp_dir: Path) -> None:
        (temp_dir / ".git").mkdir(parents=True)
        assert is_in_rebase(temp_dir) is False


class TestIsInMerge:
    def test_true_when_merge_head_exists(self, temp_dir: Path) -> None:
        (temp_dir / ".git").mkdir(parents=True)
        (temp_dir / ".git" / "MERGE_HEAD").touch()
        assert is_in_merge(temp_dir) is True

    def test_false_when_no_merge_head(self, temp_dir: Path) -> None:
        (temp_dir / ".git").mkdir(parents=True)
        assert is_in_merge(temp_dir) is False


class TestIsInConflictState:
    def test_true_when_in_rebase(self, temp_dir: Path) -> None:
        (temp_dir / ".git" / "rebase-merge").mkdir(parents=True)
        assert is_in_conflict_state(temp_dir) is True

    def test_true_when_in_merge(self, temp_dir: Path) -> None:
        (temp_dir / ".git").mkdir(parents=True)
        (temp_dir / ".git" / "MERGE_HEAD").touch()
        assert is_in_conflict_state(temp_dir) is True

    def test_true_when_both(self, temp_dir: Path) -> None:
        (temp_dir / ".git" / "rebase-merge").mkdir(parents=True)
        (temp_dir / ".git" / "MERGE_HEAD").touch()
        assert is_in_conflict_state(temp_dir) is True

    def test_false_when_clean(self, temp_dir: Path) -> None:
        (temp_dir / ".git").mkdir(parents=True)
        assert is_in_conflict_state(temp_dir) is False


class TestFindGitRepo:
    def test_finds_repo(self, temp_dir: Path) -> None:
        resolved = temp_dir.resolve()
        (resolved / ".git").mkdir()
        assert find_git_repo(resolved) == resolved

    def test_finds_parent_repo(self, temp_dir: Path) -> None:
        resolved = temp_dir.resolve()
        (resolved / ".git").mkdir()
        sub = resolved / "sub" / "dir"
        sub.mkdir(parents=True)
        assert find_git_repo(sub) == resolved

    def test_returns_none_when_no_repo(self, temp_dir: Path) -> None:
        assert find_git_repo(temp_dir) is None


class TestGenerateCommitMessage:
    def test_contains_prefix(self) -> None:
        msg = generate_commit_message("vault backup")
        assert msg.startswith("vault backup: ")

    def test_default_prefix(self) -> None:
        msg = generate_commit_message()
        assert msg.startswith("auto: ")
