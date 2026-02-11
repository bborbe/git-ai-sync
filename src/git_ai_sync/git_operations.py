"""Git operations for sync and conflict resolution."""

import subprocess
from datetime import UTC, datetime
from pathlib import Path


class GitError(Exception):
    """Git operation failed."""

    pass


def find_git_repo(path: Path) -> Path | None:
    """Find git repository root by walking UP from given path.

    Args:
        path: Starting path to search from

    Returns:
        Path to git repository root, or None if not found
    """
    current = Path(path).resolve()

    while current != current.parent:
        if (current / ".git").exists():
            return current
        current = current.parent

    return None


def has_changes(repo_path: Path) -> bool:
    """Check if repository has uncommitted changes.

    Args:
        repo_path: Path to git repository

    Returns:
        True if there are uncommitted changes, False otherwise
    """
    result = subprocess.run(
        ["git", "status", "--porcelain"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to check status: {result.stderr}")

    # Empty output means no changes
    return bool(result.stdout.strip())


def stage_all(repo_path: Path) -> None:
    """Stage all changes in repository.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If staging fails
    """
    result = subprocess.run(
        ["git", "add", "."],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to stage changes: {result.stderr}")


def commit(repo_path: Path, message: str) -> None:
    """Create a commit with given message.

    Args:
        repo_path: Path to git repository
        message: Commit message

    Raises:
        GitError: If commit fails
    """
    result = subprocess.run(
        ["git", "commit", "-m", message],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to commit: {result.stderr}")


def pull_rebase(repo_path: Path) -> None:
    """Pull with rebase from remote.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If pull fails or conflicts occur
    """
    result = subprocess.run(
        ["git", "pull", "--rebase"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        # Check if it's a conflict
        if is_in_rebase(repo_path):
            raise GitError("Rebase conflicts detected - use 'git-ai-sync resolve' to resolve")
        raise GitError(f"Failed to pull: {result.stderr}")


def push(repo_path: Path) -> None:
    """Push commits to remote.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If push fails
    """
    result = subprocess.run(
        ["git", "push"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to push: {result.stderr}")


def is_in_rebase(repo_path: Path) -> bool:
    """Check if repository is currently in rebase state.

    Args:
        repo_path: Path to git repository

    Returns:
        True if in rebase state, False otherwise
    """
    rebase_merge = repo_path / ".git" / "rebase-merge"
    rebase_apply = repo_path / ".git" / "rebase-apply"
    return rebase_merge.exists() or rebase_apply.exists()


def abort_rebase(repo_path: Path) -> None:
    """Abort current rebase operation.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If abort fails
    """
    result = subprocess.run(
        ["git", "rebase", "--abort"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to abort rebase: {result.stderr}")


def get_current_branch(repo_path: Path) -> str:
    """Get current branch name.

    Args:
        repo_path: Path to git repository

    Returns:
        Current branch name

    Raises:
        GitError: If unable to determine branch
    """
    result = subprocess.run(
        ["git", "branch", "--show-current"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get current branch: {result.stderr}")

    return result.stdout.strip()


def get_head_commit(repo_path: Path) -> str:
    """Get current HEAD commit hash.

    Args:
        repo_path: Path to git repository

    Returns:
        HEAD commit hash

    Raises:
        GitError: If unable to get HEAD
    """
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get HEAD: {result.stderr}")

    return result.stdout.strip()


def get_commit_count(repo_path: Path, from_ref: str, to_ref: str) -> int:
    """Get number of commits between two refs.

    Args:
        repo_path: Path to git repository
        from_ref: Starting ref
        to_ref: Ending ref

    Returns:
        Number of commits between refs
    """
    result = subprocess.run(
        ["git", "rev-list", "--count", f"{from_ref}..{to_ref}"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to count commits: {result.stderr}")

    return int(result.stdout.strip())


def get_commit_log(repo_path: Path, from_ref: str, to_ref: str) -> list[str]:
    """Get one-line commit log between two refs.

    Args:
        repo_path: Path to git repository
        from_ref: Starting ref
        to_ref: Ending ref

    Returns:
        List of one-line commit messages
    """
    result = subprocess.run(
        ["git", "log", "--oneline", f"{from_ref}..{to_ref}"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get commit log: {result.stderr}")

    lines = result.stdout.strip()
    if not lines:
        return []
    return lines.split("\n")


def get_changed_files_short(repo_path: Path) -> list[str]:
    """Get short status of changed files.

    Args:
        repo_path: Path to git repository

    Returns:
        List of short status lines (e.g. " M file.txt")
    """
    result = subprocess.run(
        ["git", "status", "--short"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get changed files: {result.stderr}")

    output = result.stdout.rstrip("\n")
    if not output:
        return []
    return [line for line in output.split("\n") if line]


def stage_file(repo_path: Path, file_path: str) -> None:
    """Stage a specific file.

    Args:
        repo_path: Path to git repository
        file_path: Relative path to file to stage

    Raises:
        GitError: If staging fails
    """
    result = subprocess.run(
        ["git", "add", file_path],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to stage {file_path}: {result.stderr}")


def get_conflicted_files(repo_path: Path) -> list[str]:
    """Get list of files with merge conflicts.

    Args:
        repo_path: Path to git repository

    Returns:
        List of file paths with conflicts
    """
    result = subprocess.run(
        ["git", "diff", "--name-only", "--diff-filter=U"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get conflicted files: {result.stderr}")

    files = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
    return files


def continue_rebase(repo_path: Path) -> None:
    """Continue rebase after resolving conflicts.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If rebase continuation fails
    """
    result = subprocess.run(
        ["git", "rebase", "--continue"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to continue rebase: {result.stderr}")


def generate_commit_message(prefix: str = "auto") -> str:
    """Generate auto-commit message with timestamp.

    Args:
        prefix: Message prefix (default: "auto")

    Returns:
        Commit message in format: "auto: 2026-02-11T10:15:00+01:00"
    """
    now = datetime.now(UTC).astimezone()
    return f"{prefix}: {now.isoformat()}"
