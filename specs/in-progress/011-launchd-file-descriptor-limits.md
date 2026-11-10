---
status: approved
approved: "2026-11-10T13:02:22Z"
branch: dark-factory/launchd-file-descriptor-limits
---

## Summary

- `install` renders `NumberOfFiles` 4096 soft / 8192 hard into the launchd plist
- The supervisor watches twelve Obsidian vaults holding roughly 65,000 files, and the change tracker holds a file descriptor per watched path on macOS
- At 4096 every vault loop died with `too many open files` and nothing synced; the cutover only worked after the installed plist was hand-raised
- The renderer must emit limits that fit the workload, and the number must be justified rather than copied from a baseline written for ordinary services

## Problem

Spec 010 required the plist to meet launchd baseline rule 2 — `NumberOfFiles` 4096. That rule comes from a guide written after a kernel panic caused by an unbounded crash-looping service, and 4096 is a sensible floor for a process that holds a handful of sockets and log handles.

`git-ai-sync supervise` is not that process. It watches twelve vaults with **64,945 files** between them (measured 2026-11-10). `internal/changetracker` uses `fsnotify`, which on macOS backs each watched path with a descriptor, so the supervisor needs tens of thousands of them. Under the rendered 4096/8192 every one of the twelve loops failed at startup with `too many open files`; the supervisor stayed up and logged the failure once per interval, and no vault synced at all. The cutover was dead on arrival and the failure was invisible from `launchctl list` — the agent read `running` throughout.

Raising the installed plist by hand to 65536 soft / 245760 hard brought all twelve loops up with zero errors. That fix lives only in `~/Library/LaunchAgents/`, so the next `install` overwrites it and silently reintroduces the outage.

## Goal

`git-ai-sync install` renders a plist whose file-descriptor limits fit the twelve-vault workload, a test pins both values so a future edit cannot quietly lower them, and the documented plist shape states the same numbers with the reason.

## Non-goals

- Changing the watcher from `fsnotify` to a polling tracker. A poll-based tracker would drop the per-file descriptor cost entirely and is the better long-term answer, but it changes the change-detection mechanism the quiet-window logic is built on and deserves its own spec.
- Any other plist key. `ThrottleInterval`, `ProcessType`, `PATH`, the log paths and the absence of `NumberOfProcesses` all stay as they are.
- Touching the twelve live agents, the config file, or the supervisor's behaviour.
- A per-install flag or config knob for the limit. The value is a property of the workload, not an operator preference, and a knob would invite a value too low to work.

## Acceptance Criteria

