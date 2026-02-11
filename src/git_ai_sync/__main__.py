"""Entry point for git-ai-sync."""

import argparse
import logging
import os
import signal
import sys
from typing import NoReturn

from git_ai_sync import __version__

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

    return parser.parse_args()


def setup_signal_handlers() -> None:
    """Set up signal handlers for graceful shutdown."""

    def handle_signal(signum: int, _frame: object) -> NoReturn:
        logger.info(f"Received signal {signum}, shutting down")
        sys.exit(0)

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)


def configure_logging(level: str) -> None:
    """Configure logging."""
    logging.basicConfig(
        level=getattr(logging, level),
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )


def cmd_watch(args: argparse.Namespace) -> None:
    """Start watching and syncing."""
    import time
    from pathlib import Path

    from git_ai_sync import git_operations
    from git_ai_sync.config import Config

    config = Config()
    repo_path = Path(args.path).resolve()
    interval = args.interval

    logger.info(f"Starting watch mode: path={repo_path}, interval={interval}s")

    # Validate git repository once at startup
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        logger.error(f"Not a git repository: {repo_path}")
        print(f"❌ Not a git repository: {repo_path}")
        sys.exit(1)

    print(f"👁️  Watching: {git_repo}")
    print(f"⏱️  Interval: {interval}s")
    print("Press Ctrl+C to stop")
    print()

    iteration = 0
    while True:
        iteration += 1
        logger.info(f"Sync iteration {iteration}")

        try:
            # Check for changes
            has_changes = git_operations.has_changes(git_repo)

            if not has_changes:
                logger.debug("No changes detected")
                print(f"[{iteration}] No changes", end="\r", flush=True)
            else:
                print(f"\n[{iteration}] Changes detected, syncing...")

                # Stage all changes
                git_operations.stage_all(git_repo)
                logger.info("✓ Staged")

                # Commit with auto-generated message
                commit_msg = git_operations.generate_commit_message(config.commit_prefix)
                git_operations.commit(git_repo, commit_msg)
                logger.info(f"✓ Committed: {commit_msg}")
                print(f"  ✓ Committed: {commit_msg}")

                # Pull with rebase
                try:
                    git_operations.pull_rebase(git_repo)
                    logger.info("✓ Pulled")
                    print("  ✓ Pulled with rebase")
                except git_operations.GitError as e:
                    if "conflicts" in str(e).lower():
                        logger.error(f"Rebase conflicts detected: {e}")
                        print("\n  ❌ Rebase conflicts detected")
                        print(f"  💡 Run 'git-ai-sync resolve {git_repo}' to resolve")
                        print("  ⏸️  Stopping watch mode")
                        sys.exit(1)
                    raise

                # Push to remote
                git_operations.push(git_repo)
                logger.info("✓ Pushed")
                print("  ✓ Pushed to remote")
                print("  ✅ Sync completed\n")

        except git_operations.GitError as e:
            # Log error but continue watching
            logger.error(f"Sync failed: {e}")
            print(f"\n  ⚠️  Sync failed: {e}")
            print("  Continuing to watch...\n")

        except KeyboardInterrupt:
            print("\n\n✋ Stopping watch mode")
            logger.info("Watch mode stopped by user")
            break

        except Exception as e:
            # Unexpected error - log but continue
            logger.exception(f"Unexpected error: {e}")
            print(f"\n  ⚠️  Unexpected error: {e}")
            print("  Continuing to watch...\n")

        # Wait for next iteration
        time.sleep(interval)


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
        print(f"❌ Not a git repository: {repo_path}")
        sys.exit(1)

    logger.info(f"Git repository: {git_repo}")
    current_branch = git_operations.get_current_branch(git_repo)
    logger.info(f"Current branch: {current_branch}")

    # 2. Check for uncommitted changes
    if not git_operations.has_changes(git_repo):
        logger.info("No changes to sync")
        print("✓ No changes to sync")
        return

    # 3. Stage all changes
    logger.info("Staging changes...")
    print("→ Staging changes...")
    try:
        git_operations.stage_all(git_repo)
        logger.info("✓ Staged")
    except git_operations.GitError as e:
        logger.error(f"Failed to stage: {e}")
        print(f"❌ Failed to stage: {e}")
        sys.exit(1)

    # 4. Commit with auto-generated message
    commit_msg = git_operations.generate_commit_message(config.commit_prefix)
    logger.info(f"Committing: {commit_msg}")
    print(f"→ Committing: {commit_msg}")
    try:
        git_operations.commit(git_repo, commit_msg)
        logger.info("✓ Committed")
    except git_operations.GitError as e:
        logger.error(f"Failed to commit: {e}")
        print(f"❌ Failed to commit: {e}")
        sys.exit(1)

    # 5. Pull with rebase
    logger.info("Pulling with rebase...")
    print("→ Pulling with rebase...")
    try:
        git_operations.pull_rebase(git_repo)
        logger.info("✓ Pulled")
        print("✓ Pulled")
    except git_operations.GitError as e:
        if "conflicts" in str(e).lower():
            logger.error(f"Rebase conflicts detected: {e}")
            print(f"❌ {e}")
            print("💡 Run 'git-ai-sync resolve' to resolve conflicts (not implemented yet)")
            sys.exit(1)
        logger.error(f"Failed to pull: {e}")
        print(f"❌ Failed to pull: {e}")
        sys.exit(1)

    # 6. Push to remote
    logger.info("Pushing to remote...")
    print("→ Pushing to remote...")
    try:
        git_operations.push(git_repo)
        logger.info("✓ Pushed")
        print("✓ Pushed")
    except git_operations.GitError as e:
        logger.error(f"Failed to push: {e}")
        print(f"❌ Failed to push: {e}")
        sys.exit(1)

    print(f"✅ Sync completed: {git_repo}")


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
        print(f"❌ Not a git repository: {repo_path}")
        sys.exit(1)

    # 2. Check if in rebase state
    if not git_operations.is_in_rebase(git_repo):
        logger.error("Not in rebase state")
        print("❌ Not in rebase state. Run 'git-ai-sync sync' to sync changes.")
        sys.exit(1)

    # 3. Check for ANTHROPIC_API_KEY
    if not config.anthropic_api_key:
        logger.error("ANTHROPIC_API_KEY not set")
        print("❌ ANTHROPIC_API_KEY environment variable not set")
        print("   Set it with: export ANTHROPIC_API_KEY=your_key")
        sys.exit(1)

    print(f"🤖 Resolving conflicts with Claude ({config.model})...")
    logger.info(f"Using model: {config.model}")

    # 4. Resolve conflicts
    async def run_resolution() -> bool:
        try:
            resolved_count, failed_files = await conflict_resolver.resolve_all_conflicts(
                git_repo, config.model
            )

            if failed_files:
                print(f"⚠️  Failed to resolve {len(failed_files)} files:")
                for file in failed_files:
                    print(f"   - {file}")
                return False

            if resolved_count == 0:
                print("No conflicts found")
                return False

            print(f"✓ Resolved {resolved_count} files")

            # 5. Continue rebase
            print("→ Continuing rebase...")
            await conflict_resolver.continue_rebase(git_repo)
            print("✓ Rebase continued")

            # 6. Push changes
            print("→ Pushing to remote...")
            git_operations.push(git_repo)
            print("✓ Pushed")

            return True

        except conflict_resolver.ConflictError as e:
            logger.error(f"Resolution failed: {e}")
            print(f"❌ {e}")
            return False

    success = asyncio.run(run_resolution())
    if success:
        print(f"✅ Conflicts resolved: {git_repo}")
    else:
        sys.exit(1)


