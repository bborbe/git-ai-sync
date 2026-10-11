---
status: prompted
approved: "2026-10-11T12:16:50Z"
generating: "2026-10-11T12:32:51Z"
prompted: "2026-10-11T12:32:51Z"
branch: dark-factory/bug-unsafe-path-refusal-skips-pull
---

## Summary

- When the global pre-commit hook refuses a commit because a staged path is not safe on Windows, git-ai-sync never pulls — the pull sits inside the same `try` as the commit, so any commit failure jumps straight to the error handler
- The local checkout therefore drifts silently behind `origin` for as long as the unsafe path stays staged, while launchd and the `/watch` probe read healthy
- Observed in the `private-personal` vault: 1,194 consecutive `Sync failed: Failed to commit` lines over ~12.5 hours and a checkout 680 commits behind `origin/master`
- Fix: detect the hook's unsafe-path refusal, rename the offending path to a Windows-safe name and retry (bounded), and — whatever happens to the commit — never skip the pull
- Never a silent divergence between the local checkout and `origin` because a commit could not be made

## Problem

`cmd_watch` commits local changes and then pulls, but both steps live inside one `try` block. The commit is the first thing that can raise, so a refused commit skips the pull entirely and lands in the handler that logs `Sync failed` and continues watching. The consequence is not merely a missing commit: the checkout stops receiving remote commits, so every reader of that vault — agents, hooks, sync consumers — answers from a snapshot that silently ages.

Detection is late at best, and was absent in the observed incident. The heartbeat is pushed at the top of every cycle, before the sync `try` (`src/git_ai_sync/__main__.py:172-178`), so it stays fresh while `last_success` — pushed only on the pull/push path that never runs — ages. That combination is exactly what spec-002's sync-stall alert is designed to fire on, so the alert covers this stall **by design** and is not blind to it. Two things made the incident silent anyway: (a) the alert's window is a `last_success` stale by more than an hour, so the checkout diverges silently for at least that long before any signal; and (b) the vault it happened on carries no `GIT_AI_SYNC_PUSHGATEWAY_URL` in its launchd plist, so `config.pushgateway_url` is falsy and neither metric is pushed at all — verified on disk 2026-10-11: 10 of 12 sibling plists are wired, `personal` and `seibert-personal` are not. That configuration gap is a separate defect, tracked outside this spec; this spec fixes the tool-side bug that the alert is supposed to be able to see.

## Reproduction

Environment: `git-ai-sync v0.12.1` (bug present at HEAD `650095f`), global pre-commit hook at `~/.git-hooks/pre-commit` (installed via `core.hooksPath=~/.git-hooks`), repo path NOT under `/tmp` (the hook skips `/tmp/*`, `/var/folders/*`, `/private/tmp/*`, `/private/var/folders/*`).

Steps:
1. `git init --bare ~/Documents/workspaces/gas-repro-remote`
2. `git init ~/Documents/workspaces/gas-repro` (real path so the hook fires)
3. `git remote add origin ~/Documents/workspaces/gas-repro-remote`; write `clean.md`; `git add clean.md && git commit -m initial && git push -u origin master`
4. Advance the remote so a pull has something to fetch: from a second clone, commit and push `remote-only.md`
5. In the repro repo create `unsafe?.md` (the `?` is the trigger) and `git add .`
6. Run `git-ai-sync watch --interval 5 ~/Documents/workspaces/gas-repro`

Observed: the commit is refused every cycle, and `remote-only.md` never appears locally.

```
2026-10-11 11:59:45 ERROR    [git_ai_sync.__main__:283] Sync failed: Failed to commit: 
✋ Blocked: staged path is not safe on Windows

     unsafe?.md  —  contains one of  : ? * " < > |

   Windows rejects these characters in a path component: : ? * " < > |
   It also rejects a component ending in a dot or a space, and a name
   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.

   Rename the file, then commit again:
```

Verbatim from the production incident, `/tmp/git-ai-sync-personal.log` — the last of 1,194 `Sync failed: Failed to commit` lines spanning 2026-10-10 23:28 → 2026-10-11 11:59:

