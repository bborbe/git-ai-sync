---
status: verifying
approved: "2026-10-23T15:43:55Z"
generating: "2026-10-23T16:09:29Z"
prompted: "2026-10-23T16:09:29Z"
verifying: "2026-10-23T16:20:52Z"
branch: dark-factory/watch-quiet-window-decay
---
## Summary

- `git-ai-sync watch` refuses to sync while the vault is being written, and the vault is written continuously by a large agent fleet.
- Under that load the quiet window is never satisfied, so the syncer can go an unbounded time without committing or pulling.
- The fix makes the required quiet window shrink the longer the syncer has been waiting, and adds a maximum wait that forces a sync regardless of quiet.
- Both the starting window and the maximum are configurable and documented.
- A quiet vault behaves exactly as it does today.

## Problem

`git-ai-sync watch` gates every cycle on a single condition: sync only if the newest filesystem change is at least `interval` seconds old. With `--interval 30` on a vault written by a fleet of concurrent agent sessions, that condition is never met — a change lands every few seconds.

The `continue` that implements the skip sits above the pull, so a busy vault does not merely delay the commit — it stops fetching remote work as well. Nothing decays, nothing times out, and the skip is logged at INFO while the consecutive-failure counter deliberately ignores skips, so no alert fires for this failure mode.

The decaying window is not an embellishment added here: the operator's recorded design for this fix is "shrink the required quiet window the longer the syncer has been waiting, so a busy vault still syncs within a bounded time". The maximum wait alone would satisfy the bound; the decay is what keeps the typical latency at 90–180 seconds rather than 300.

## Reproduction

Vault `private-personal`, launchd job `com.github.bborbe.git-ai-sync-personal`, `git-ai-sync` v0.13.1, `dark-factory` v0.198.7, arguments `watch <vault> --interval 30 --strategy merge`. Log `/tmp/git-ai-sync-personal.log`:

```
2026-10-23 15:59:48 INFO [git_ai_sync.__main__:199] [1991] Checking...
2026-10-23 15:59:51 INFO [git_ai_sync.__main__:343] Pushed to remote
2026-10-23 16:00:22 INFO [git_ai_sync.__main__:199] [1992] Checking...
2026-10-23 16:00:23 INFO [git_ai_sync.__main__:332] No local changes
2026-10-23 16:00:54 INFO [git_ai_sync.__main__:191] [1993] Skipping - files changed 27.6s ago (still editing)
...
2026-10-23 16:27:47 INFO [git_ai_sync.__main__:191] [2046] Skipping - files changed 12.8s ago (still editing)
2026-10-23 16:28:06 INFO [git_ai_sync.__main__:116] Received signal 15, shutting down
```

54 consecutive skips across a 27-minute skip window (16:00:54 → 16:27:47). The vault went 32.5 minutes without a commit — 15:59:51 to 16:32:22. The stall ended only because an operator stopped the service, ran a one-shot sync, and restarted it: the first cycle after the restart committed 11 files and pulled 11 commits from the remote.

### Replay without the live vault

```bash
mkdir -p /tmp/gas-repro && cd /tmp/gas-repro && git init -q && git commit -q --allow-empty -m init
( while true; do echo "$(date +%s)" > note.md; sleep 3; done ) & touchloop=$!
GIT_AI_SYNC_PUSHGATEWAY_URL= uv run python -m git_ai_sync watch /tmp/gas-repro --interval 30
kill $touchloop
```

Expected on the current build: every cycle logs `Skipping - files changed Ns ago (still editing)` and no commit ever lands. Expected after the fix: a commit lands within `max_wait + interval`.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A vault written continuously still lands commits and pulls remote work within a bounded time. The documented design (`README.md:91` "Debounce — Skips sync when files changed recently"; `CLAUDE.md` § Key Design Decisions "Watch mode = poll + debounce") describes a debounce, and a debounce that never releases is not a debounce. |
| **Actual** | The debounce never releases while the fleet writes: 54 consecutive skips, 32.5 minutes without a commit or a pull, ended only by an operator restarting the service. |

This contradicts a reasonable user expectation rather than a documented promise — no doc promises a bound, but none describes an unbounded deferral either, and the debounce's stated purpose (avoid committing mid-edit) is served by any finite window.