def cmd_status(args: argparse.Namespace) -> None:
    """Show status."""
    from pathlib import Path

    from git_ai_sync import git_operations

    repo_path = Path(args.path).resolve()
    logger.info(f"Showing status: path={repo_path}")

    # Validate git repository
    git_repo = git_operations.find_git_repo(repo_path)
    if not git_repo:
        print(f"❌ Not a git repository: {repo_path}")
        sys.exit(1)

    print(f"Repository: {git_repo}")

    # Get current branch
    try:
        branch = git_operations.get_current_branch(git_repo)
        print(f"Branch: {branch}")
    except git_operations.GitError as e:
        print(f"⚠️  Unable to determine branch: {e}")

    # Check for changes
    try:
        has_changes = git_operations.has_changes(git_repo)
        if has_changes:
            print("Status: Uncommitted changes")
        else:
            print("Status: Clean (no changes)")
    except git_operations.GitError as e:
        print(f"⚠️  Unable to check status: {e}")

    # Check if in rebase
    if git_operations.is_in_rebase(git_repo):
        print("⚠️  Currently in rebase state")
        print("   Run 'git-ai-sync resolve' to resolve conflicts")


def cmd_config(args: argparse.Namespace) -> None:
    """Configure settings."""
    logger.info("Configuring settings")
    if args.interval:
        print(f"Setting interval to {args.interval}s")
        # TODO: save to config
    if args.model:
        print(f"Setting model to {args.model}")
        # TODO: save to config


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


if __name__ == "__main__":
    main()