```
2026-10-11 11:59:45 ERROR    [git_ai_sync.__main__:283] Sync failed: Failed to commit: 
✋ Blocked: staged path is not safe on Windows

     00 Inbox/This AI Learned from 12 Billion Candles. Can It Trade Gold?.md  —  contains one of  : ? * " < > |

   Windows rejects these characters in a path component: : ? * " < > |
   It also rejects a component ending in a dot or a space, and a name
   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.

   Rename the file, then commit again:
```

Note the reason string carries two spaces after `of` — that is the hook's own text (`~/.git-hooks/pre-commit:137`), not a transcription slip. The hook's four reason strings are `contains one of  : ? * " < > |`, `has a path component ending in a dot or a space`, `ends in a dot or a space`, and `has a path component that is a reserved Windows device name`.

and the resulting git state, with the remote having advanced throughout:

```
$ git rev-list --left-right --count HEAD...origin/master
0	680
```

## Expected vs Actual

**Expected:** per the tool's stated sync flow (`CLAUDE.md:116` — "stage → commit → pull --rebase → push"), and per spec-002's principle that a refusal must never leave the tool in a silently-wedged state, a refused commit must not prevent the pull. Whatever happens to the commit, the checkout tracks `origin`; a refusal is reported loudly rather than absorbed.

**Actual:** `src/git_ai_sync/__main__.py:204-213` (stage + commit) and `:215-242` (pull) share one `try`. A refusal from `commit_with_marker_recovery` propagates to the `except git_operations.GitError` handler at `:282-284`, which logs `Sync failed: {e}` and `Continuing to watch...`. The pull at `:215` is never reached. `cmd_sync` reaches the same outcome by a different route: commit and pull sit in separate `try` blocks, and a commit failure exits 1 at `:352` before the pull at `:358-360`.

## Why this is a bug

The tool's contract is "automatic git repository sync". A commit that cannot be made is a *local* condition — an unsafe filename, a hook refusal — but the code lets it suppress a *remote* operation it has nothing to do with. The user-visible promise (`CLAUDE.md:3`, "Automatic Git repository sync") is broken for as long as the offending path is staged, and the break is invisible: the log says `Sync failed`, which reads like "this cycle did nothing", not "this checkout has stopped receiving remote commits".

This is the sibling of spec-002 (`bug-marker-refusal-recovery`). That spec fixed the *loop-forever* half of a hook refusal; this one fixes the *silently-stops-pulling* half. Both share a root cause: the refusal is classified as a generic `GitError` and handled by the catch-all handler rather than by a refusal-specific path.

## Goal

When the pre-commit hook refuses a commit because a staged path is not safe on Windows, git-ai-sync recovers: it detects the refusal, renames each offending path to a Windows-safe name, re-stages, and retries the commit up to a bounded number of attempts. Independently of the commit's outcome, the pull always runs — if the commit could not be made, the offending paths are unstaged first so the working tree is clean enough for the pull, and the refusal is logged loudly with the exact paths. The local checkout never silently diverges from `origin` because a commit failed.

## Non-goals

- No change to the global pre-commit hook — it is correct; the fix belongs in git-ai-sync's recovery
- No change to spec-002's marker-refusal recovery path — this is a sibling classification, not a replacement
- No `--no-verify`, no hook bypass, no weakening of the hook's rules
- No change to pull/push strategy (`--rebase` vs `--merge`), the retry interval, or the watch loop machinery
- No sanitization of file *contents* — only path components
- No fix to the missing `GIT_AI_SYNC_PUSHGATEWAY_URL` on the `personal` / `seibert-personal` launchd plists — that config gap is a separate defect, tracked outside this spec

## Acceptance Criteria

