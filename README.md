# git-ai-sync

Automatic Git repository sync with AI-powered conflict resolution.

## Installation

```bash
pipx install git-ai-sync
```

## Usage

```bash
# Start watching and syncing
git-ai-sync watch

# Run sync once
git-ai-sync sync

# Resolve conflicts in current repo
git-ai-sync resolve

# Show status
git-ai-sync status

# Configure settings
git-ai-sync config --interval 60 --model claude-sonnet-4-5
```

## Configuration

The tool reads configuration from (in order of priority):

1. Command line arguments
2. Environment variables (`GIT_AI_SYNC_*`)
3. Local config file (`.git-ai-sync.toml` in repo root)
4. Global config file (`~/.config/git-ai-sync/config.toml`)

### Environment Variables

- `ANTHROPIC_API_KEY` - Required for AI conflict resolution
- `GIT_AI_SYNC_INTERVAL` - Override sync interval (default: 30)
- `GIT_AI_SYNC_MODEL` - Override Claude model (default: claude-sonnet-4-5)

## Development

```bash
make install  # Install dependencies
make test     # Run tests
make precommit  # Run all checks
```

## License

BSD-2-Clause
