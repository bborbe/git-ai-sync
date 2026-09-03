"""AI-powered conflict resolution using Claude."""

import logging
import re
from pathlib import Path
from typing import Any

from claude_code_sdk import (
    AssistantMessage,
    ClaudeCodeOptions,
    ClaudeSDKClient,
    ClaudeSDKError,
    TextBlock,
)
from claude_code_sdk._errors import MessageParseError
from claude_code_sdk._internal import message_parser as _message_parser

from git_ai_sync import git_operations

logger = logging.getLogger(__name__)

# Top-level message types that claude-code-sdk 0.0.25's parse_message knows how
# to parse. Any other type (e.g. the CLI's informational rate_limit_event)
# raises MessageParseError("Unknown message type: ..."), which the resolver's
# except ClaudeSDKError would turn into ConflictError and abort resolution.
_SDK_HANDLED_MESSAGE_TYPES = frozenset(("user", "assistant", "system", "result", "stream_event"))

# Reference to the SDK's original parser, captured before the tolerance wrapper
# is installed so it can delegate without recursing into itself.
_original_parse_message = _message_parser.parse_message


def _parse_message_tolerant(data: dict[str, Any]) -> object | None:
    """Parse a raw CLI stream message, skipping unrecognized message types.

    The Claude Code CLI emits informational stream messages (e.g.
    rate_limit_event) at session start that claude-code-sdk 0.0.25's
    parse_message does not recognize; it raises MessageParseError, which the
    resolver's except ClaudeSDKError converts to ConflictError and aborts
    resolution. Returning None for those lets the resolver's receive loop
    (which only acts on AssistantMessage) skip them. Genuine parse errors for
    handled message types (missing fields, malformed payloads) still
    propagate.

    Args:
        data: Raw message dictionary from the CLI output stream

    Returns:
        Parsed Message, or None when the top-level type is unrecognized
    """
    try:
        return _original_parse_message(data)
    except MessageParseError:
        if data.get("type") not in _SDK_HANDLED_MESSAGE_TYPES:
            return None
        raise


def _install_message_parser_tolerance() -> None:
    """Install the tolerant parser on claude_code_sdk's message parser.

    Idempotent: repeated calls (e.g. re-imports) do not double-wrap. The SDK's
    ClaudeSDKClient.receive_messages resolves parse_message at each call, so
    the attribute swap is picked up on the next receive.
    """
    if _message_parser.parse_message is _parse_message_tolerant:
        return
    _message_parser.parse_message = _parse_message_tolerant  # type: ignore[assignment]


_install_message_parser_tolerance()


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


async def resolve_marker_flagged_files(
    repo_path: Path,
    flagged_files: list[str],
    model: str = "claude-sonnet-4-5-20250929",
) -> None:
    """Resolve conflict markers in files flagged by the pre-commit hook and re-stage them.

    Args:
        repo_path: Path to git repository
        flagged_files: Files flagged by the pre-commit hook for conflict markers
        model: Claude model to use

    Raises:
        ConflictError: If resolution or staging fails on any file (stops early)
    """
    for file_path in flagged_files:
        full_path = repo_path / file_path
        try:
            content = full_path.read_text(encoding="utf-8")
            resolved_content = await resolve_conflict_with_claude(file_path, content, model)
            full_path.write_text(resolved_content, encoding="utf-8")
            git_operations.stage_file(repo_path, file_path)
            logger.info(f"Resolved and staged {file_path}")
        except (git_operations.GitError, ConflictError) as e:
            raise ConflictError(f"Failed to resolve marker-flagged file {file_path}: {e}") from e


async def commit_with_marker_recovery(
    repo_path: Path,
    message: str,
    model: str = "claude-sonnet-4-5-20250929",
) -> None:
    """Commit with bounded auto-recovery when the pre-commit hook refuses the
    commit over conflict-marker content.

    Args:
        repo_path: Path to git repository
        message: Commit message
        model: Claude model to use

    Raises:
        MarkerRefusalError: If the commit is still refused after the bounded
            recovery rounds (the final error names still-flagged files)
        GitError: Non-marker commit failures, propagated unchanged
    """
    max_attempts = 3
    attempt = 0
    while True:
        attempt += 1
        try:
            git_operations.commit(repo_path, message)
            return
        except git_operations.MarkerRefusalError:
            if attempt >= max_attempts:
                break
            flagged_files = git_operations.get_marker_flagged_files(repo_path)
            if not flagged_files:
                break
            logger.info(
                f"Marker refusal detected; resolving {len(flagged_files)} flagged "
                f"file(s): {', '.join(flagged_files)}"
            )
            try:
                await resolve_marker_flagged_files(repo_path, flagged_files, model)
            except ConflictError as e:
                raise git_operations.MarkerRefusalError(
                    f"Failed to resolve marker-flagged files: {e}. "
                    "Run 'git-ai-sync resolve <path>' to resolve"
                ) from e
            logger.info("Recovery round complete; retrying commit")

    still_flagged = git_operations.get_marker_flagged_files(repo_path)
    raise git_operations.MarkerRefusalError(
        f"Commit refused by pre-commit hook after {max_attempts} attempts; "
        f"still-flagged files: {', '.join(still_flagged) or 'none'}. "
        "Run 'git-ai-sync resolve <path>' to resolve"
    )


def do_continue_rebase(repo_path: Path) -> None:
    """Continue conflict resolution (rebase or merge).

    Args:
        repo_path: Path to git repository

    Raises:
        ConflictError: If continuation fails
    """
    try:
        # Detect state and use appropriate continue command
        if git_operations.is_in_rebase(repo_path):
            git_operations.continue_rebase(repo_path)
        elif git_operations.is_in_merge(repo_path):
            git_operations.continue_merge(repo_path)
        else:
            raise ConflictError("Not in rebase or merge state")
    except git_operations.GitError as e:
        conflicted = git_operations.get_conflicted_files(repo_path)
        if conflicted:
            raise ConflictError(
                f"Continuation failed - still have conflicts in: {', '.join(conflicted)}"
            ) from e
        raise ConflictError(f"Failed to continue: {e}") from e
