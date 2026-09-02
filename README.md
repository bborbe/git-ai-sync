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

## Install from local checkout

```bash
uv tool install --force --reinstall .
```

## Quick Start

```bash
# Start watching and syncing (default: current directory, 30s interval, merge strategy)
git-ai-sync watch

# Watch specific directory with custom interval and rebase strategy
git-ai-sync watch /path/to/repo --interval 60 --strategy rebase

# Run sync once
git-ai-sync sync /path/to/repo

# Resolve conflicts
git-ai-sync resolve

# Show status
git-ai-sync status
```

The `--strategy` flag supports `merge` (default) or `rebase`, and can be set via the `GIT_AI_SYNC_STRATEGY` environment variable.

## Verify Setup

```bash
git-ai-sync doctor
```

Checks Claude Code CLI, Node.js, Git, and runs a live round-trip that prints the resolved model + base URL and asks the backend to self-identify. Use it after setting `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_MODEL` to confirm alt-provider routing (e.g. MiniMax) actually reaches the intended backend rather than silently falling back to `api.anthropic.com`.

Example with MiniMax:

```bash
ANTHROPIC_BASE_URL="https://api.minimax.io/anthropic" \
ANTHROPIC_AUTH_TOKEN="sk-..." \
ANTHROPIC_MODEL="MiniMax-M2.7" \
git-ai-sync doctor
```

## Run in the Background

For production-style usage, run one background service per repository so `git-ai-sync watch ...` stays active across sessions.

| Platform | Guide |
|----------|-------|
| Linux (systemd) | [`docs/systemd-user-service.md`](docs/systemd-user-service.md) |
| macOS (launchd) | [`docs/launchd-service.md`](docs/launchd-service.md) |

Quick example (systemd):

```bash
systemctl --user enable --now git-ai-sync-vault.service
```

Quick example (launchd):

```bash
launchctl load ~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-obsidian.plist
```

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
| `GIT_AI_SYNC_STRATEGY` | Pull strategy (merge or rebase) | `merge` |
| `GIT_AI_SYNC_PUSHGATEWAY_URL` | Prometheus pushgateway base URL (e.g. https://pushgateway.dev.nuke.benjamin-borbe.de) | unset (disabled) |
| `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` | Pushgateway basic-auth username | unset |
| `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` | Pushgateway basic-auth password | unset |

`ANTHROPIC_API_KEY` is only required for conflict resolution (uses Claude Code auth otherwise).

## Metrics

`git-ai-sync watch` pushes two per-vault metrics to the configured Prometheus pushgateway:

- `git_ai_sync_heartbeat_timestamp{vault="<name>"}` — pushed on every watch cycle, regardless of sync outcome
- `git_ai_sync_last_success_timestamp{vault="<name>"}` — pushed after each successful sync

A fresh heartbeat with a stale last-success means the vault has stopped landing commits while the watcher is alive — alertable via age-of-last-success. Laptop-asleep periods stay silent because both series go stale together.

Credentials come from `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` / `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` and are never logged. Without `GIT_AI_SYNC_PUSHGATEWAY_URL` the feature is disabled and makes no network calls; failed pushes are logged and never break the sync loop.

**Caveat:** exactly ONE watcher per vault is required — two watchers pushing the same vault overwrite each other's series and can mask a wedge.

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
