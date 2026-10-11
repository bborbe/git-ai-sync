"""Git operations for sync and conflict resolution."""

import re
import subprocess
from datetime import UTC, datetime
from pathlib import Path


class GitError(Exception):
    """Git operation failed."""

    pass


class MarkerRefusalError(GitError):
    """Commit refused by the pre-commit hook because staged content contains conflict markers."""

    pass


class UnsafePathRefusalError(GitError):
    """Commit refused by the pre-commit hook because a staged path is not safe on Windows."""

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
        UnsafePathRefusalError: If the pre-commit hook refuses the commit because
            a staged path is not safe on Windows
        MarkerRefusalError: If the pre-commit hook refuses the commit because
            staged content contains conflict markers
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
        if is_unsafe_path_refusal(result.stderr):
            raise UnsafePathRefusalError(f"Failed to commit: {result.stderr}")
        if is_marker_refusal(result.stderr):
            raise MarkerRefusalError(f"Failed to commit: {result.stderr}")
        raise GitError(f"Failed to commit: {result.stderr}")


def is_marker_refusal(message: str) -> bool:
    """Check whether an error message matches the pre-commit hook's marker-refusal signature.

    Args:
        message: Error message text (typically git stderr)

    Returns:
        True if the message contains "Refusing commit" or "conflict markers" (case-insensitive)
    """
    lowered = message.lower()
    return "refusing commit" in lowered or "conflict markers" in lowered


def is_unsafe_path_refusal(message: str) -> bool:
    """Check whether an error message matches the pre-commit hook's unsafe-path refusal signature.

    Args:
        message: Error message text (typically git stderr)

    Returns:
        True if the message contains "staged path is not safe on Windows" (case-insensitive)
    """
    return "staged path is not safe on windows" in message.lower()


_BLOCKED_PATH_RE = re.compile(r"^[ \t]+(?P<path>.+?)\s{2}—\s{2}(?P<reason>.+)$", re.MULTILINE)


def get_blocked_paths(message: str) -> list[str]:
    """Extract the offending paths from the pre-commit hook's unsafe-path refusal block.

    Args:
        message: Error message text (typically git stderr)

    Returns:
        List of offending paths named in the hook's indented block, or an empty list
    """
    return [match.group("path") for match in _BLOCKED_PATH_RE.finditer(message)]


_UNSAFE_CHARS = ':?*"<>|'
_RESERVED_DEVICES = frozenset(
    {
        "CON",
        "PRN",
        "AUX",
        "NUL",
        *(f"COM{i}" for i in range(1, 10)),
        *(f"LPT{i}" for i in range(1, 10)),
    }
)


def _sanitize_component(component: str) -> str:
    """Sanitize a single path component into a Windows-safe name."""
    cleaned = "".join("-" if ch in _UNSAFE_CHARS else ch for ch in component)
    while "--" in cleaned:
        cleaned = cleaned.replace("--", "-")
    cleaned = cleaned.rstrip(". ")
    stem, dot, ext = cleaned.rpartition(".")
    if not dot:
        stem, dot, ext = cleaned, "", ""
    stem = stem.strip("-")
    if not stem:
        return f"untitled{dot}{ext}"
    if stem.upper() in _RESERVED_DEVICES:
        stem = f"{stem}_"
    return f"{stem}{dot}{ext}"


def sanitize_path(path: str, existing: set[str] | None = None) -> str:
    """Rename an unsafe path to a Windows-safe one, disambiguating a collision.

    Args:
        path: Relative path whose components are sanitized
        existing: Optional set of already-taken paths; a collision gets a numeric suffix

    Returns:
        A Windows-safe path, suffixed with "-2", "-3", ... when it collides with ``existing``
    """
    sanitized = "/".join(_sanitize_component(part) for part in path.split("/"))
    if not existing or sanitized not in existing:
        return sanitized
    stem, dot, ext = sanitized.rpartition(".")
    if not dot:
        stem, dot, ext = sanitized, "", ""
    counter = 2
    candidate = f"{stem}-{counter}{dot}{ext}"
    while candidate in existing:
        counter += 1
        candidate = f"{stem}-{counter}{dot}{ext}"
    return candidate


