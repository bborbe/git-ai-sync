# Run the git-ai-sync supervisor as a macOS launchd service

One launchd user agent runs `git-ai-sync supervise --config <path>` for every
configured vault. The agent is named `com.github.bborbe.git-ai-sync`, and the
vault set lives in one `0600` config file instead of one plist per vault.

## Why one agent?

`git-ai-sync supervise` runs one sync loop per configured vault inside a single
process. A launchd user agent gives you:

- automatic startup after login
- automatic restart if the supervisor exits
- one label, one plist, one log file
- one config file to edit when the vault set or the cadence changes

## Prerequisites

Install the binary on `PATH` (the operator installs `/usr/local/bin/git-ai-sync`
with their own credentials) and verify it:

```bash
command -v git-ai-sync
git-ai-sync --version
```

Write the config file at `~/.config/git-ai-sync/config.yaml` with mode `0600`.
It holds the pushgateway password, so it must not be group- or world-readable:

```bash
chmod 0600 ~/.config/git-ai-sync/config.yaml
```

The file names the absolute vault paths and the shared settings:

```yaml
vaults:
  - /Users/me/Documents/Obsidian/Personal
  - /Users/me/Documents/Obsidian/Work
interval: 30
max_wait: 300
strategy: merge
commit_prefix: auto
log_level: INFO
stall_threshold: 10
attention_script: /Users/me/bin/attention-ask.py
attention_store_url: http://localhost:18080
state_file: /Users/me/.local/state/git-ai-sync/status.json
pushgateway:
  url: https://pushgateway.example.com
  username: git-ai-sync
  password: <secret>
```

`install` refuses a config file any other user can read, naming the permission
problem; `0600` proceeds.

## Install the agent

```bash
git-ai-sync install
```

`install`:

1. checks the config file mode and refuses a group- or world-accessible file;
2. creates `~/Library/Logs/git-ai-sync/`;
3. writes `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist`;
4. boots the job out with `launchctl bootout`, then polls
   `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync` every 100 ms
   until launchd reports the job gone (bounded to 15 s), and then bootstraps it
   into `gui/$(id -u)`.

It is idempotent: a second `install` writes a byte-identical plist and exits 0,
so it is also the way to reload after a config change or a binary upgrade.

The reload converges even when the old job is still loaded. If the bootstrap
still races the teardown, `install` polls once more and retries the bootstrap
exactly once. If the job is still present after the bounded wait, or the
bootstrap fails twice, `install` exits non-zero and prints the exact manual
`launchctl bootstrap` command to run.

### The plist `install` produces

The plist is a frozen contract with no tunable knobs:

- **Label**: `com.github.bborbe.git-ai-sync`
- **ProgramArguments**: the launch path — the `git-ai-sync` binary path the
  `install` process was launched from, recorded verbatim — then
  `supervise --config <config path>`
- **KeepAlive**: `true` — launchd restarts the supervisor if it exits
- **RunAtLoad**: `true` — starts immediately after registration and at login
- **ThrottleInterval**: `30` seconds, so a crash loop cannot relaunch every ten
  seconds
- **ProcessType**: `Standard`, the value for a long-running daemon
- **SoftResourceLimits**: `NumberOfFiles` 65536
- **HardResourceLimits**: `NumberOfFiles` 245760
- **EnvironmentVariables**: an explicit `PATH` only
  (`/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin`), because
  launchd's default `PATH` does not include `/usr/local/bin`
- **StandardOutPath / StandardErrorPath**:
  `~/Library/Logs/git-ai-sync/git-ai-sync.log`
- **No credentials**: the plist carries no pushgateway URL, username or password.
  Everything secret stays in the `0600` config file.

The first `ProgramArguments` entry is the **launch path**: the absolute path
the `install` process was launched from, recorded verbatim, not the target a
package-manager link points at. A Homebrew cask installs a stable link such
as `/opt/homebrew/bin/git-ai-sync` and repoints it on every upgrade; recording
the link means the plist survives the upgrade, while recording the link's
versioned target would leave the agent naming a directory the upgrade
deletes. A plain install that is not a link records its own path unchanged.

`NumberOfFiles` is sized for the workload, not for the launchd baseline. The
change tracker (`internal/changetracker`) is built on `fsnotify`, which on macOS
backs each watched path with a file descriptor; the twelve Obsidian vaults hold
64,945 files between them (measured 2026-11-10), so the supervisor needs tens of
thousands of descriptors. At 4096 every vault loop failed at startup with
`too many open files` while `launchctl list` still reported the agent as
`running`, so nothing synced and the failure was invisible from the status
output. The launchd baseline's 4096-file rule is a floor written for ordinary
services that hold a handful of sockets and log handles, not a fit for this
process.

The soft limit is the one that governs what the process may open; the hard
limit must stay at or below `kern.maxfilesperproc` (245760 on the reference
host), because launchd silently clamps a larger value. If the vault set grows
past the soft limit, raise the two literals in `internal/launchd/launchd.go`
and re-release — the next `install` overwrites a hand-edited copy of the
installed plist.

The plist deliberately sets no `NumberOfProcesses` and no `ResidentSetSize`
limit; the metrics heartbeat and the stall alert are the memory signal instead.

## Check status

```bash
git-ai-sync status
```

`status` reads the same config file and prints:

- one `vault:` line per configured vault;
- `last metrics push: <age> ago` from the supervisor's state file, or
  `last metrics push: never` before the first push;
- `agent: loaded` or `agent: not loaded`.

It exits non-zero when the agent is not loaded, so it can gate a scripted check.
A backwards clock clamps the reported age at zero instead of printing a negative
duration.

## Uninstall the agent

```bash
git-ai-sync uninstall
```

`uninstall` boots the job out and removes the plist. On a machine with no plist
it exits 0 without calling `launchctl`.

## Logs

The supervisor writes stdout and stderr to:

```bash
tail -f ~/Library/Logs/git-ai-sync/git-ai-sync.log
```

Logs live under `~/Library/Logs/git-ai-sync/`, never under a temporary directory
that a reboot wipes.

## Troubleshooting

### Setup fails in a sandboxed or SSH context

When the launchd GUI domain cannot be managed (for example an SSH session or a
sandbox without a GUI login), `install` exits non-zero after writing the plist
and prints the exact manual command to run:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist
```

A plist left on disk by a failed install is inert; `git-ai-sync uninstall`
cleans it up.

### The agent is loaded but nothing syncs

- verify the config file names absolute vault paths that exist;
- verify each vault is a git repository with a remote;
- run `git-ai-sync sync /path/to/repo` once to test the sync path;
- read `~/Library/Logs/git-ai-sync/git-ai-sync.log` for the per-vault failure
  line, which is logged once per interval.

### You upgraded the binary

An install through a stable link needs nothing: the plist names the link, and
the upgrade repoints the link at the new binary, so the agent starts the new
target on its next restart. If you replaced the binary at a different path, or
you want to reload the running job immediately, re-run `git-ai-sync install`.
It records the launch path, rewrites the plist, and reloads the job: it runs
`launchctl bootout`, polls until the job is gone, and then runs
`launchctl bootstrap`. A bootstrap that still races the teardown is retried
once; if it still fails, the command prints the exact manual
`launchctl bootstrap` line.

### The service keeps restarting

Check the log file for errors:

```bash
tail -100 ~/Library/Logs/git-ai-sync/git-ai-sync.log
```

Common causes are a moved binary, a missing vault path, or a repository with no
remote. Re-run `git-ai-sync install` after fixing the binary path.

## Related

- `README.md`
- `docs/systemd-user-service.md`
- `docs/attention-store.md`
