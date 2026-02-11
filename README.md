# git-ai-sync

Automatic Git repository sync with AI-powered conflict resolution.

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
- `GIT_AI_SYNC_MODEL` - Claude model (default: claude-sonnet-4-5)
- `GIT_AI_SYNC_COMMIT_PREFIX` - Commit message prefix (default: "auto")

## Development

```bash
make install  # Install dependencies
make test     # Run tests
make precommit  # Run all checks
```

## License

BSD-2-Clause
