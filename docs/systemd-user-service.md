# Run git-ai-sync as a user systemd service

Use this setup when you want `git-ai-sync watch ...` to stay running in the background across logins and reboots.

## Why use a user service?

`git-ai-sync watch` is intended to run continuously. A user systemd service gives you:

- automatic startup after boot/login
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

Verify the binary exists:

```bash
command -v git-ai-sync
```

## 1. Create a user service

Example for a repository at `~/vault`:

Create `~/.config/systemd/user/git-ai-sync-vault.service`:

```ini
[Unit]
Description=git-ai-sync for vault
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/git-ai-sync watch %h/vault --interval 30
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```

Reload systemd and enable the service:

```bash
systemctl --user daemon-reload
systemctl --user enable --now git-ai-sync-vault.service
```

Check status:

```bash
systemctl --user status git-ai-sync-vault.service --no-pager
```

## 2. Repeat for each repository

Create one service per repository.

Example service names:

- `git-ai-sync-vault.service`
- `git-ai-sync-obsidian.service`
- `git-ai-sync-family.service`

Example `ExecStart` lines:

```ini
ExecStart=%h/.local/bin/git-ai-sync watch %h/vault --interval 30
ExecStart=%h/.local/bin/git-ai-sync watch %h/obsidian-personal --interval 30
ExecStart=%h/.local/bin/git-ai-sync watch %h/obsidian-family --interval 30
```

## 3. If `systemctl --user` fails with “Failed to connect to bus: No medium found”

This usually means the current shell is missing the user DBus environment.

Export these variables in the shell before calling `systemctl --user`:

```bash
export XDG_RUNTIME_DIR=/run/user/$(id -u)
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
```

Then retry:

```bash
systemctl --user restart git-ai-sync-vault.service
```

To make this available in future shells, add the exports to `~/.bashrc`:

```bash
export XDG_RUNTIME_DIR=/run/user/$(id -u)
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
```

Reload your shell:

```bash
source ~/.bashrc
```

## 4. Keep services running after logout

On host-based installs, enable linger once as root:

```bash
sudo loginctl enable-linger $USER
```

Without linger, user services may stop when the user fully logs out.

## 5. Verify the watcher is really running

Check systemd status:

```bash
systemctl --user status git-ai-sync-vault.service --no-pager
```

Check the process list:

```bash
ps -ef | grep git-ai-sync | grep -v grep
```

You should see a `git-ai-sync watch ...` process for each configured repository.

## 6. One-shot sync is different from the service

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

### Service file exists but is not enabled

Run:

```bash
systemctl --user enable --now <service-name>
```

### You upgraded git-ai-sync but the old watcher is still running

Restart the user service:

```bash
systemctl --user restart <service-name>
```

## Related

- `README.md`
- `git-ai-sync doctor`