- [ ] **Classification:** a unit test asserts that a `GitError` whose message contains `staged path is not safe on Windows` (case-insensitive) is classified as an unsafe-path refusal, and one with an unrelated message is not — and that a marker-refusal message is NOT classified as an unsafe-path refusal (the two classes stay disjoint). Evidence: `pytest tests/test_git_operations.py -k unsafe_path_refusal` passes in `make test`.
- [ ] **Offending-path extraction:** a unit test asserts that the refused paths are recovered from the hook's stderr block (lines of the form `     <path>  —  <reason>` between the `✋ Blocked:` banner and the trailing explanatory paragraph), and that a non-refusal stderr yields an empty list. Evidence: `pytest tests/test_git_operations.py -k blocked_paths` passes in `make test`.
- [ ] **Path sanitization:** a unit test asserts DB 3's fixed rule set with a table of input → expected output covering one case per rule: `a?b` → `a-b`; `a???b` → `a-b` (run collapsed); `-a-` → `a` (edges trimmed); `a.` → `a` and `a ` → `a` (trailing dot / space); `CON.md` → `CON_.md` and `con.md` → `con_.md` (reserved stem, case-insensitive); `???.md` → `untitled.md` (component empty after sanitizing); and a collision — sanitizing `a?.md` to `a.md` when `a.md` already exists → `a-2.md`. Evidence: `pytest tests/test_git_operations.py -k sanitize_path` passes in `make test`.
- [ ] **Auto-recovery rename:** a unit test asserts that a refused commit triggers a rename of each offending path to its sanitized name, that the new path is staged and the old one is no longer present in `git status`, that a sanitized name colliding with an existing file gets a disambiguating suffix rather than overwriting it, and that the commit is retried and returns cleanly on a successful retry. Evidence: `pytest tests/test_conflict_resolver.py -k unsafe_path_recovery_success` passes in `make test`.
- [ ] **Bounded retries:** a unit test asserts that after 3 consecutive unsafe-path refusals recovery stops and raises a final error naming the still-offending paths — no 4th attempt. Evidence: `pytest tests/test_conflict_resolver.py -k unsafe_path_recovery_bounded` passes in `make test`.
- [ ] **Pull survives a commit failure (the load-bearing AC):** a unit test drives one watch cycle in a repo whose commit is refused, with the remote holding a commit the local checkout does not have, and asserts that after the cycle the local checkout contains the remote commit — i.e. the pull ran despite the refusal. It additionally asserts the cycle landed a real commit: `git log --oneline -1` shows a commit whose tree contains the sanitized path — not merely that the refusal was logged. It further asserts that when the commit could not be made at all, the offending paths were unstaged so the pull could run. Evidence: `pytest tests/test_main.py -k watch_pulls_when_commit_refused` passes in `make test` — the recoverable and unrecoverable sub-scenarios are separate test functions (`…_recovered` / `…_unrecovered`), both collected by that selector.
- [ ] **Refusal is reported, not absorbed (negative):** the same test asserts the captured log names the offending path, and that no log line reports a bare `Sync failed:` with nothing after it — the refusal is never absorbed silently. Evidence: `pytest tests/test_main.py -k watch_pulls_when_commit_refused` asserts, over the captured log with `re.MULTILINE`, zero matches for `^Sync failed: Failed to commit:\s*$` and at least one match for the offending path.
- [ ] **One-shot sync parity:** a unit test asserts `cmd_sync` also pulls when its commit is refused, matching watch mode — and that its exit code follows DB 6: a recovered-and-pulled cycle exits 0, while an unrecoverable refusal (paths still offending after 3 attempts) exits non-zero. Evidence: `pytest tests/test_main.py -k sync_pulls_when_commit_refused` passes in `make test`.
- [ ] **Watch continues after a refusal:** a unit test asserts that a refused cycle is followed by a further cycle in the same watch run — the loop does not exit, and does not spin without pulling. Evidence: `pytest tests/test_main.py -k watch_continues_after_refusal` passes in `make test`; the test asserts a second cycle emits `[2] Checking...` and re-evaluates `git rev-parse HEAD`, proving the interval elapsed rather than a tight retry loop.
- [ ] **Marker-refusal regression, negative:** the existing marker-refusal tests (5 of them in `tests/test_conflict_resolver.py`) continue to pass without modification, proving a marker refusal still takes spec-002's path (resolver invoked, `MarkerRefusalError` raised on final failure) and does NOT trigger the unsafe-path rename path. Evidence: `pytest tests/test_conflict_resolver.py -k marker_recovery` passes in `make test`.
- [ ] **Regression:** `make precommit` exits 0 — format + lint + mypy strict + full test suite, all pre-existing tests still passing.
- [ ] **Runtime repro gone (bug verification):** replaying the Reproduction steps against a fresh repro repo with the fixed binary produces a commit with a Windows-safe path and the remote-only commit present locally. Evidence: the watch-mode log lines and `git rev-parse HEAD` equalling `git rev-parse origin/master` after the cycle. (Operator-executable, after `uv tool upgrade git-ai-sync`.)

