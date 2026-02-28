# git-ai-sync

Automatic Git repository sync with AI-powered conflict resolution.

## Install

```bash
uv tool install git+https://github.com/bborbe/git-ai-sync
```

## Upgrade

```bash
uv tool upgrade git-ai-sync
```

## Quick Start

```bash
# Start watching and syncing (default: current directory, 30s interval)
git-ai-sync watch

# Watch specific directory with custom interval
git-ai-sync watch /path/to/repo --interval 60

# Run sync once
git-ai-sync sync /path/to/repo

# Resolve conflicts
git-ai-sync resolve

# Show status
git-ai-sync status
```

## Verify Setup

```bash
git-ai-sync doctor
```

Checks Claude Code CLI, Node.js, Git, and active session.

## How It Works

1. **Watch** - Polls repository at regular intervals
2. **Debounce** - Skips sync when files changed recently (active editing)
3. **Sync** - Stages, commits, pulls with rebase, pushes
4. **Resolve** - On conflicts, invokes Claude to intelligently merge

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `GIT_AI_SYNC_INTERVAL` | Sync interval in seconds | `30` |
| `GIT_AI_SYNC_MODEL` | Claude model | `claude-sonnet-4-5-20250929` |
| `GIT_AI_SYNC_COMMIT_PREFIX` | Commit message prefix | `auto` |

`ANTHROPIC_API_KEY` is only required for conflict resolution (uses Claude Code auth otherwise).

## Troubleshooting

**Watch mode shows no activity** — Verify the path is a git repository with a configured remote.

**Conflict resolution fails** — Ensure Claude Code is authenticated (`claude login`). Run `git-ai-sync doctor` to diagnose.

**Changes not syncing** — Debounce may be active (files changed within the interval). Run `git-ai-sync sync` to force a one-time sync.

## Requirements

- Git configured with a remote
- [uv](https://docs.astral.sh/uv/) — `curl -LsSf https://astral.sh/uv/install.sh | sh`
- Claude Code CLI — `npm install -g @anthropic-ai/claude-code`

## License

[BSD-2-Clause](LICENSE)