## Goal

A vault written continuously still commits and pulls remote work within a bounded interval — the maximum wait rounded up to the next poll boundary, i.e. at most `max_wait + interval` — while a vault that goes quiet behaves exactly as it does today. The bound is configurable, documented, and provable from the service log.

## Non-goals

- Changing the `interval` default or what `interval` means for a quiet vault.
- Changing the pull strategy (`--strategy merge` / `rebase`).
- Changing the consecutive-failure counter or the stall-attention card.
- Changing the pushgateway metrics.
- Changing the pre-commit hook or the vault's content.
- Any behaviour on the `sync` / `resolve` / `status` / `doctor` / `version` subcommands.
- Documenting the setting in `docs/systemd-user-service.md`, which today documents no environment variables at all; `docs/launchd-service.md` is the service doc that lists the passthrough keys and is the one updated.

## Assumptions

- The newest filesystem-change timestamp is an acceptable proxy for "an agent is still editing". This change scales that proxy's threshold; it does not replace it.
- The existing instance lock guarantees one watch process per repository, so the skip count needs no cross-process coordination.
- The decay and the maximum wait both use the poll cadence the watch loop actually runs at — the `--interval` argument (default 30 seconds), not a second independent timer.
- A skipped cycle never writes to git, so the only state the decay adds is an in-memory counter and a timestamp, both reset by each successful sync.

## Alternatives considered

- **Cap only, no decay.** A single maximum wait satisfies the Goal with one mechanism, one knob and one test surface. Rejected because it leaves the vault at the full debounce window for up to `max_wait` on every stall, whereas the decay starts syncing as soon as the window falls below the observed change age — typically 90–180 seconds rather than 300 — so the vault is current sooner at no extra operator cost. The decay also keeps the normal path (a briefly quiet vault) unchanged.
- **Always pull, skip only the commit.** The cheapest fix for the second half of the Problem: move the pull above the skip so a busy vault keeps fetching even while the commit is debounced. Rejected as the whole answer because it leaves the primary symptom — local work not being committed for 28 minutes — untouched, and it changes quiet-vault behaviour (a pull on every cycle). Worth doing separately if the pull half ever needs its own fix.
- **Reduce `interval` globally.** Makes the stall shorter but not bounded, and doubles the sync cadence for every vault including quiet ones.
- **Hardcode the maximum wait instead of making it configurable.** Rejected: the operator asked for the maximum to be a documented, configurable bound, and `interval` is already per-vault tunable, so a fixed cap would bind differently for every vault without an escape hatch.
- **Do nothing.** Costed below.

## Acceptance Criteria

