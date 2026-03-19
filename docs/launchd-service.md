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

## 1. Create a launch agent

Example for a repository at `~/Documents/Obsidian/Personal`:

Create `~/Library/LaunchAgents/com.bborbe.git-ai-sync-obsidian.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.bborbe.git-ai-sync-obsidian</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/bborbe/.local/bin/git-ai-sync</string>
        <string>watch</string>
        <string>/Users/bborbe/Documents/Obsidian/Personal</string>
        <string>--interval</string>
        <string>30</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/git-ai-sync-obsidian.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/git-ai-sync-obsidian.log</string>
</dict>
</plist>
```

**Important:** Replace the binary path with the output of `command -v git-ai-sync`. Common locations:

- `~/.local/bin/git-ai-sync` (uv tool install)
- `/opt/homebrew/bin/git-ai-sync`

Load and start the service:

```bash
launchctl load ~/Library/LaunchAgents/com.bborbe.git-ai-sync-obsidian.plist
```

Check status:

```bash
launchctl list | grep git-ai-sync
```

## 2. Repeat for each repository

Create one plist per repository.

Example plist names:

- `com.bborbe.git-ai-sync-obsidian.plist`
- `com.bborbe.git-ai-sync-vault.plist`
- `com.bborbe.git-ai-sync-family.plist`

Change the `Label` and repository path in each plist.

## 3. Manage the service

Stop:

```bash
launchctl unload ~/Library/LaunchAgents/com.bborbe.git-ai-sync-obsidian.plist
```

Restart (stop + start):

```bash
launchctl unload ~/Library/LaunchAgents/com.bborbe.git-ai-sync-obsidian.plist
launchctl load ~/Library/LaunchAgents/com.bborbe.git-ai-sync-obsidian.plist
```

## 4. Verify the watcher is running

Check launchd status:

```bash
launchctl list | grep git-ai-sync
```

A running service shows `0` or `-` in the status column. A non-zero exit code indicates a problem.

Check the process list:

```bash
ps -ef | grep git-ai-sync | grep -v grep
```

Check logs:

```bash
tail -f /tmp/git-ai-sync-obsidian.log
```

## 5. One-shot sync is different from the service

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

### Plist exists but service is not loaded

Run:

```bash
launchctl load ~/Library/LaunchAgents/<plist-name>
```

### You upgraded git-ai-sync but the old watcher is still running

Restart the service:

```bash
launchctl unload ~/Library/LaunchAgents/<plist-name>
launchctl load ~/Library/LaunchAgents/<plist-name>
```

### Service keeps restarting (exit code 1 in `launchctl list`)

Check the log file for errors:

```bash
cat /tmp/git-ai-sync-obsidian.log
```

Common causes:
- wrong binary path in the plist
- repository path does not exist
- no git remote configured

## Related

- `README.md`
- `docs/systemd-user-service.md`
- `git-ai-sync doctor`
