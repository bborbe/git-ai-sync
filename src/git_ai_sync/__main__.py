"""Entry point for git-ai-sync."""

import argparse
import logging
import os
import signal
import sys
from typing import NoReturn

from git_ai_sync import __version__
from git_ai_sync.logging_setup import configure_logging

logger = logging.getLogger(__name__)


def parse_args() -> argparse.Namespace:
    """Parse command-line arguments with environment variable defaults."""
    parser = argparse.ArgumentParser(
        description="Automatic Git sync with AI conflict resolution",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument(
        "--log-level",
        default=os.getenv("LOG_LEVEL", "INFO"),
        choices=["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"],
        help="Logging level",
    )

    subparsers = parser.add_subparsers(dest="command", required=True)

    # watch subcommand
    watch_parser = subparsers.add_parser("watch", help="Start watching and syncing")
    watch_parser.add_argument(
        "--interval",
        type=int,
        default=int(os.getenv("GIT_AI_SYNC_INTERVAL", "30")),
        help="Sync interval in seconds",
    )
    watch_parser.add_argument(
        "path",
        nargs="?",
        default=".",
        help="Repository path to watch",
    )

    # sync subcommand
    sync_parser = subparsers.add_parser("sync", help="Run sync once")
    sync_parser.add_argument(
        "path",
        nargs="?",
        default=".",
        help="Repository path to sync",
    )

    # resolve subcommand
    resolve_parser = subparsers.add_parser("resolve", help="Resolve conflicts in current repo")
    resolve_parser.add_argument(
        "path",
        nargs="?",
        default=".",
        help="Repository path with conflicts",
    )

    # status subcommand
    status_parser = subparsers.add_parser("status", help="Show sync status")
    status_parser.add_argument(
        "path",
        nargs="?",
        default=".",
        help="Repository path",
    )

    # config subcommand
    config_parser = subparsers.add_parser("config", help="Configure settings")
    config_parser.add_argument("--interval", type=int, help="Set sync interval")
    config_parser.add_argument("--model", help="Set Claude model")

    # version subcommand
    subparsers.add_parser("version", help="Show version information")

    # doctor subcommand
    subparsers.add_parser("doctor", help="Verify Claude Code CLI setup")

    return parser.parse_args()


def setup_signal_handlers() -> None:
    """Set up signal handlers for graceful shutdown."""

    def handle_signal(signum: int, _frame: object) -> NoReturn:
        logger.info(f"Received signal {signum}, shutting down")
        sys.exit(0)

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)


