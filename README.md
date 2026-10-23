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
# Install a per-vault launch agent (writes the plist, registers, starts the watcher)
git-ai-sync setup-launchd ~/Documents/Obsidian/Personal

# Tear it down again
git-ai-sync remove-launchd ~/Documents/Obsidian/Personal
```

See [`docs/launchd-service.md`](docs/launchd-service.md) for the full guide and the plist shape `setup-launchd` produces.

## How It Works

1. **Watch** - Polls repository at regular intervals
2. **Debounce** - Waits for the vault to go quiet before syncing, halving the required quiet window on every consecutive skip and forcing a sync once the maximum wait elapses
3. **Sync** - Stages, commits, pulls with rebase, pushes
4. **Resolve** - On conflicts, invokes Claude to intelligently merge

## Debounce

The watcher does not sync while you are actively editing. Each cycle it checks how long ago files last changed, and skips the sync when that is more recent than the required quiet window. The window starts at the poll interval and halves on every consecutive skipped cycle, so a vault written continuously still decays toward a window short enough to become eligible to sync. A successful sync resets the window to the full interval and restarts the wait clock.

That decay alone would still let a busy vault go a long time without a sync, so a hard bound sits on top of it: `GIT_AI_SYNC_MAX_WAIT` forces a sync once that many seconds have passed since the last successful sync, regardless of how recently files changed. The worst-case delay on a continuously written vault is therefore the maximum wait rounded up to the next poll boundary — at most `max_wait + interval` seconds.

A vault that goes quiet between cycles is unaffected: once the quiet window has passed, the cycle syncs exactly as it did before this behaviour existed. The two mechanisms are distinguishable in the log — a skipped cycle reports the quiet window currently in force, while a cycle forced by the bound says so explicitly.

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
| `GIT_AI_SYNC_LOG_FILE` | Log file path for the rotating file handler (5 MB x 5 backups) | unset (stderr only) |
| `GIT_AI_SYNC_MAX_WAIT` | Maximum seconds without a successful watch sync before one is forced regardless of how recently files changed | `300` |
| `GIT_AI_SYNC_STALL_THRESHOLD` | Consecutive failed watch cycles before one attention card is posted (must be >= 2) | `10` |
| `GIT_AI_SYNC_ATTENTION_SCRIPT` | Path to the supervisor's `attention-ask.py` posting script | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/attention-ask.py` |
| `ATTENTION_STORE_URL` | Attention store base URL (no `GIT_AI_SYNC_` prefix — the same variable the posting script reads) | `http://localhost:18080` |

`ANTHROPIC_API_KEY` is only required for conflict resolution (uses Claude Code auth otherwise).

## Metrics

`git-ai-sync watch` pushes two per-vault metrics to the configured Prometheus pushgateway:

- `git_ai_sync_heartbeat_timestamp{vault="<name>"}` — pushed on every watch cycle, regardless of sync outcome
- `git_ai_sync_last_success_timestamp{vault="<name>"}` — pushed after each successful sync

A fresh heartbeat with a stale last-success means the vault has stopped landing commits while the watcher is alive — alertable via age-of-last-success. Laptop-asleep periods stay silent because both series go stale together.

Credentials come from `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` / `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` and are never logged. Without `GIT_AI_SYNC_PUSHGATEWAY_URL` the feature is disabled and makes no network calls; failed pushes are logged and never break the sync loop.

**Caveat:** exactly ONE watcher per vault is required — two watchers pushing the same vault overwrite each other's series and can mask a wedge.

## Stall Alert

A watch loop that keeps failing while the process stays alive — an unreachable remote, a refused commit — is invisible to liveness checks: the watcher is up, but no commit lands. When `GIT_AI_SYNC_STALL_THRESHOLD` consecutive watch cycles have failed, `git-ai-sync` posts one operator card on the attention board so the stall is visible instead of silent.

Exactly one card is posted per stall, and it is closed automatically on the next successful cycle; a later stall posts a fresh card. A single isolated failure stays silent, and a debounce skip neither counts as a failure nor resets the counter.

The notifier is best-effort: a missing script, a non-zero exit, a timeout, or an unreachable store is logged as a warning and never breaks or delays a sync cycle.

The notifier is **disabled when the script is absent**. There is no enable/disable flag — a missing script path is the disable path. One startup warning names the missing path, and the sync loop then behaves exactly as it does without the feature.

The alert is `watch`-mode only: `git-ai-sync sync`, `status`, and `doctor` are unaffected by the threshold.

The store protocol the alert depends on is documented in [`docs/attention-store.md`](docs/attention-store.md).

## Troubleshooting

**Watch mode shows no activity** — Verify the path is a git repository with a configured remote.

**Conflict resolution fails** — Ensure Claude Code is authenticated (`claude login`). Run `git-ai-sync doctor` to diagnose.

**Changes not syncing** — Debounce may be active while files changed recently, but it cannot defer a sync beyond the maximum wait (`GIT_AI_SYNC_MAX_WAIT`, default 300 s). Run `git-ai-sync sync` to force a one-time sync.

## Requirements

- Git configured with a remote
- [uv](https://docs.astral.sh/uv/) — `curl -LsSf https://astral.sh/uv/install.sh | sh`
- Claude Code CLI — `npm install -g @anthropic-ai/claude-code`

## License

[BSD-2-Clause](LICENSE)