## Verification

### Container-executable (runs inside the dark-factory YOLO container at prompt time)

- `make precommit` — format + lint + mypy strict + test
- `make test` — full pytest suite
- `pytest tests/test_git_operations.py -k 'unsafe_path_refusal or blocked_paths or sanitize_path'`
- `pytest tests/test_conflict_resolver.py -k unsafe_path_recovery`
- `pytest tests/test_main.py -k 'pulls_when_commit_refused'`

### Operator-executable (runs on the host after PR merge)

- `uv tool upgrade git-ai-sync`, then re-run the Reproduction steps against a fresh repro repo — confirm the offending path is renamed, the commit succeeds, and the remote-only commit is present locally.

## Desired Behavior

1. When `git commit` returns non-zero and stderr matches the hook's unsafe-path signature (`staged path is not safe on Windows`, case-insensitive), the failure is classified as an unsafe-path refusal rather than a generic `GitError`.
2. The offending paths are extracted from the hook's own stderr block — the indented `<path>  —  <reason>` lines under the `✋ Blocked:` banner.
3. Each offending path is renamed to a Windows-safe name using a fixed rule set applied to each path component. Take the component and split it at its final `.` into stem and extension; then, on the stem: replace each of `: ? * " < > |` with `-`; collapse runs of `-` to one; trim leading and trailing `-`; strip a trailing dot or space; if the stem matches a reserved device (`CON`, `PRN`, `AUX`, `NUL`, `COM1`-`COM9`, `LPT1`-`LPT9`, case-insensitive) append `_`; and if the stem is left empty, use `untitled`. Reassemble stem + extension. The new path is staged and the commit retried. When the sanitized name collides with an existing file, `-2`, `-3`, … is appended to the stem rather than the existing file being overwritten. Total attempts are bounded at 3 (1 initial + 2 recovery rounds).
4. If recovery exhausts its attempts, the offending paths are unstaged so the working tree is clean enough to pull, and the failure is logged at ERROR naming each still-offending path.
5. **The pull runs on every cycle in which a pull is possible — including cycles whose commit was refused.** A commit failure is never the reason a pull is skipped.
6. When the commit was refused but the pull succeeded, the cycle reports both facts: the refusal (with paths) and the pull's outcome (commits pulled, or none). In `sync` mode a recovered-and-pulled cycle exits 0; a refusal that could not be recovered (paths still offending after 3 attempts) exits non-zero, matching spec-002's precedent.
7. Watch mode continues to the next interval after a refusal; it does not exit and does not loop without having pulled.
8. Spec-002's marker-refusal behavior is unchanged: a marker refusal still routes to the AI resolver, still raises `MarkerRefusalError` after 3 attempts, and still exits non-zero in both `watch` and `sync`.

## Constraints