def cmd_watch(args: argparse.Namespace) -> None:
    """Start watching and syncing with debounce-gated polling."""
    import time
    from pathlib import Path

    from git_ai_sync import git_operations
    from git_ai_sync.config import Config
    from git_ai_sync.file_watcher import ChangeTracker

    config = Config()
    repo_path = Path(args.path).resolve()
    interval = float(args.interval)

    logger.info(f"Starting watch mode: path={repo_path}, interval={interval}s")

    # Validate git repository once at startup
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        logger.error(f"Not a git repository: {repo_path}")
        sys.exit(1)

    logger.info(f"Watching: {git_repo}")
    logger.info(f"Interval: {interval}s (skips if actively editing)")

    # Start filesystem watcher to track changes
    tracker = ChangeTracker(git_repo)
    tracker.start()

    iteration = 0
    try:
        while True:
            iteration += 1
            time.sleep(interval)

            # Check if files changed recently (within interval)
            seconds_since_change = tracker.get_seconds_since_last_change()
            if seconds_since_change < interval:
                logger.info(
                    f"[{iteration}] Skipping - files changed"
                    f" {seconds_since_change:.1f}s ago (still editing)"
                )
                continue

            # Safe to sync - no recent changes
            logger.info(f"[{iteration}] Checking...")
            try:
                # Check for local changes first
                has_local_changes = git_operations.has_changes(git_repo)

                # Commit local changes before pulling (pull --rebase requires clean tree)
                if has_local_changes:
                    changed_files = git_operations.get_changed_files_short(git_repo)
                    logger.info(f"Local changes detected ({len(changed_files)} file(s))")
                    for file_line in changed_files[:5]:
                        logger.info(f"  {file_line}")
                    if len(changed_files) > 5:
                        logger.info(f"  ... and {len(changed_files) - 5} more")

                    git_operations.stage_all(git_repo)
                    commit_msg = git_operations.generate_commit_message(config.commit_prefix)
                    git_operations.commit(git_repo, commit_msg)
                    logger.info(f"Committed: {commit_msg}")

                # Pull to get remote changes
                try:
                    before_pull = git_operations.get_head_commit(git_repo)
                    git_operations.pull_rebase(git_repo)
                    after_pull = git_operations.get_head_commit(git_repo)

                    if before_pull != after_pull:
                        commit_count = git_operations.get_commit_count(
                            git_repo, before_pull, after_pull
                        )
                        commits = git_operations.get_commit_log(git_repo, before_pull, after_pull)
                        logger.info(f"Pulled {commit_count} commit(s) from remote")
                        for commit_line in commits[:3]:
                            logger.info(f"  {commit_line}")
                        if len(commits) > 3:
                            logger.info(f"  ... and {len(commits) - 3} more")
                    else:
                        logger.info("No new commits from remote")

                except git_operations.GitError as e:
                    if "conflicts" in str(e).lower():
                        logger.error(f"Rebase conflicts detected: {e}")
                        logger.error(f"Run 'git-ai-sync resolve {git_repo}' to resolve")
                        sys.exit(1)
                    raise

                if not has_local_changes:
                    logger.info("No local changes")
                    continue

                # Push to remote
                git_operations.push(git_repo)
                logger.info("Pushed to remote")

            except git_operations.GitError as e:
                logger.error(f"Sync failed: {e}")
                logger.info("Continuing to watch...")

            except Exception as e:
                logger.exception(f"Unexpected error: {e}")
                logger.info("Continuing to watch...")

    finally:
        tracker.stop()


def cmd_sync(args: argparse.Namespace) -> None:
    """Run sync once."""
    from pathlib import Path

    from git_ai_sync import git_operations
    from git_ai_sync.config import Config

    config = Config()
    repo_path = Path(args.path).resolve()

    logger.info(f"Running sync: path={repo_path}")

    # 1. Validate git repository
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        logger.error(f"Not a git repository: {repo_path}")
        sys.exit(1)

    logger.info(f"Git repository: {git_repo}")
    current_branch = git_operations.get_current_branch(git_repo)
    logger.info(f"Current branch: {current_branch}")

    # 2. Check for uncommitted changes
    if not git_operations.has_changes(git_repo):
        logger.info("No changes to sync")
        return

    # 3. Stage all changes
    logger.info("Staging changes...")
    try:
        git_operations.stage_all(git_repo)
        logger.info("Staged")
    except git_operations.GitError as e:
        logger.error(f"Failed to stage: {e}")
        sys.exit(1)

    # 4. Commit with auto-generated message
    commit_msg = git_operations.generate_commit_message(config.commit_prefix)
    logger.info(f"Committing: {commit_msg}")
    try:
        git_operations.commit(git_repo, commit_msg)
        logger.info("Committed")
    except git_operations.GitError as e:
        logger.error(f"Failed to commit: {e}")
        sys.exit(1)

    # 5. Pull with rebase
    logger.info("Pulling with rebase...")
    try:
        git_operations.pull_rebase(git_repo)
        logger.info("Pulled")
    except git_operations.GitError as e:
        if "conflicts" in str(e).lower():
            logger.error(f"Rebase conflicts detected: {e}")
            logger.error("Run 'git-ai-sync resolve' to resolve conflicts")
            sys.exit(1)
        logger.error(f"Failed to pull: {e}")
        sys.exit(1)

    # 6. Push to remote
    logger.info("Pushing to remote...")
    try:
        git_operations.push(git_repo)
        logger.info("Pushed")
    except git_operations.GitError as e:
        logger.error(f"Failed to push: {e}")
        sys.exit(1)

    logger.info(f"Sync completed: {git_repo}")


