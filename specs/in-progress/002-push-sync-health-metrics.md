---
status: verifying
approved: "2026-09-02T19:48:42Z"
generating: "2026-09-02T19:55:54Z"
prompted: "2026-09-02T19:55:54Z"
verifying: "2026-09-02T21:38:06Z"
branch: dark-factory/push-sync-health-metrics
---

## Summary

- Push a heartbeat timestamp and a last-success timestamp per vault from `git-ai-sync watch` to the existing Prometheus pushgateway
- Alert on "heartbeat fresh AND last-success stale" — fires when a vault stops landing commits, stays silent while the laptop is asleep
- Laptop reaches the pushgateway through the existing traefik ingress (`pushgateway.{dev,prod}.nuke.benjamin-borbe.de`) behind the shared monitoring basic auth — in-cluster DNS does not resolve from the laptop
- Push failures are logged and never break the sync loop; the feature is disabled when no pushgateway is configured
- Root cause being fixed: the OpenClaw vault was wedged 9 days (12,093 consecutive failures) with zero detection — error counts cannot distinguish healthy (8,111 failures) from dead (12,093), age-of-last-success can

## Problem

`git-ai-sync watch` runs as a launchd (macOS) / systemd (Linux) service per vault. When a vault wedges (conflict-marker refusal, credentials expiry, any future cause), the service keeps running and looping — launchd reports it healthy, every liveness check passes, and nothing external notices. Observed 2026-08-21→30: the OpenClaw vault failed every 30s for 9 days before a human noticed the vault "felt stale." A subsequent 2026-08-30 loop showed the same failure mode recurs. The only guard that exists today is a `/watch` probe that covers "while I am at the keyboard" — not "while I am not."

## Goal

A wedged vault is noticed within an hour of its last successful sync, with an Alertmanager alert, regardless of why sync stopped landing commits — while remaining silent during normal laptop-asleep periods.

## Non-goals

- Auto-recovery of the wedge — owned by the sibling task (conflict-handling auto-recover); this spec only notices
- Deploying a new pushgateway — the existing `monitoring/pushgateway` on nuke prod + dev is reused
- Alerting for any service other than git-ai-sync
- Replacing launchd/systemd as the scheduler
- A general-purpose pushgateway abstraction beyond what git-ai-sync needs
- Pushing the alert rule itself (that is a nuke-repo config change applied by the operator)

## Acceptance Criteria

- [ ] `make precommit` exits 0
- [ ] Unit tests assert per-behavior: a call-count recorder over the push function records exactly one heartbeat push per watch cycle, records a `last_success` push only after a successful sync (and zero after a failed one), and records zero pushes when the pushgateway is unconfigured
- [ ] **Post-Deploy (Rung-2):** after `git-ai-sync watch <repo>` runs a few cycles against dev, `curl -u <monitoring creds> https://pushgateway.dev.nuke.benjamin-borbe.de/metrics` shows both series for the vault, with `git_ai_sync_last_success_timestamp` advancing after each successful sync.
  - `deploy_check:` `curl -s -o /dev/null -w '%{http_code}' -u "$MONITORING_USER:$MONITORING_PASS" https://pushgateway.dev.nuke.benjamin-borbe.de/`
  - `deploy_target:` `200`
- [ ] **Post-Deploy (Rung-3):** the alert rule (loaded in the nuke repo, applied by the operator) is visible via `amtool --alertmanager.url=http://localhost:9093 alert` as a configured rule after deploy to prod.
  - `deploy_check:` `kubectlnukeprod -n monitoring get alert git-ai-sync-sync-stalled -o jsonpath='{.metadata.name}'`
  - `deploy_target:` `git-ai-sync-sync-stalled`
- [ ] Negative: with no `GIT_AI_SYNC_PUSHGATEWAY_URL` set, the push-function mock records 0 calls during a watch cycle, and `grep -c 'metric push failed' <run log>` returns 0 — plus a startup WARNING is logged ("pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL"; the WARNING itself does not count as a push attempt)
- [ ] Log rotation: log output is capped via `RotatingFileHandler(maxBytes=5_000_000, backupCount=5)` — force rotation by writing > 5 MB through the logger, then `ls -l` on the log path shows the live file ≤ 5 MB and ≤ 5 rotated backups

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format + lint + typecheck + test suite passes
- `make test` — unit tests including the new metric-export coverage
- `grep -rn 'git_ai_sync_last_success_timestamp' src/` — the metric name exists in the source

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `curl -u <monitoring creds> https://pushgateway.dev.nuke.benjamin-borbe.de/metrics | grep git_ai_sync_heartbeat_timestamp` — dev push visible
- Wedge test: stop one vault's sync (or stage conflict markers), confirm Alertmanager alert within the configured window; restore, confirm it clears within one cycle
- Laptop-asleep test: leave the laptop asleep past the 1h threshold, confirm no alert fires
- Alert rule present: `kubectlnukeprod -n monitoring get alert git-ai-sync-sync-stalled` returns the rule

