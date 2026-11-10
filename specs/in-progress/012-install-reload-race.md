---
status: prompted
approved: "2026-11-10T13:41:01Z"
generating: "2026-11-10T14:23:51Z"
prompted: "2026-11-10T14:38:37Z"
branch: dark-factory/install-reload-race
---

## Summary

- `git-ai-sync install` on an already-loaded agent exits **1** and leaves the agent **unloaded**
- It runs `launchctl bootout` and then `launchctl bootstrap` with no wait; launchd has not finished tearing the job down, so bootstrap fails with `Input/output error` — and the agent is now stopped
- `git-ai-sync status` prints an int64 duration overflow instead of `never` between a restart and the first metrics push
- Both live on the same command surface and both misreport the agent's state: one says the install failed and leaves nothing running, the other says the last push was 2.9 million hours ago
- The install race is the expensive one — it turns every reinstall and every rollback step into an outage

## Problem

`Install` writes the plist, runs `bootout`, and immediately runs `bootstrap`. On macOS the bootout returns before the job is gone, so the bootstrap races the teardown and fails with `Bootstrap failed: 5: Input/output error`. Because the plist was already rewritten and the old job was already booted out, the end state is an agent that is **not running** — an outage produced by the command whose job is to install it.

Reproduced twice on 2026-11-10 during the vault cutover: once on the first `install` after a binary swap, once on a plain repeat `install` against a healthy agent. Both times the recovery was a manual `launchctl bootstrap gui/$(id -u) <plist>`, and both times `launchctl list` showed the label absent afterwards.

The same shape appears in `status`: `time.Since(state.LastMetricsPush)` is computed without checking for the zero value, so a state file that exists but records no push yet yields a duration near the int64 maximum. Observed immediately after a reload — `last metrics push: 2562047h47m16.854775807s ago` — where the documented behaviour is `never`. It self-corrects on the next push, but a script that greps for `never` reads the overflow as a live value.

## Goal

`install` is idempotent against a loaded agent: it waits for the booted-out job to disappear before bootstrapping, retries a bootstrap that still races, and ends with exactly one label carrying a PID. `status` prints `never` whenever there is no recorded push, whether the state file is absent or merely empty.

## Non-goals

- Changing the plist contents, the config schema, or the supervisor's behaviour.
- Making `install` restart a job that is already running and unchanged. It may still bootout and bootstrap; the requirement is that it converges, not that it is a no-op.
- Changing the manual-recovery message. A bootstrap that fails after the wait and the retry must still print the exact `launchctl bootstrap` command.
- The `NumberOfFiles` values, which spec 011 owns.

## Acceptance Criteria

- [ ] `install` against an already-loaded agent polls until the job is gone, then bootstraps — evidence: a Go test drives `Install` with a `launchctl` stub on `PATH` that records every argv and makes `print gui/<uid>/com.github.bborbe.git-ai-sync` exit non-zero from its third invocation onward. `Install` returns nil, and the recorded argv shows `bootout`, then **at least three** `print` calls, then `bootstrap` — so the test fails an implementation that discards a single `print` and sleeps.
- [ ] `uninstall` followed immediately by `install` converges — evidence: the same test runs `Uninstall` then `Install` against the stub; both return nil and the recorded argv is `bootout, print…, bootstrap` for the install leg.
- [ ] A bootstrap that fails once and succeeds on the retry does not fail the command — evidence: the stub makes the first `bootstrap` exit 5 with `Input/output error` and the second exit 0; `Install` returns nil and the recorded argv contains two `bootstrap` calls.
- [ ] A bootstrap that fails every time still fails loudly and names the manual command — evidence: with the stub failing every `bootstrap`, `Install` returns a non-nil error whose message contains `launchctl bootstrap gui/`.
- [ ] The wait is bounded, and the bound is testable without a real 15-second sleep — evidence: the bound is injectable on `Options` for tests only; with a stub whose `print` never exits non-zero and whose `bootstrap` always fails, `Install` returns a non-nil error within the injected bound (the test injects 200 ms and asserts elapsed time under 2 s).
- [ ] `status` prints `never` when the state file exists but records no push — evidence: a test writes `{"last_metrics_push":"0001-01-01T00:00:00Z","vaults":{}}` and asserts the output line is exactly `last metrics push: never`.
- [ ] `status` still prints a sane age for a real timestamp — evidence: the existing freshness test keeps passing, and its printed age parses as a positive duration under 60 s.
- [ ] `docs/launchd-service.md` describes the converging reload — evidence: `grep -c 'bootout' docs/launchd-service.md` ≥ 1 and `grep -c 'waits\|polls' docs/launchd-service.md` ≥ 1, so the doc no longer says only "boots the job out, then bootstraps it".
- [ ] **Post-Deploy (Rung-2):** a real `install` against the live agent exits 0 and leaves exactly one label running — evidence: `~/.local/bin/git-ai-sync install; echo rc=$?` prints `rc=0`, `launchctl list | grep -c com.github.bborbe.git-ai-sync` prints `1`, and the label's PID column is numeric.
  - `deploy_check:` `~/.local/bin/git-ai-sync --version`
  - `deploy_target:` `git-ai-sync $(git -C /Users/bborbe/Documents/workspaces/git-ai-sync describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit-go` — must exit 0. **Not `make precommit`**: that target is Python-only and never compiles a Go file.
