---
status: draft
kind: bug
---

## Summary

- git-ai-sync watch mode loops forever when the global pre-commit hook refuses a commit over conflict-marker content
- Detect the hook's refusal signature in the commit path and auto-recover instead of looping
- Recovery: list flagged files with the hook's own pattern, resolve each with the existing AI conflict resolver, re-stage, retry the commit — bounded to 3 attempts total
- On final failure: log the exact still-flagged files + `Run 'git-ai-sync resolve <path>'` + exit non-zero, in both sync and watch mode
- Never a silent infinite loop on this failure

## Problem

When `git commit` is refused by the global pre-commit hook (staged content adds a `<<<<<<< ` line), git-ai-sync's watch mode catches the resulting `GitError`, logs `Sync failed` + `Continuing to watch...`, and retries every interval forever. Observed in the OpenClaw vault: 12,093 consecutive failures over 9 days (2026-08-21 → 2026-08-30), unbounded log growth (27 MB), 909 commits of silent local drift while every liveness check passed. The tool already ships an AI conflict resolver, but it is wired only to pull-time index conflicts (`"conflicts" in str(e).lower()` on pull errors) — never to hook-refused commits.

## Reproduction

Environment: `git-ai-sync 0.8.0`, global pre-commit hook at `~/.git-hooks/pre-commit` (installed via `core.hooksPath=~/.git-hooks`), repo path NOT under `/tmp` (the hook skips `/tmp/*`, `/var/folders/*`, `/private/tmp/*`, `/private/var/folders/*`).

Steps:
1. `git init ~/Documents/workspaces/git-ai-sync-repro` (real path so the hook fires)
2. Add a bare remote and push one clean commit so pull/push work: `git init --bare <remote>`; `git remote add origin <remote>`; write `clean.md`; `git add clean.md && git commit -m initial && git push -u origin master`
3. Create `conflict.md` containing:
   ```
   section one
   <<<<<<< HEAD
   ours
   =======
   theirs
   >>>>>>> 0a5369ae
   section two
   ```
4. Run `git-ai-sync watch --interval 5 ~/Documents/workspaces/git-ai-sync-repro`

Observed (verbatim, 2026-09-02, reproduced twice from a fresh repo):
```
2026-09-02 22:32:53 INFO     [git_ai_sync.__main__:164] [1] Checking...
2026-09-02 22:32:53 INFO     [git_ai_sync.__main__:172] Local changes detected (2 file(s))
2026-09-02 22:32:53 INFO     [git_ai_sync.__main__:174]   AM .git-ai-sync.lock
2026-09-02 22:32:53 INFO     [git_ai_sync.__main__:174]   A  conflict.md
2026-09-02 22:32:53 ERROR    [git_ai_sync.__main__:226] Sync failed: Failed to commit:
✋ Refusing commit: staged changes contain git merge conflict markers

   Files:
     conflict.md

   Resolve the conflict (pick a side, remove the markers) before committing.
   Emergency override: git commit --no-verify


2026-09-02 22:32:53 INFO     [git_ai_sync.__main__:227] Continuing to watch...
```
The block repeats every interval indefinitely (cycles 1–4 captured in 25 s; no exit, no recovery).

## Expected vs Actual

Expected: per the tool's design (`src/git_ai_sync/conflict_resolver.py` exists precisely for marker content) and the task contract, a resolvable commit refusal auto-recovers via the AI resolver with bounded retries, then fails loudly. `cmd_watch` is documented to keep watching only for transient/retryable failures.

Actual: `cmd_watch`'s `except git_operations.GitError` handler (`src/git_ai_sync/__main__.py:225-227`) logs `Sync failed: {e}` + `Continuing to watch...` and retries forever. The refusal signature (`Refusing commit` / `conflict markers`) is never matched; the resolver is reached only when `"conflicts" in str(e).lower()` on pull errors (`__main__.py:206`, `:301`), and a hook-refused commit never contains that string.

## Why this is a bug

git-ai-sync's stated contract is "sync with AI conflict resolution"; the resolver exists for marker content. A pre-commit hook refusal is a resolvable, mostly mechanical failure (keep both sides — the common vault case), not a permanent condition. Looping silently forever on a resolvable failure violates the contract and hides real wedges: 9 days of failures produced zero user surface while the local mirror drifted 909 commits behind and read-only consumers answered from a stale snapshot.