- [ ] A test `install` writes a plist whose soft limit is 65536 and hard limit is 245760 — evidence: `TestInstallWritesPlistAndBootstraps` parses the plist it writes and asserts `<integer>65536</integer>` and `<integer>245760</integer>`; `go test ./internal/cli -run TestInstallWritesPlistAndBootstraps -count=1` exits 0.
- [ ] The renderer's shape test pins both values — evidence: `go test ./internal/launchd -run TestRenderPlistShape -count=1` exits 0 and its body asserts both numbers; reverting either constant makes that test fail rather than pass silently.
- [ ] The documented shape carries the same numbers — evidence: `grep -cE 'NumberOfFiles`? ?(65536|245760)' docs/launchd-service.md` returns 2, and `grep -cE 'NumberOfFiles`? ?(4096|8192)' docs/launchd-service.md` returns 0. The grep is scoped to the value position on purpose: the doc still has to be able to say that the baseline's 4096 is a floor for ordinary services.
- [ ] The document records why the number changed — evidence: `grep -c 'fsnotify' docs/launchd-service.md` ≥ 1 and `grep -c '64,945\|64945' docs/launchd-service.md` ≥ 1, so the next reader sees the workload figure that justified the raise.
- [ ] Nothing else in the rendered plist changes — evidence: `TestInstallIsIdempotent` still passes, and the rendered plist contains no `NumberOfProcesses` key.
- [ ] **Post-Deploy (Rung-2):** a real `install` produces an agent that starts all twelve loops — evidence: `grep -c 'Starting watch mode' ~/Library/Logs/git-ai-sync/git-ai-sync.log` reaches 12 after a fresh `install`, and `grep -c 'too many open files'` over the same window is 0.
  - `deploy_check:` `plutil -extract SoftResourceLimits.NumberOfFiles raw ~/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist`
  - `deploy_target:` `65536`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit-go` — must exit 0. **Not `make precommit`**: that target is Python-only (`uv sync`, `ruff`, `pytest`, `mypy`) and never compiles a Go file. `precommit-go` is `gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`.
- `go test ./internal/launchd ./internal/cli -count=1` — the renderer shape test and the lifecycle install test, both of which assert the rendered numbers. `plutil` is macOS-only and is therefore not a container probe.

### Operator-executable (runs on the host after merge)

- `~/.local/bin/git-ai-sync install` after the release, then `plutil -extract SoftResourceLimits.NumberOfFiles raw ~/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist` prints `65536`, `grep -c 'Starting watch mode' ~/Library/Logs/git-ai-sync/git-ai-sync.log` reaches 12, and `grep -c 'too many open files'` over the same window is 0.

## Desired Behavior

1. The rendered plist carries `SoftResourceLimits` → `NumberOfFiles` 65536 and `HardResourceLimits` → `NumberOfFiles` 245760.
2. The renderer's shape test asserts both values, so a later edit that lowers them fails a test rather than an outage.
3. `TestInstallWritesPlistAndBootstraps` asserts the same two values on the plist a real `install` writes, so the renderer and the install path cannot drift apart.
4. `docs/launchd-service.md` states the same numbers and records why: the change tracker holds a descriptor per watched path on macOS, the twelve vaults hold 64,945 files, and the baseline's 4096 is a floor for ordinary services rather than a fit for this one.

## Constraints

- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`, `internal/cli/lifecycle_test.go`, `docs/launchd-service.md` and `CHANGELOG.md`. ⚠️ `internal/cli/lifecycle_test.go` is in this list because it also asserts the two literals — leaving it out would make the spec's own `make precommit-go` gate fail.
- No change to `internal/supervise`, `internal/watch`, `internal/changetracker`, the config schema, or any other plist key.
- The hard limit must stay at or below `kern.maxfilesperproc` (245760 on this host) — a plist value above it is silently clamped by launchd and would read as a fix that did nothing.
- `CHANGELOG.md` currently has no `## Unreleased` section; create one above `## v0.18.0` and add the bullet there.
- Nothing may touch `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit under `.github/` is rejected at push.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| The host's `kern.maxfilesperproc` is lower than 245760 | launchd clamps the hard limit to the host value; the soft limit still governs what the process may open | Keep the soft limit at 65536 and lower only the hard limit to the host value; the soft limit is the one the loops need | `launchctl print gui/$UID/com.github.bborbe.git-ai-sync \| grep maxfiles` | Reversible |
| The vault set grows past the soft limit | Loops fail with `too many open files` exactly as before | Raise the constant and re-release; this spec makes that a one-line change | `grep -c 'too many open files' <log>` climbing | Reversible |
| A future edit lowers the constants | Both shape tests fail in CI | Restore the values | `go test ./internal/launchd ./internal/cli` | Reversible |
| An operator hand-edits the installed plist instead of re-releasing | The next `install` overwrites it and the outage returns | Re-release and re-install; the rendered value is now correct | `plutil -extract SoftResourceLimits.NumberOfFiles raw` on the installed plist | Reversible |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Renderer constants plus both tests — `internal/launchd/launchd.go`, its shape test, and the two literals in `internal/cli/lifecycle_test.go` | 1, 2, 3 | 1, 2, 5 | — |
| 2 | `docs/launchd-service.md` plist-shape numbers with the fsnotify rationale and the 64,945 figure, plus the `## Unreleased` CHANGELOG bullet | 4 | 3, 4 | — |
| 3 | Operator verification after release: fresh `install`, twelve `Starting watch mode` lines, zero `too many open files` | — | 6 | 1, 2 |

## Do-Nothing Option

Every `git-ai-sync install` re-renders 4096/8192 and silently kills the supervisor's ability to watch anything. The cutover task cannot pass its own end-to-end verification, and the next person to reinstall — or the next reboot that reloads the agent from the shipped plist — takes every vault offline with an agent that reports `running`.