- [ ] AC1 — The required quiet window after N consecutive skips is `interval / 2**N`, and a successful sync resets N to 0. Evidence: `uv run pytest tests/ -q -k "decay or window"` exits 0 asserting, for `interval = 32`, the window is exactly 32, 16, 8, 4, 2 for N = 0..4 and is < 0.001 for N = 20, and is back to 32 after a completed cycle.
- [ ] AC2 — The gate's own decision, not a helper's return value, reflects the decayed window. Evidence: `uv run pytest tests/ -q -k "decay or window"` exits 0 with a test pinning `seconds_since_change` to 5 s — at `interval = 30` the gate rejects after 1 skip (window 15 s), rejects after 2 (7.5 s) and accepts after 3 (3.75 s); at `interval = 100` it rejects through skip 4 (6.25 s) and accepts at skip 5 (3.125 s). A hardcoded skip counter satisfies only the first sequence and fails the second.
- [ ] AC3 — The maximum wait binds at the first cycle boundary at or after it, for a cap that is and is not a multiple of the interval. Evidence: `uv run pytest tests/ -q -k "decay or window"` exits 0 with two cases under an injected clock and a change stream that never goes quiet — `interval = 30, max_wait = 60` forces at the second boundary (60 s, not the first at 30 s); `interval = 30, max_wait = 75` forces at the third boundary (90 s, not the second at 60 s). A hardcoded "every second cycle" satisfies the first case and fails the second; an implementation that reads the maximum wait and never acts on it forces never and fails both.
- [ ] AC4 — The production watch loop, not a helper, is what changes, and a successful sync resets the decay. Evidence: `uv run pytest tests/test_main.py -q -k quiet_window` exits 0, driving `cmd_watch` with an injected clock and a change stream that never goes quiet, asserting the pull and the push are invoked and a cap-forced log record is emitted; a second scenario runs two cycles where the first syncs and the second sees a 5 s change age at `interval = 30` and must skip again. A gate module that is never wired into `cmd_watch` fails this test.
- [ ] AC5 — A quiet vault is unchanged. Evidence: `git rev-parse --verify origin/master` succeeds; `test -n "$(git diff origin/master -- tests/test_main.py)"` succeeds (the file really was touched, so the negative probe below cannot pass vacuously); `uv run pytest tests/test_main.py -q` exits 0; `git diff origin/master -- tests/test_main.py | grep -cE '^-[^-].*assert' || true` prints `0`; and `test_watch_heartbeat_on_debounce_skip_cycle` and `test_watch_skip_is_neutral` still assert a skip at N = 0 with `interval = 30`.
- [ ] AC6a — The setting exists under a fixed name, has the documented default, is honoured rather than rewritten, and its configured value actually drives the forcing cadence. Evidence: `uv run python -c "from git_ai_sync.config import Config; print(Config().max_wait)"` prints `300`; `uv run pytest tests/test_main.py -q -k quiet_window` exits 0 with a case running `cmd_watch` under `GIT_AI_SYNC_MAX_WAIT=10` and `--interval 30` that forces a sync on **every** cycle and logs a warning that the debounce is effectively disabled — a hardcoded two-cycle boundary fails it.
- [ ] AC6b — The setting is documented and reaches a managed service, and the change is in the CHANGELOG. Evidence: `grep -n 'GIT_AI_SYNC_MAX_WAIT' README.md` returns ≥ 1 line; `grep -n 'GIT_AI_SYNC_MAX_WAIT' docs/launchd-service.md` returns ≥ 1 line; `grep -n 'MAX_WAIT' src/git_ai_sync/launchd.py` returns ≥ 1 line; `awk '/^## /{sec=$0} /MAX_WAIT/{print sec}' CHANGELOG.md` prints `## Unreleased`; `grep -nE 'halves|decay' README.md` returns ≥ 1 line and `grep -c 'Skips sync when files changed recently' README.md` returns `0`; `grep -nE 'decay|max_wait' CLAUDE.md` returns ≥ 1 line.
- [ ] AC7 — A skipped cycle reports the window in force, and a cap-forced cycle is distinguishable from a decay-accepted one. Evidence: `uv run pytest tests/test_main.py -q -k quiet_window` exits 0 with `caplog` assertions — at `interval = 30`, the skip record at N = 1 contains `15` (a constant `30` fails), and a cap-forced cycle emits a record naming the maximum wait.
- [ ] **Post-Deploy (Rung-3):** the installed service syncs within the bound on a busy vault — evidence: over a ≥ 30-minute window in `/tmp/git-ai-sync-personal.log`, `grep -c 'Skipping - files changed'` returns ≥ 20, `grep -c 'Pushed to remote'` returns ≥ 5, and no two consecutive `Pushed to remote` timestamps are more than `max_wait + interval` apart.
  - `deploy_check:` `git-ai-sync version | awk '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 | sed 's/^v//')`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, ruff, mypy strict, and the full pytest suite