## Goal

When `git commit` is refused by the pre-commit hook over conflict-marker content, git-ai-sync auto-recovers: it detects the refusal, lists the flagged files, resolves each with the existing AI resolver, re-stages, and retries the commit up to a bounded number of times. If the markers cannot be resolved, sync and watch both exit non-zero with the exact still-flagged files and a pointer to `git-ai-sync resolve`. Watch mode never loops silently forever on this failure.

## Non-goals

- No change to the global pre-commit hook (its marker check stays as-is, including its `/tmp` skip rule)
- No new or second conflict-resolution mechanism — reuse the existing `resolve_conflict_with_claude`
- No change to pull/push behavior, the retry interval, or the watch loop machinery for non-marker failures
- No `_conflicts/` archive exclusion from the hook — tracked separately, not part of this fix

## Constraints

- Do NOT weaken, bypass, or extend the global pre-commit hook (it was correct in the observed cases; the fix belongs in git-ai-sync's recovery)
- Reuse the existing `resolve_conflict_with_claude` — do not write a second resolution mechanism
- Flagged-file detection must use the hook's own pattern verbatim: `git diff --cached --diff-filter=AM -G '^<<<<<<< ' --name-only`
- Preserve existing error-handling shape: `subprocess.run` with `check=False`, `GitError` subclasses before broad `except Exception`
- Type hints on all new functions (mypy strict); pytest conventions only
- Non-marker commit failures and all pull/push behavior must be unchanged

## Acceptance Criteria

- [ ] **Classification:** a unit test asserts that a `GitError` whose message contains `Refusing commit` or `conflict markers` (case-insensitive) is classified as a marker refusal, and one with an unrelated message is not. Evidence: `pytest tests/test_git_operations.py::test_is_marker_refusal` + `::test_not_marker_refusal` pass in `make test`.
- [ ] **Flagged-file listing:** a unit test asserts that `git diff --cached --diff-filter=AM -G '^<<<<<<< ' --name-only` returns only the staged files whose hunks carry a `<<<<<<< ` line (clean staged files excluded). Evidence: `pytest tests/test_git_operations.py::test_get_marker_flagged_files` passes in `make test`.
- [ ] **Auto-recovery commit:** a unit test asserts that a refused commit triggers resolution of each flagged file via the existing resolver, writes and re-stages the resolved content, retries the commit, and returns cleanly on a successful retry. Evidence: `pytest tests/test_conflict_resolver.py::test_commit_with_marker_recovery_success` passes in `make test`.
- [ ] **Bounded retries:** a unit test asserts that after 3 consecutive marker refusals the recovery stops and raises a final error naming the still-flagged files — no 4th attempt. Evidence: `pytest tests/test_conflict_resolver.py::test_commit_with_marker_recovery_bounded` passes in `make test`.
- [ ] **Early stop on unresolved:** a unit test asserts that when the resolver fails on a flagged file, recovery stops immediately and the final error names that file. Evidence: `pytest tests/test_conflict_resolver.py::test_commit_with_marker_recovery_early_stop` passes in `make test`.
- [ ] **Sync exit on final failure:** a unit test asserts that one-shot sync exits non-zero on final marker-refusal failure, logging the flagged files and `Run 'git-ai-sync resolve <path>' to resolve`. Evidence: `pytest tests/test_main.py::test_sync_marker_refusal_exits` passes in `make test`.
- [ ] **Watch exit on final failure:** a unit test asserts that watch mode exits non-zero on final marker-refusal failure (does NOT log `Continuing to watch...` on the refusal path), logging the flagged files and the resolve pointer. Evidence: `pytest tests/test_main.py::test_watch_marker_refusal_exits` passes in `make test`.
- [ ] **Regression:** `make precommit` exits 0 — format + lint + mypy strict + full test suite, all existing tests still passing.
- [ ] **Runtime repro gone (bug verification):** replaying the Reproduction steps against a fresh repro repo with the fixed binary — `git-ai-sync watch --interval 5` — produces an auto-recovered commit with marker-free content and no unbounded refusal loop; evidence: the watch-mode log lines from the run. (Operator-executable, after `uv tool install/upgrade`.)

## Verification

### Container-executable (runs inside the dark-factory YOLO container at prompt time)

- `make precommit` — format + lint + mypy strict + test
- `make test` — full pytest suite

### Operator-executable (runs on the host after PR merge)

- `uv tool install git-ai-sync` (upgrades the fixed version) and re-run the Reproduction steps against a fresh repro repo — confirm watch mode auto-recovers the marker commit and the log shows no unbounded refusal loop.

## Desired Behavior

1. When `git commit` returns non-zero and stderr matches the marker-refusal signature (`Refusing commit` or `conflict markers`, case-insensitive), the failure is classified as a marker refusal rather than a generic `GitError`.
2. On marker refusal, the flagged files are listed with the hook's own pattern: `git diff --cached --diff-filter=AM -G '^<<<<<<< ' --name-only`.
3. Each flagged file is resolved via `resolve_conflict_with_claude` (existing resolver), written back to disk, and re-staged.
4. The commit is retried after resolution. Total attempts are bounded: 3 (1 initial + 2 recovery rounds).
5. If a flagged file fails to resolve, the recovery stops early and the final error names the unresolved file(s).
6. On final failure after bounded retries, the tool logs the exact still-flagged files and `Run 'git-ai-sync resolve <path>' to resolve`, then exits non-zero — in both sync and watch mode. Watch mode does NOT log `Continuing to watch...` for this failure.
7. If the commit succeeds after recovery, the normal sync flow (pull → push) continues unchanged.
8. Non-marker commit failures behave exactly as today.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Commit refused; resolution succeeds on retry | Auto-recovered; commit lands; pull/push continue | None needed |
| Claude resolver fails on a flagged file | Recovery stops early; final failure surface + exit non-zero | Operator runs `git-ai-sync resolve <path>` |
| Markers persist after re-stage (retry refused again) | Counts against the bounded attempts; at 3, final failure surface + exit non-zero | Operator runs `git-ai-sync resolve <path>` |
| Non-marker commit failure | Generic `GitError` path unchanged (watch: `Continuing to watch...`; sync: exit non-zero) | As today |
| Multiple flagged files | All flagged files resolved in one recovery round, re-staged, one retry | None needed |
| Hook message format changes upstream (dotfiles repo) | Classification misses → generic `GitError` path (watch retries forever, as today) | Signature pinned in code + negative test (AC 1); review hook changes in the dotfiles repo before updating the signature |
| Claude resolver unavailable / rate-limited | Recovery stops early with the file(s) named; final failure surface + exit non-zero | Operator runs `git-ai-sync resolve <path>` once the resolver is available |

## Security / Abuse

No new trust boundary: detection matches a fixed refusal string from the pre-commit hook, and resolution reuses the existing `resolve_conflict_with_claude` (same SDK path as `cmd_resolve`). The only new file writes are resolved content to already-staged repo files, gated by the same hook on retry.

## Suggested Decomposition

Single-layer change (one Python package, 3 source files + tests). Prompts in dependency order:

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `git_operations.py`: `MarkerRefusalError`, `is_marker_refusal`, `get_marker_flagged_files` + unit tests | 1, 2, 8 | 1, 2, 8 | — |
| 2 | `conflict_resolver.py`: `resolve_marker_flagged_files` (write + re-stage) + bounded `commit_with_marker_recovery` loop + unit tests | 3, 4, 5 | 3, 4, 5 | prompt 1 |
| 3 | `__main__.py`: wire recovery into `cmd_sync` and `cmd_watch`, final-failure surface + `sys.exit(1)` in both + unit tests | 6, 7 | 6, 7 | prompt 2 |

## Do-Nothing Option

Current state is not acceptable: a silent unbounded retry hides real wedges (9-day OpenClaw incident, 12,093 failures, 27 MB log, 909 commits of silent drift). The interim `git commit --no-verify` unblocks but requires human presence and re-runs risk committing marker-polluted files. The cost of the fix is small because the resolver is already shipped — this work wires it to the one failure mode it currently misses.

## Workaround

Until the fix ships: run `git commit --no-verify` once on the marker-polluted batch (the hook's documented emergency override), then let sync resume — this unblocked the OpenClaw vault on 2026-08-30. For individual files, `git-ai-sync resolve <path>` cleans them before the next sync cycle.
