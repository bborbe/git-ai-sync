# git-ai-sync

Automatic Git repository sync with AI-powered conflict resolution.

## How It Works

1. **Watch** - Polls repository at regular intervals for changes
2. **Debounce** - Skips sync when files changed recently (active editing)
3. **Sync** - Stages, commits, pulls with rebase, and pushes
4. **Resolve** - On rebase conflicts, invokes Claude to intelligently merge

## Prerequisites

- Python 3.14+
- Git installed and configured with remote
- `ANTHROPIC_API_KEY` environment variable (only for conflict resolution)

## Usage

Run directly from GitHub using `uvx`:

```bash
# Start watching and syncing (default: current directory, 30s interval)
uvx --from git+https://github.com/bborbe/git-ai-sync git-ai-sync watch

# Watch specific directory with custom interval
uvx --from git+https://github.com/bborbe/git-ai-sync git-ai-sync watch /path/to/repo --interval 60

# Run sync once
uvx --from git+https://github.com/bborbe/git-ai-sync git-ai-sync sync /path/to/repo

# Resolve conflicts in current repo
uvx --from git+https://github.com/bborbe/git-ai-sync git-ai-sync resolve

# Show status
uvx --from git+https://github.com/bborbe/git-ai-sync git-ai-sync status
```

### Local Development

```bash
# Use local version with --refresh to pick up changes
uvx --refresh --from ~/path/to/git-ai-sync git-ai-sync watch . --interval 5
```

### Installation (Optional)

For permanent installation:

```bash
pipx install git+https://github.com/bborbe/git-ai-sync
```

## Features

- **Auto-sync**: Polls repository at regular intervals to sync changes
- **Debounce-gating**: Skips sync during active editing (when files changed recently)
- **AI conflict resolution**: Uses Claude to intelligently resolve merge conflicts
- **Rebase workflow**: Uses `git pull --rebase` to maintain clean history

## Configuration

### Environment Variables

- `ANTHROPIC_API_KEY` - Required for AI conflict resolution (resolve command)
- `GIT_AI_SYNC_INTERVAL` - Sync interval in seconds (default: 30)
- `GIT_AI_SYNC_MODEL` - Claude model (default: claude-sonnet-4-5-20250929)
- `GIT_AI_SYNC_COMMIT_PREFIX` - Commit message prefix (default: "auto")

## Troubleshooting

**Watch mode shows no activity:**
- Verify the path is a git repository with a configured remote
- Check `--interval` is not too high

**Conflict resolution fails:**
- Ensure `ANTHROPIC_API_KEY` is set and valid
- Run `git-ai-sync status` to confirm rebase state
- Check logs for Claude API errors

**Changes not syncing:**
- Debounce may be active (files changed within the interval window)
- Run `git-ai-sync sync` manually to force a one-time sync

## Development

```bash
make install    # Install dependencies
make test       # Run tests
make precommit  # Run all checks (format, test, lint, typecheck)
```

## License

[BSD-2-Clause](LICENSE)