- `uv run pytest tests/ -q` — full suite
- `uv run pytest tests/ -q -k "decay or window or quiet_window"` — the window, gate and watch tests
- `uv run python -c "from git_ai_sync.config import Config; print(Config().max_wait)"` — prints `300`
- `grep -n 'max_wait' src/git_ai_sync/config.py` — returns at least one line
- `grep -n 'MAX_WAIT' src/git_ai_sync/launchd.py` — returns at least one line (the key is uppercase in the passthrough allowlist)
- `grep -n 'GIT_AI_SYNC_MAX_WAIT' README.md docs/launchd-service.md` — returns a line in each
- `git rev-parse --verify origin/master && git diff origin/master -- tests/test_main.py | grep -cE '^-[^-].*assert' || true` — prints `0`

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `uv tool install --force "git+https://github.com/bborbe/git-ai-sync@v<X.Y.Z>"` — the install path the README documents; this repo publishes no package index, and `release-wheel.yml` uploads the wheel to the GitHub Release. `<X.Y.Z>` is the version the maintainer bot releases on merge (`.maintainer.yaml` sets `release.autoRelease: true`, which pushes a `v*` tag); confirm with `git tag --points-at HEAD` in the released checkout, and if the bot has not tagged yet, cut it with `git tag vX.Y.Z && git push origin vX.Y.Z`.
- `git-ai-sync version` — reports that version
- restart the service per `docs/launchd-service.md` (the `launchctl kickstart -k gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` step); confirm the live label first with `launchctl list | grep git-ai-sync`, since the label derives from the vault directory name
- `grep -c 'Skipping - files changed' /tmp/git-ai-sync-personal.log` over a 30-minute window — returns at least 20, proving the vault was busy
- `grep -c 'Pushed to remote' /tmp/git-ai-sync-personal.log` over the same window — returns at least 5 (the bound `max_wait + interval` = 330 s allows 5 in 1800 s)
- every `Pushed to remote` line is preceded by a `Checking...` line, and the window contains at least one `Pulled N commit(s)` or `No new commits from remote` line

Runtime evidence for the bound is the Post-Deploy AC above, and this rung is the set of commands that produces it after merge. The deploy gate compares the installed version against the repo's latest tag, so `spec-verifier` refuses to walk the AC on a stale install.

## Desired Behavior

1. The quiet window is a function of the number of consecutive skipped checks since the last successful sync: it starts at `interval` and halves on each skip. A successful sync resets the count and the window.
2. The gate accepts a cycle when either the newest change is older than the current decayed window, or the elapsed time since the last successful sync has reached the maximum wait. Both conditions are evaluated at cycle boundaries, so the bound is the maximum wait rounded up to the next boundary. A successful sync means a cycle that reaches the sync stage without failing — the `record_success()` path, including a cycle that finds nothing to commit. The elapsed clock starts at process start, is measured with `time.monotonic()` (not `time.time()`), and is updated by every successful sync; a monotonic last-success timestamp does not exist today and has to be added, since the only current artifact is the pushgateway metric, which is skipped when no pushgateway URL is configured. A failed cycle does not reset the clock.
3. The maximum wait is a setting named `max_wait` (environment variable `GIT_AI_SYNC_MAX_WAIT`) with a default of 300 seconds. A configured value below `interval` forces a sync on every cycle; a value between `interval` and `2 * interval` forces at least every other cycle. Both are honoured rather than rewritten, and a value below `interval` logs a startup warning naming the consequence that the debounce is effectively disabled. The value is resolved at watch startup — where the effective `--interval` is known — not at `Config` load — and the effective value is logged there. The decay schedule's starting value is the existing `--interval` argument. Both appear in the README configuration table, `GIT_AI_SYNC_MAX_WAIT` is added to the launchd passthrough allowlist and to `docs/launchd-service.md`, and the change carries a `## Unreleased` CHANGELOG bullet.
4. A skipped cycle logs the window currently in force; a cycle accepted because the maximum wait elapsed logs a message naming the cap, so an operator reading the log can tell which mechanism fired.
5. An accepted cycle runs the existing commit → pull → push sequence unchanged, including the pull that the skip path currently bypasses.
6. A vault that goes quiet between cycles sees the same sync cadence it sees today.

## Constraints

