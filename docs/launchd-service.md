# Run git-ai-sync as a macOS launchd service

Use this setup when you want `git-ai-sync watch ...` to stay running in the background across logins and reboots on macOS.

## Why use a launchd service?

`git-ai-sync watch` is intended to run continuously. A launchd user agent gives you:

- automatic startup after login
- automatic restart if the watcher exits
- one service per repository
- easier status and log inspection

## Prerequisites

Install the tool first:

```bash
uv tool install git+https://github.com/bborbe/git-ai-sync
```

Or upgrade an existing install:

```bash
uv tool upgrade git-ai-sync
```

Verify the binary exists and note the path:

```bash
command -v git-ai-sync
```

## 1. Create a per-vault launch agent

The `setup-launchd` subcommand writes the plist and registers the agent for you — there is no hand-written plist. Example for a repository at `~/Documents/Obsidian/Personal`:

```bash
git-ai-sync setup-launchd ~/Documents/Obsidian/Personal
```

This generates `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-<label>.plist` (label derivation below), bootstraps the job into your GUI launchd domain (`gui/$(id -u)`), and kickstarts the watcher.

Tear the agent down with the `remove-launchd` subcommand:

```bash
git-ai-sync remove-launchd ~/Documents/Obsidian/Personal
```

`remove-launchd` boots the job out of launchd and deletes the plist.

### Idempotence

Both commands are safe to re-run:

- re-running `setup-launchd` rewrites a byte-identical plist and tolerates an already-loaded job (no error)
- `remove-launchd` on a directory that was never set up exits 0 — there is nothing to remove, and removal is best-effort (a failed `launchctl bootout` is logged as a warning, never fatal)

### Label derivation

The label is the last path component of the vault directory, lowercased, with every run of non-alphanumeric characters collapsed to a single hyphen (leading/trailing hyphens stripped):

- `~/Documents/Obsidian/Personal` → `personal`
- `~/Documents/Obsidian/My Vault` → `my-vault`
- `~/Notes/Work-Notes` → `work-notes`

So for a vault whose last component is `Personal` you can predict:

- plist: `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-personal.plist`
- log: `/tmp/git-ai-sync-personal.log`

### The plist shape `setup-launchd` produces

The command always generates the same frozen plist — this is the contract, and there are no tunable knobs:

- **Label**: `com.github.bborbe.git-ai-sync-<label>`
- **ProgramArguments**: the resolved `git-ai-sync` binary path, then `watch <vault-dir> --interval 30 --strategy merge` (interval and strategy are fixed by design)
- **KeepAlive**: true — launchd restarts the watcher if it exits
- **RunAtLoad**: true — starts immediately after registration and at login
- **StandardOutPath / StandardErrorPath**: `/tmp/git-ai-sync-<label>.log`
- **SoftResourceLimits**: `NumberOfFiles` 1024, `ResidentSetSize` 536870912 (512 MB)
- **HardResourceLimits**: `NumberOfFiles` 2048, `ResidentSetSize` 1073741824 (1 GB)
- **EnvironmentVariables**: `GIT_AI_SYNC_PUSHGATEWAY_URL`, `GIT_AI_SYNC_PUSHGATEWAY_USERNAME`, `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` — each is passed through only if it was set when `setup-launchd` ran

## 2. Repeat for each repository

Create one launch agent per repository — run `git-ai-sync setup-launchd <vault-dir>` once per vault. Each vault gets its own label, plist, and log file.

## 3. Verify the watcher is running

Check launchd status:

```bash
launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>
```

Check the process list:

```bash
ps -ef | grep git-ai-sync | grep -v grep
```

Check logs:

```bash
tail -f /tmp/git-ai-sync-<label>.log
```

## 4. One-shot sync is different from the service

This command runs a single sync and exits:

```bash
git-ai-sync sync /path/to/repo
```

That is useful for testing, but it does **not** replace the long-running watcher service.

## Troubleshooting

### Service starts but nothing syncs

- verify the watched directory is a git repository
- verify the repository has a configured remote
- run `git-ai-sync doctor`
- run `git-ai-sync sync /path/to/repo` once to test basic sync behavior

### Setup fails in a sandboxed or SSH context

In contexts where the launchd GUI domain cannot be managed (for example an SSH session or a sandbox without a GUI login), `setup-launchd` exits non-zero after writing the plist and prints the exact manual commands to run:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-<label>.plist
launchctl kickstart -k gui/$(id -u)/<label>
```

A plist left on disk by a failed setup is inert; `git-ai-sync remove-launchd <vault-dir>` cleans it up.

### You upgraded git-ai-sync but the old watcher is still running

Regenerate the agent so the plist points at the current binary:

```bash
git-ai-sync remove-launchd <vault-dir>
git-ai-sync setup-launchd <vault-dir>
```

### Service keeps restarting (exit code 1 in `launchctl list`)

Check the log file for errors:

```bash
cat /tmp/git-ai-sync-<label>.log
```

Common causes:
- wrong binary path in the plist — the binary was moved or renamed after `setup-launchd`, so the plist still points at the old path; regenerate the agent
- repository path does not exist
- no git remote configured

## Related

- `README.md`
- `docs/systemd-user-service.md`
- `git-ai-sync doctor`
