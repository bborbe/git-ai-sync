---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-03-20T14:00:20Z"
prompted: "2026-03-20T14:04:51Z"
branch: dark-factory/instance-locking
---

## Summary

- Prevent concurrent git-ai-sync runs on the same repository using OS-level file locking
- Lock acquired at command entry for watch, sync, and resolve commands
- Second instance fails immediately with clear PID-based error message
- Kernel auto-releases lock on crash -- no stale lock problem
- Lock file added to .gitignore

## Problem

Running two git-ai-sync instances on the same repo causes race conditions: both poll for changes, both try to commit or resolve conflicts simultaneously, producing corrupted state or conflicting git operations. Nothing currently prevents accidental double-launch (e.g., two terminals, a cron job plus manual run).

## Goal

Only one git-ai-sync instance operates on a given repository at a time. A second instance fails immediately with a clear error identifying the process that holds the lock. Crashes leave no stale state that blocks future runs.

## Non-goals

- No distributed locking (single machine only)
- No lock timeout or auto-expiry
- No graceful takeover (existing instance must exit or crash first)
- No per-command granularity (all three commands use the same lock)
- No support for network filesystems (NFS, SMB) -- flock semantics are local-only

## Desired Behavior

1. On startup of watch, sync, or resolve commands, acquire an exclusive non-blocking file lock on `.git-ai-sync.lock` in the repository root
2. Lock file contains the PID of the owning process (for debugging)
3. If the lock is already held, exit immediately with: "another instance is already running (pid NNNN)" and exit code 1
4. Lock is released on clean shutdown (normal exit, SIGINT, SIGTERM)
5. If the process crashes, the kernel releases the flock automatically -- a subsequent run acquires the lock even if the lock file still exists on disk

## Constraints

- Must use OS-level exclusive non-blocking file locking (not advisory file-presence checks)
- Lock file location is always the git repo root, not configurable
- Lock file permissions: 0600
- Locking behavior must be independently testable without invoking full command execution
- Must not interfere with the existing CLI argument parsing or error handling

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Lock already held by another instance | Exit 1 with PID of holder | Wait for other instance or kill it |
| Process crashes without cleanup | OS releases flock automatically | Next startup succeeds |
| Lock file exists on disk but no flock held | Lock acquisition succeeds (stale file) | None needed |
| Lock file directory is read-only | Startup fails with permission error | Fix directory permissions |
| Repository root cannot be determined | Startup fails with clear error | Run from within a git repo |

## Security / Abuse Cases

- Lock file is local-only, no network exposure
- PID written to file is informational; an attacker with local file access already has greater capabilities
- Lock file permissions 0600 prevent other users from reading the PID

## Acceptance Criteria

- [ ] First instance starts successfully and creates `.git-ai-sync.lock` with its PID
- [ ] Second instance on same repo fails immediately with exit code 1 and message containing the first instance's PID
- [ ] Lock is released on clean shutdown (exit, SIGINT, SIGTERM)
- [ ] Stale lock files (from crashed processes) do not block new instances
- [ ] All three commands (watch, sync, resolve) acquire the lock before executing
- [ ] `.git-ai-sync.lock` appears in `.gitignore`
- [ ] Tests exist that verify lock acquisition, contention rejection, and release after exit

## Verification

```
make precommit
```

## Do-Nothing Option

Hope users don't run two instances. In practice, this happens with cron + manual runs, multiple terminals, or restarting without checking. Consequences: duplicate commits, merge conflicts, corrupted sync state.