- Python 3.14, `uv`, pytest with `asyncio_mode = "auto"`; `make precommit` must stay green.
- `docs/dod.md` applies in full: type hints on all function signatures, docstrings on public functions, no unexplained `# type: ignore` or `noqa`, functions over classes for stateless operations, no absolute paths.
- Configuration stays on the existing pydantic-settings class with the `GIT_AI_SYNC_` env prefix.
- The existing `interval` semantics for a quiet vault must not change.
- Test setup may be extended — clock injection, and a `max_wait` default on `_configure_watch_config` (`tests/test_main.py:129`) so existing watch tests do not compare against a `MagicMock`; no assertion may be removed or weakened. Existing `time.sleep` patches must keep working.
- The new tests carry `decay`, `window`, or `quiet_window` in their names so the verification selectors collect them.
- The watch loop's notion of "now" is supplied by its caller so tests can advance time deterministically; no test may sleep on the real clock.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery | Reversibility |
|---|---|---|---|---|
| Vault never goes quiet for hours | Sync forced at the first cycle boundary at or after the maximum wait, i.e. within `max_wait + interval` | `grep -c 'Pushed to remote'` over any 30-minute window returns ≥ 5 | None needed — the vault stays current | n/a |
| Maximum wait configured below `interval` | Honoured — every cycle syncs — with a startup warning that the debounce is effectively disabled | Startup warning naming the consequence; sync lines at the poll cadence | Operator raises the value | n/a |
| Maximum wait set to 0 or negative | Same as below `interval`: treated as "always force" | Startup warning; sync lines at the poll cadence | Operator sets a positive value | n/a |
| Partial write present when a cycle is accepted — either the cap fired, or the decayed window dropped below the newest change age | The cycle commits the partially written file; the debounce is bypassed by design, and this is the accepted trade for a bounded wait | A commit whose file count is unexpectedly low, or a follow-up commit touching the same file | Operator runs `git show --stat <sha>`; if the file is truncated, `git revert <sha>` and let the next cycle commit the completed file | Reversible — a follow-up commit or revert restores the file |
| Wall clock steps backwards while the process runs | The change age is computed from `time.time()` and reads large, so the gate accepts during active editing; the monotonic cap does not bound that direction | Syncs land while the vault is being written, visible as a `Checking...` line immediately after a sub-second `Skipping` line | Operator restarts the service after the clock settles; the skip count resets | n/a |
| Process restarts while waiting | The elapsed clock restarts at process start, so at most one extra maximum wait passes before the first forced sync | `Pushed to remote` gap slightly exceeds `max_wait + interval` across a restart | None needed | n/a |
| Sync fails while a cap-forced cycle is running | Existing failure handling applies: the counter increments and the loop continues; the elapsed clock is not reset | The existing stall card, unchanged | Existing stall-card path is unchanged | n/a |
| Concurrent watch processes on the same repo | Existing instance lock still governs; this change adds no new shared state | Existing lock error at startup | Unchanged | n/a |

## Security / Abuse

The change reads one new integer setting and a filesystem timestamp. It writes no new files and makes no network calls. A value below `interval` is honoured rather than rejected, so a malformed or aggressive environment variable can never stop the service from syncing — the worst case is that it syncs every cycle.

## Why the cap defaults to 300 seconds

With `interval = 30` the decay alone usually accepts within 90–180 seconds, so the default cap rarely fires on this vault. That is intended: the decay is the normal path, and the cap is the guaranteed upper bound for an operator who raises `interval` (a 600-second interval would otherwise leave the window above the observed age for ten minutes) or for a change cadence that keeps the window above the newest change's age. Shipping the cap with a default above the typical decay bound is what makes "bounded time" a guarantee rather than a hope.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Clock seam and the window/cap decision as a pure unit, with its own tests | 1, 2 | AC1, AC2, AC3 | — |
| 2 | Wire the decision into `cmd_watch`, replacing the flat comparison, plus the two log messages and the success reset | 4, 5 | AC4, AC7 | prompt 1 |
| 3 | Configuration setting, the below-interval warning, the launchd passthrough, and the README / CLAUDE.md / service-doc / CHANGELOG updates | 3 | AC6a, AC6b | — |
| 4 | Integration tests driving `cmd_watch`: cap boundary, env-configured cap, post-success skip, quiet-vault regression | — | AC4, AC5, AC6a, AC7 | prompts 1, 2 |

Rationale: prompt 1 establishes the decision as a pure function with its unit tests, and prompt 2 wires it into the production loop; prompt 3 is independent config and docs work; prompt 4 adds the integration tests once 1 and 2 exist. AC4 and AC7 appear in rows 2 and 4 by design — row 2 lands the wiring, row 4 proves it. DB6 (unchanged quiet-vault cadence) is a constraint on every prompt rather than its own row, and is enforced by AC5.

## Do-Nothing Option

The syncer keeps going quiet for as long as the fleet keeps writing. Each stall costs a manual stop/sync/restart and leaves every concurrent session reading a vault that is missing remote commits — the failure this spec was written from ran 32.5 minutes and was only noticed by an operator. The cost of doing nothing is paid again on every busy day.
