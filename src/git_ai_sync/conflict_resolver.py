"""AI-powered conflict resolution using Claude."""

import logging
import re
from pathlib import Path

from claude_code_sdk import (
    AssistantMessage,
    ClaudeCodeOptions,
    ClaudeSDKClient,
    ClaudeSDKError,
    TextBlock,
)

from git_ai_sync import git_operations

logger = logging.getLogger(__name__)


class ConflictError(Exception):
    """Conflict resolution failed."""

    pass


def parse_conflict_markers(content: str) -> list[dict[str, str]]:
    """Parse conflict markers from file content.

    Args:
        content: File content with conflict markers

    Returns:
        List of conflict dicts with keys: ours, theirs, base (optional)

    Example conflict format:
        <<<<<<< HEAD
        our changes
        =======
        their changes
        >>>>>>> branch
    """
    conflicts = []

    # Pattern to match conflict markers
    pattern = re.compile(
        r"<{7} .*?\n(.*?)\n={7}\n(.*?)\n>{7} .*?\n",
        re.DOTALL,
    )

    for match in pattern.finditer(content):
        conflicts.append(
            {
                "ours": match.group(1),
                "theirs": match.group(2),
                "full_match": match.group(0),
            }
        )

    return conflicts


async def resolve_conflict_with_claude(
    file_path: str,
    content: str,
    model: str = "claude-sonnet-4-5-20250929",
) -> str:
    """Resolve conflicts in a file using Claude.

    Args:
        file_path: Relative path to file
        content: File content with conflict markers
        model: Claude model to use

    Returns:
        Resolved file content

    Raises:
        ConflictError: If resolution fails
    """
    logger.info(f"Resolving conflicts in {file_path} with Claude")

    # Parse conflicts
    conflicts = parse_conflict_markers(content)
    if not conflicts:
        return content  # No conflicts found

    # Determine file type for context
    file_type = Path(file_path).suffix or "unknown"

    # Build prompt
    prompt = f"""You are resolving a git merge conflict in a file.

File: {file_path}
Type: {file_type}

**Resolution Strategy:**
- Preserve ALL meaningful changes from both sides when possible
- For timestamps/dates: prefer the more recent one
- For additions: include both additions
- For contradictory edits: use judgment based on context
- Preserve file structure (frontmatter, formatting, headers)
- Never remove content unless it's clearly a deletion
- For Markdown: preserve frontmatter and heading structure

**File Content with Conflicts:**

```
{content}
```

**Task:**
Return the COMPLETE resolved file content with NO conflict markers.
Return ONLY the file content, no explanations, no markdown code blocks.
"""

    options = ClaudeCodeOptions(model=model)

    response_text = ""
    try:
        async with ClaudeSDKClient(options=options) as client:
            await client.query(prompt)

            async for message in client.receive_response():
                if isinstance(message, AssistantMessage):
                    for block in message.content:
                        if isinstance(block, TextBlock):
                            response_text += block.text

    except ClaudeSDKError as e:
        raise ConflictError(f"Claude API call failed: {e}") from e

    if not response_text:
        raise ConflictError("Claude returned empty response")

    # Clean up response (remove markdown code blocks if present)
    resolved = response_text.strip()
    if resolved.startswith("```"):
        # Remove code fence
        lines = resolved.split("\n")
        if lines[0].startswith("```"):
            lines = lines[1:]
        if lines and lines[-1].startswith("```"):
            lines = lines[:-1]
        resolved = "\n".join(lines)

    logger.info(f"Resolved {file_path}")
    return resolved


async def resolve_all_conflicts(
    repo_path: Path,
    model: str = "claude-sonnet-4-5-20250929",
) -> tuple[int, list[str]]:
    """Resolve all conflicts in repository using Claude.

    Args:
        repo_path: Path to git repository
        model: Claude model to use

    Returns:
        Tuple of (files_resolved, failed_files)

    Raises:
        ConflictError: If unable to get conflicted files
    """
    # Get conflicted files
    conflicted_files = git_operations.get_conflicted_files(repo_path)

    if not conflicted_files:
        logger.info("No conflicted files found")
        return 0, []

    logger.info(f"Found {len(conflicted_files)} conflicted files")

    resolved_count = 0
    failed_files = []

    for file_path in conflicted_files:
        full_path = repo_path / file_path
        logger.info(f"Resolving {file_path}")

        try:
            # Read file with conflicts
            content = full_path.read_text(encoding="utf-8")

            # Resolve with Claude
            resolved_content = await resolve_conflict_with_claude(file_path, content, model)

            # Write resolved content
            full_path.write_text(resolved_content, encoding="utf-8")

            # Stage resolved file
            git_operations.stage_file(repo_path, file_path)
            resolved_count += 1
            logger.info(f"Resolved and staged {file_path}")

        except git_operations.GitError as e:
            logger.error(f"Failed to stage {file_path}: {e}")
            failed_files.append(file_path)

        except ConflictError as e:
            logger.error(f"Failed to resolve {file_path}: {e}")
            failed_files.append(file_path)

    return resolved_count, failed_files


def do_continue_rebase(repo_path: Path) -> None:
    """Continue rebase after resolving conflicts.

    Args:
        repo_path: Path to git repository

    Raises:
        ConflictError: If rebase continuation fails
    """
    try:
        git_operations.continue_rebase(repo_path)
    except git_operations.GitError as e:
        conflicted = git_operations.get_conflicted_files(repo_path)
        if conflicted:
            raise ConflictError(
                f"Rebase failed - still have conflicts in: {', '.join(conflicted)}"
            ) from e
        raise ConflictError(f"Failed to continue rebase: {e}") from e