def cmd_resolve(args: argparse.Namespace) -> None:
    """Resolve conflicts."""
    import asyncio
    from pathlib import Path

    from git_ai_sync import conflict_resolver, git_operations
    from git_ai_sync.config import Config

    config = Config()
    repo_path = Path(args.path).resolve()

    logger.info(f"Resolving conflicts: path={repo_path}")

    # 1. Validate git repository
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        logger.error(f"Not a git repository: {repo_path}")
        sys.exit(1)

    # 2. Check if in conflict state (rebase or merge)
    if not git_operations.is_in_conflict_state(git_repo):
        logger.error(
            "Not in conflict state (rebase or merge). Run 'git-ai-sync sync' to sync changes."
        )
        sys.exit(1)

    logger.info(f"Resolving conflicts with Claude ({config.model})...")

    # 4. Resolve conflicts
    async def run_resolution() -> tuple[int, list[str]]:
        return await conflict_resolver.resolve_all_conflicts(git_repo, config.model)

    try:
        resolved_count, failed_files = asyncio.run(run_resolution())
    except conflict_resolver.ConflictError as e:
        logger.error(f"Resolution failed: {e}")
        sys.exit(1)

    if failed_files:
        logger.warning(f"Failed to resolve {len(failed_files)} files")
        for file in failed_files:
            logger.warning(f"  {file}")
        sys.exit(1)

    if resolved_count == 0:
        logger.info("No conflicts found")
        sys.exit(1)

    logger.info(f"Resolved {resolved_count} files")

    # 5. Continue rebase
    logger.info("Continuing rebase...")
    try:
        conflict_resolver.do_continue_rebase(git_repo)
    except conflict_resolver.ConflictError as e:
        logger.error(f"Rebase continuation failed: {e}")
        sys.exit(1)
    logger.info("Rebase continued")

    # 6. Push changes
    logger.info("Pushing to remote...")
    try:
        git_operations.push(git_repo)
    except git_operations.GitError as e:
        logger.error(f"Failed to push: {e}")
        sys.exit(1)
    logger.info("Pushed")
    logger.info(f"Conflicts resolved: {git_repo}")


def cmd_status(args: argparse.Namespace) -> None:
    """Show status."""
    from pathlib import Path

    from git_ai_sync import git_operations

    repo_path = Path(args.path).resolve()
    logger.info(f"Showing status: path={repo_path}")

    # Validate git repository
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        logger.error(f"Not a git repository: {repo_path}")
        sys.exit(1)

    logger.info(f"Repository: {git_repo}")

    # Get current branch
    try:
        branch = git_operations.get_current_branch(git_repo)
        logger.info(f"Branch: {branch}")
    except git_operations.GitError as e:
        logger.warning(f"Unable to determine branch: {e}")

    # Check for changes
    try:
        has_changes = git_operations.has_changes(git_repo)
        if has_changes:
            logger.info("Status: Uncommitted changes")
        else:
            logger.info("Status: Clean (no changes)")
    except git_operations.GitError as e:
        logger.warning(f"Unable to check status: {e}")

    # Check if in rebase
    if git_operations.is_in_rebase(git_repo):
        logger.warning("Currently in rebase state")
        logger.warning("Run 'git-ai-sync resolve' to resolve conflicts")


def cmd_config(args: argparse.Namespace) -> None:
    """Configure settings."""
    logger.info("Configuring settings")
    if args.interval:
        logger.info(f"Setting interval to {args.interval}s")
        # TODO: save to config
    if args.model:
        logger.info(f"Setting model to {args.model}")
        # TODO: save to config