- Do NOT weaken, bypass, or extend the global pre-commit hook — no `--no-verify`, no `core.hooksPath` override, no hook edit
- Reuse the existing error-handling shape: `subprocess.run(..., check=False)` with a typed `GitError` subclass raised before any broad `except`
- The new refusal class must be disjoint from `MarkerRefusalError` — classification order in `commit()` is a correctness concern, not a style choice
- Path renaming must use `git mv` (or filesystem rename followed by staging both sides) so the change is visible to `git status` in the same cycle
- Type hints on all new functions (mypy strict); pytest conventions only; `asyncio_mode = "auto"`
- Existing public function signatures in `git_operations.py` must keep their current behavior; new behavior is additive
- Classification keys off the hook's current banner text (`staged path is not safe on Windows`) and its indented `<path>  —  <reason>` line format; a change to that hook text is out of scope and would need a follow-up spec
- `cmd_watch` and `cmd_sync` must both gain the never-skip-the-pull guarantee — fixing only watch leaves the one-shot path silently divergent

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Unsafe path staged; rename succeeds | Commit succeeds on retry with the sanitized path; pull runs; INFO names the rename | Automatic; no operator action |
| Unsafe path staged; rename fails (permission denied, read-only parent) | Path is unstaged; pull still runs; ERROR names the path and the rename failure | Operator renames by hand; next cycle commits it |
| Two offending paths, one of which sanitizes onto an existing name | The colliding path gets the `-2` suffix; **both** paths are committed | Automatic; INFO names the disambiguation |
| Refusal persists after 3 attempts | ERROR names still-offending paths; pull still runs; watch continues to the next interval | Operator inspects the named paths |
| Pull itself fails after a refused commit | The pull's own error handling is unchanged (conflict detection, `git-ai-sync resolve` pointer) | Unchanged from today |
| Marker refusal (spec-002 path) | Unchanged: resolver invoked, bounded at 3, `MarkerRefusalError` on final failure | Unchanged from today |
| Remote unreachable during a refused-commit cycle | Pull error surfaces as today; the refusal is still reported | Unchanged from today |
| Hook text changes (banner wording or the indented line format) | Classification misses, so rename recovery stops — but DB 5's pull guarantee is structural and still holds, so the checkout keeps syncing | Operator updates the classifier; a follow-up spec per Constraints |

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `git_operations.py`: unsafe-path refusal classification, offending-path extraction, path sanitization helper | 1, 2, 3 | 1, 2, 3 | — |
| 2 | `conflict_resolver.py`: bounded recovery — rename, re-stage, retry, unstaging fallback | 3, 4 | 4, 5 | prompt 1 (uses the classifier + sanitizer) |
| 3 | `__main__.py`: pull is never skipped on a commit failure, in both `watch` and `sync` | 5, 6, 7 | 6, 7, 8, 9 | prompts 1, 2 |
| 4 | Regression lock for the marker path (spec-002) | 8 | 10 | prompt 3 |

Two ACs are not owned by any single prompt: AC 11 (`make precommit`) applies to every prompt as its regression gate, and AC 12 is operator-executable, running on the host after merge. DB 8's "exits non-zero in both `watch` and `sync`" clause is additionally carried by the existing `tests/test_main.py::test_sync_marker_refusal_exits` and `::test_watch_marker_refusal_exits`, which AC 10's `-k marker_recovery` selector does not collect — prompt 4 must run those two by name.

Rationale: prompt 1 establishes the classification and sanitization primitives; prompt 2 builds the recovery loop on them; prompt 3 is the wiring that delivers the load-bearing guarantee and needs both; prompt 4 is a test-only addition proving the sibling path did not regress.

## Do-Nothing Option

Not fixing this leaves a failure mode that is both silent and unbounded. A single unsafe filename — which the vault's capture path produces with no agent involvement at all — stops the checkout from receiving remote commits for as long as it stays staged. In the observed incident that was 12.5 hours and 680 commits.

Nothing surfaces it promptly. The log reads `Sync failed`, which is indistinguishable from a cycle that simply had nothing to do. Spec-002's alert is designed to catch exactly this condition (heartbeat fresh + `last_success` stale), but its window is over an hour — so the checkout diverges silently for at least that long — and on a vault whose launchd plist carries no pushgateway URL, which is the configuration this incident ran under, no metric is pushed and nothing fires at all.

The user-side workaround is to notice a stale vault and rename the file by hand, which is what happened, roughly half a day late. That is not a workaround a reader can rely on, because the symptom is a *silence*: nothing prompts the check. And the class is not rare — the vault's own inbox is a steady source of raw video titles, and any of them containing `?`, `:` or `*` reproduces it.

Cost of doing nothing: the same class recurs on the next unsafe filename, and each occurrence silently ages every consumer of the vault — agents answering from a stale snapshot, hooks reading stale state — with no signal until a human happens to notice the vault looks wrong. The fix is a bounded change inside one tool; the cost of not doing it is unbounded and invisible.

## Open Questions

- None. The hook's rule set and refusal text were read verbatim from `~/.git-hooks/pre-commit:134-162`; the offending-path format (`     <path>  —  <reason>`) is fixed by that hook's `printf` at `:158`.