## Desired Behavior

1. On every watch cycle (`watch` loop iteration), git-ai-sync pushes `git_ai_sync_heartbeat_timestamp{vault="<vault-name>"}` to the configured pushgateway, success or failure of that cycle
2. When a watch cycle completes successfully — including idle no-change cycles — git-ai-sync additionally pushes `git_ai_sync_last_success_timestamp{vault="<vault-name>"}` — on success only
3. The pushgateway target is configurable via environment (`GIT_AI_SYNC_PUSHGATEWAY_URL`); basic-auth credentials via `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` / `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD`
4. When no pushgateway URL is configured, metric pushing is fully disabled — zero network calls, no behavior change to the sync loop. A startup WARNING is logged ("pushgateway metrics disabled — set GIT_AI_SYNC_PUSHGATEWAY_URL") so the disable state is observable rather than silent
5. A failed metric push is logged at WARNING and does not abort, retry-storm, or otherwise affect the sync cycle
6. The vault label value is stable and human-readable (the repo directory name, not the full path)
7. Log output is subject to rotation so files do not grow unbounded (observed 27 MB unrotated)

## Constraints

- Reuse the existing `monitoring/pushgateway` (ClusterIP 9090, nuke prod + dev) — do not deploy a new one
- From the laptop the push must use the traefik ingress URL `https://pushgateway.{dev,prod}.nuke.benjamin-borbe.de` with the shared monitoring basic auth (user `monitoring`, secret from TeamVault under `MONITORING_BASIC_AUTH_KEY`) — the in-cluster `http://pushgateway.monitoring:9090` name does not resolve outside the cluster
- Metric names are exactly `git_ai_sync_heartbeat_timestamp` and `git_ai_sync_last_success_timestamp`; the alert rule in the nuke repo depends on these exact names
- Per-vault isolation: each watched vault pushes under its own grouping key so one vault's metrics do not overwrite another's
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Functions over classes for stateless operations (existing codebase pattern)
- No absolute paths in code — paths via `Path`
- The heartbeat push must tolerate gateway unavailability and never block the sync cycle

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Pushgateway unreachable (cluster down, laptop offline) | Push fails, logged at WARNING, sync loop continues | Alert stays silent (heartbeat stale = laptop/network off — correct); when reachable again pushes resume |
| Credentials wrong/expired | Push returns 401, logged at WARNING, sync continues | Fix TeamVault secret, restart service |
| Laptop asleep | No pushes happen; both series go stale | Correct — heartbeat-stale guard keeps the alert silent |
| Vault wedged while laptop awake | Heartbeat fresh, last-success stale → alert fires | Operator fixes wedge (sibling task auto-recovers some cases); on next success alert clears |
| Laptop clock skewed (time() in alert vs timestamp) | A slow laptop clock delays the "1h stale" firing; a fast one can suppress it | NTP on the laptop keeps the clock honest; alert only guards a 1h+ window so small skew is absorbed |
| Two machines watch the same vault | Both push the same grouping key; last-write-wins can mask a wedge on one machine | Deployment is one machine per vault (launchd/systemd); document that two watchers per vault break the alert |
| Push script error / bad metric format | Push fails, logged, sync continues; Prometheus scrapes nothing new | Operator inspects logs, fixes code |

## Security / Abuse Cases

- Credentials: basic-auth user/password read from environment, never logged, never written to disk by the app
- Push scope: only the two git-ai-sync series are pushed, per-vault, to the configured gateway — no arbitrary metric injection
- The push goes over TLS (traefik ingress, websecure entrypoint) with basic auth — no plaintext credential transit

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Metric export module + push + config + watch-loop wiring + unit tests | 1-6 | 1, 2, 5 | — |
| 2 | Log rotation in logging setup | 7 | 6 | — |

Rationale: prompt 1 is the core — a new `metrics.py` module with a push function, config fields, watch-loop wiring, and the unit tests that prove heartbeat/last-success semantics and push-failure tolerance. Prompt 2 is independent and small (rotation in `logging_setup.py`). The alert rule in the nuke repo is an operator-applied config change, not part of this repo's prompts.

Why log rotation is bundled rather than its own spec: it is a single-file change (logging setup) with no business-why beyond "logs must not grow unbounded," and it was already in the same vault task scope as the metrics work. It stays a separate prompt (row 2) so its execution and tests remain independent; splitting it into its own spec would add a full approve/audit round for a one-file fix. If it grows beyond the single prompt, split it out at that point.

## Do-Nothing Option

Detection stays human-only: a wedged vault is noticed when someone happens to notice the vault "feels stale" — observed to take 9 days. The `/watch` probe covers only keyboard-present periods. The next wedge (different cause) recurs silently. Cost of doing nothing: repeated silent multi-day vault drift with wrong answers produced by every tool reading the stale mirror.