- `go test ./internal/launchd ./internal/cli -count=1` — the reload tests and the status test.

### Operator-executable (runs on the host after merge)

- `~/.local/bin/git-ai-sync install` against the live loaded agent, then `echo rc=$?` is `0` and `launchctl list | grep git-ai-sync` shows one label with a numeric PID.

## Desired Behavior

1. `Install` boots the job out, then polls `launchctl print gui/<uid>/<label>` every 100 ms until it reports the job absent, with a bounded total wait (default 15 s, injectable for tests).
2. If the bootstrap still fails, `Install` polls for absence once more — the failed bootstrap may itself have registered nothing — and retries the bootstrap once.
3. If the bootstrap fails on both attempts, `Install` returns an error naming the manual `launchctl bootstrap` command, as it does today.
4. The total wait never exceeds the bound; a job that never disappears fails the command rather than hanging it.
5. `status` prints `never` when the recorded push time is zero, and a real age otherwise. The existing clamp that prevents a negative age is left as it is.
6. `uninstall` followed immediately by `install` converges the same way.

## Constraints

- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`, `internal/cli/cli.go`, `internal/cli/lifecycle_test.go`, `docs/launchd-service.md` and `CHANGELOG.md`. The doc is in the list because it currently states the old two-step flow at lines 69 and 180.
- The `launchctl` invocations stay injectable through the existing seam, so the tests never touch the real launchd domain.
- The wait uses the caller's context; a cancelled context returns promptly rather than sleeping out the bound.
- The injected bound exists for tests only and is documented as such — it is not a config key and must not appear in the YAML schema.
- The plist written by `Install` is unchanged — same bytes, same keys, same values.
- Add a `CHANGELOG.md` bullet under `## Unreleased`.
- Nothing may touch `.github/workflows/`.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| The job never disappears after bootout | `Install` fails after the bound, naming the manual command | Operator runs the printed `launchctl bootstrap`; the plist is already correct | The command's exit code and message | Reversible |
| Bootstrap fails twice | `Install` fails, naming the manual command | Same as above | The command's exit code | Reversible |
| The process is killed between `bootout` and `bootstrap` | The agent is left down with a correct plist on disk | Re-run `install`, which converges; or run the printed bootstrap command | `launchctl list` shows no label while the plist exists | Reversible |
| The agent is already running and healthy | Bootout, poll, bootstrap — a brief stop, then running again | None needed | `launchctl list` shows a new PID | Reversible |
| A state file records a zero push time | `status` prints `never` | None needed | `status` output | Reversible |
| A clock jumps backwards between push and read | The existing clamp keeps the age at zero rather than negative — unchanged by this spec | None needed; the next cycle rewrites the timestamp | `status` output | Reversible |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `internal/launchd` reload: bounded poll for teardown, one bootstrap retry, injectable bound, tests modelling the race, the always-failing case and the uninstall→install sequence | 1, 2, 3, 4, 6 | 1, 2, 3, 4, 5 | — |
| 2 | `internal/cli` status: `never` for a zero timestamp, plus its test | 5 | 6, 7 | — |
| 3 | `docs/launchd-service.md` reload description and the `## Unreleased` CHANGELOG bullet | — | 8 | 1 |
| 4 | Operator verification after release | — | 9 | 1, 2 |

## Do-Nothing Option

`git-ai-sync install` keeps exiting 1 and leaving the agent down whenever the job is already loaded — which is every reinstall after the first, and every `uninstall`/`install` cycle in a rollback. The failure looks like a launchd quirk rather than a defect, so the natural response is to re-run it, which fails again, while the vaults stop syncing and `launchctl list` shows nothing to explain why.