def get_marker_flagged_files(repo_path: Path) -> list[str]:
    """List staged files whose hunks contain conflict markers (the pre-commit hook's own pattern).

    Args:
        repo_path: Path to git repository

    Returns:
        List of file paths whose staged hunks contain conflict markers

    Raises:
        GitError: If the git diff command fails
    """
    result = subprocess.run(
        ["git", "diff", "--cached", "--diff-filter=AM", "-G", "^<<<<<<< ", "--name-only"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to get marker-flagged files: {result.stderr}")

    files = [line.strip() for line in result.stdout.strip().split("\n") if line.strip()]
    return files


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


def pull_merge(repo_path: Path) -> None:
    """Pull with merge from remote.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If pull fails or conflicts occur
    """
    result = subprocess.run(
        ["git", "pull", "--no-rebase"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        # Check if it's a conflict
        if is_in_merge(repo_path):
            raise GitError("Merge conflicts detected - use 'git-ai-sync resolve' to resolve")
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


def is_in_merge(repo_path: Path) -> bool:
    """Check if repository is currently in merge state.

    Args:
        repo_path: Path to git repository

    Returns:
        True if in merge state, False otherwise
    """
    merge_head = repo_path / ".git" / "MERGE_HEAD"
    return merge_head.exists()


def is_in_conflict_state(repo_path: Path) -> bool:
    """Check if repository is in any conflict state (rebase or merge).

    Args:
        repo_path: Path to git repository

    Returns:
        True if in rebase or merge conflict state, False otherwise
    """
    return is_in_rebase(repo_path) or is_in_merge(repo_path)


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


def unstage_paths(repo_path: Path, paths: list[str]) -> None:
    """Unstage the given paths, keeping the files on disk.

    Uses `git reset -- <paths>` rather than `git restore --staged -- <paths>`:
    reset is lenient about a repository with no commits and about paths that are
    not in the index (both exit 0), whereas restore fails with
    `fatal: could not resolve 'HEAD'` (exit 128) or a pathspec error (exit 1).
    A brand-new repo whose very first commit is refused over an unsafe path is a
    real case, and it must not abort recovery with a `GitError`.

    Args:
        repo_path: Path to git repository
        paths: Relative paths to unstage

    Raises:
        GitError: If unstaging fails
    """
    if not paths:
        # Load-bearing: `git reset --` with no pathspec is a full mixed reset.
        return
    result = subprocess.run(
        ["git", "reset", "--", *paths],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise GitError(f"Failed to unstage paths: {result.stderr}")


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


def continue_merge(repo_path: Path) -> None:
    """Continue merge after resolving conflicts.

    Args:
        repo_path: Path to git repository

    Raises:
        GitError: If merge continuation fails
    """
    result = subprocess.run(
        ["git", "commit", "--no-edit"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise GitError(f"Failed to continue merge: {result.stderr}")


def is_ahead_of_remote(repo_path: Path) -> bool:
    """Check if local branch has commits not yet pushed to remote.

    Args:
        repo_path: Path to git repository

    Returns:
        True if local branch is ahead of remote tracking branch, False otherwise.
        Returns False if no upstream is configured or command fails.
    """
    result = subprocess.run(
        ["git", "rev-list", "--count", "@{upstream}..HEAD"],
        cwd=repo_path,
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        return False

    try:
        return int(result.stdout.strip()) > 0
    except ValueError:
        return False


def generate_commit_message(prefix: str = "auto") -> str:
    """Generate auto-commit message with timestamp.

    Args:
        prefix: Message prefix (default: "auto")

    Returns:
        Commit message in format: "auto: 2026-02-11T10:15:00+01:00"
    """
    now = datetime.now(UTC).astimezone()
    return f"{prefix}: {now.isoformat()}"