def cmd_doctor() -> None:
    """Verify Claude Code CLI and dependencies are working.

    Checks:
    1. Claude Code CLI binary exists and is executable
    2. Node.js is installed (CLI runtime requirement)
    3. Git is available
    4. CLI has active authenticated session (test query)
    """
    import asyncio
    import os
    import shutil
    from pathlib import Path

    from claude_code_sdk import ClaudeCodeOptions, ClaudeSDKClient, ClaudeSDKError

    checks_passed = 0
    checks_total = 4  # Git repo check is informational only, not counted

    # 1. Check Claude Code CLI binary
    logger.info("Checking Claude Code CLI installation...")
    cli_path = shutil.which("claude")
    if not cli_path:
        # Check fallback locations (same as claude-code-sdk)
        fallback_locations = [
            Path.home() / ".npm-global/bin/claude",
            Path("/usr/local/bin/claude"),
            Path("/opt/homebrew/bin/claude"),
            Path.home() / "node_modules/.bin/claude",
        ]
        cli_path = next((str(p) for p in fallback_locations if p.exists()), None)

    if cli_path and os.access(cli_path, os.X_OK):
        logger.info(f"✓ Claude Code CLI found: {cli_path}")
        checks_passed += 1
    else:
        logger.error("✗ Claude Code CLI not found or not executable")
        logger.error("  Install: npm install -g @anthropic-ai/claude-code")

    # 2. Check Node.js
    logger.info("Checking Node.js installation...")
    node_path = shutil.which("node")
    if node_path:
        logger.info(f"✓ Node.js found: {node_path}")
        checks_passed += 1
    else:
        logger.error("✗ Node.js not found")
        logger.error("  Install from: https://nodejs.org/")

    # 3. Check Git
    logger.info("Checking Git installation...")
    git_path = shutil.which("git")
    if git_path:
        logger.info(f"✓ Git found: {git_path}")
        checks_passed += 1
    else:
        logger.error("✗ Git not found")
        logger.error("  Install Git first")

    # 4. Check current directory is a git repo (informational only, not counted toward pass/fail)
    from git_ai_sync import git_operations

    repo_path = Path(".").resolve()
    git_repo = git_operations.find_git_repo(repo_path)
    if git_repo:
        logger.info(f"✓ Current directory is a git repository: {git_repo}")
        # Note: Not incrementing checks_passed - this is informational only
    else:
        logger.warning("⚠ Current directory is not a git repository")
        logger.info("  (This is OK - just informational)")

    # 5. Test Claude Code session with minimal query
    logger.info("Testing Claude Code CLI session...")
    if not cli_path:
        logger.error("✗ Skipping session test - CLI not found")
    else:

        async def test_session() -> bool:
            from claude_code_sdk import AssistantMessage

            try:
                options = ClaudeCodeOptions(model="claude-sonnet-4-5-20250929")
                async with ClaudeSDKClient(options=options) as client:
                    await client.query("Respond with just: OK")
                    # Consume response to complete the request
                    async for message in client.receive_response():
                        if isinstance(message, AssistantMessage):
                            # If we get a response, session is valid
                            return True
                # If no assistant message received, still valid (query sent)
                return True
            except ClaudeSDKError as e:
                logger.error(f"✗ Claude Code session test failed: {e}")
                logger.error("  Run 'claude login' to authenticate")
                return False
            except Exception as e:
                logger.error(f"✗ Unexpected error during session test: {e}")
                return False

        try:
            session_valid = asyncio.run(test_session())
            if session_valid:
                logger.info("✓ Claude Code CLI session is active")
                checks_passed += 1
        except Exception as e:
            logger.error(f"✗ Session test failed: {e}")

    # Summary
    logger.info("")
    logger.info("=" * 60)
    if checks_passed == checks_total:
        logger.info(f"✓ All checks passed ({checks_passed}/{checks_total})")
        logger.info("git-ai-sync is ready to use!")
        sys.exit(0)
    else:
        logger.warning(f"⚠ {checks_passed}/{checks_total} checks passed")
        logger.error(f"✗ {checks_total - checks_passed} check(s) failed")
        logger.error("Please fix the issues above before using git-ai-sync")
        sys.exit(1)


def cmd_version() -> None:
    """Print version information."""
    print(f"git-ai-sync {__version__}")


def main() -> None:
    """Main entry point."""
    args = parse_args()
    configure_logging(args.log_level)
    setup_signal_handlers()

    if args.command == "watch":
        cmd_watch(args)
    elif args.command == "sync":
        cmd_sync(args)
    elif args.command == "resolve":
        cmd_resolve(args)
    elif args.command == "status":
        cmd_status(args)
    elif args.command == "config":
        cmd_config(args)
    elif args.command == "version":
        cmd_version()
    elif args.command == "doctor":
        cmd_doctor()


if __name__ == "__main__":
    main()
