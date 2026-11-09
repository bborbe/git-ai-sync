---
status: verifying
approved: "2026-11-09T16:26:19Z"
generating: "2026-11-09T18:47:02Z"
prompted: "2026-11-09T19:09:18Z"
verifying: "2026-11-09T19:26:47Z"
branch: dark-factory/go-sync-loop-prototype
---

## Summary

- The repo gains a Go module alongside the Python package: a `git-ai-sync` binary with `sync` and `watch` subcommands, configuration, logging, git operations, a filesystem change tracker, the decaying quiet-window gate, and an instance lock.
- The Go watcher reproduces the Python tool's steady-state timing exactly: poll every interval, skip while files change, halve the required quiet window on each consecutive skip, and force a sync once the maximum wait has elapsed since the last success.
- One sync cycle stages, commits, pulls, then pushes — the pull merging by default and rebasing when asked.
- Every timing rule is a pure, clock-injected decision unit, so the decay sequence and the maximum-wait boundary are asserted by unit tests rather than by waiting.
- The Python package, its tests, and its existing Makefile targets are untouched; the Go module adds its own test and precommit targets beside them, and CI runs them.

## Problem

Twelve live vaults run the Python watcher, each a separate interpreter process. The rewrite to a single Go binary is blocked on nothing but proof: nobody will cut twelve live vaults over to a port whose debounce decays differently, whose maximum-wait cap fires late, or whose sync cycle silently skips the push. The behaviour is small but easy to get wrong — the window halves on each skip, and a vault that never goes quiet is rescued only by the cap — so it must be reproduced and demonstrated against a throwaway repository before any vault depends on it.

## Goal

A Go binary that, pointed at a scratch clone, keeps that clone in sync with its remote using the same poll / debounce / decay / maximum-wait semantics as the Python tool, refuses to run as a second instance on the same repository, and proves each of those rules with a test or an observable run.

## Non-goals

- Conflict resolution of any kind. A pull that conflicts is reported and the process stops; no AI resolution, no marker recovery, no path sanitisation.
- Multi-repo supervision, launchd plists, brew cask packaging, and the cutover of the twelve live vaults.
- Retiring the Python implementation, or changing any Python behaviour.
- Pushgateway metrics, the consecutive-failure attention card, and the notifier.
- Any new sync capability: no multi-remote, no partial fetch, no changed conflict heuristics.

## Assumptions

- **The YOLO container has a Go toolchain.** Verified 2026-11-09 against the running image: `go version go1.27.3 linux/arm64`, with `git` and `gofmt` present. The container-executable verification rung depends on this.
- **The first Go build fetches modules over the network.** `.dark-factory.yaml` mounts only `~/.cache/uv`, so there is no pre-warmed Go module cache. Tests must not need the network; the first `go build` may.
- **The ported behaviour is settled by two existing specs** — `specs/in-progress/001-instance-locking.md` (the lock contract) and `specs/in-progress/006-watch-quiet-window-decay.md` (decay, cap and log format). Both are at `status: verifying`, i.e. implemented but not yet signed off. This spec ports them; it does not redefine them. If one of them changes during verification, this spec follows it.

## Acceptance Criteria

- [ ] The Go module builds and its tests pass — evidence: `go build ./...` exits 0 and `go test ./... -count=1` exits 0.
- [ ] The decay rule is asserted, not merely implemented — evidence: a test asserts `decayed_window(30, 0) == 30`, `decayed_window(30, 1) == 15`, `decayed_window(30, 2) == 7.5` and `decayed_window(30, 3) == 3.75`, plus a property assertion over k = 0..10 that each step is exactly half the previous one. Four pinned points and a range rule, because three pinned points alone are satisfied by a three-case lookup table. The mutation clause: replacing the decay expression with a constant `interval` in the production code makes `go test ./...` fail; the failing output is pasted into the PR body, then the mutation is reverted — a reverted experiment leaves no replayable artifact, so the PR body is where it must live.
- [ ] The maximum-wait boundary is asserted with an injected clock — evidence: a test drives the tracker so the cap fires at exactly 300 s elapsed since the last success and does not fire at 299 s, with no real sleeping.
- [ ] A success resets both the decay and the cap clock — evidence: after two skip records followed by a success record, a test asserts the window is back to the full interval and elapsed-since-success is 0 at the same clock reading.
- [ ] A failed cycle leaves the decay and the cap clock untouched — evidence: after a failure record, a test asserts the window is unchanged from before the failure and elapsed-since-success keeps growing. This is the rule that stops a flapping remote from silently shortening the quiet window, and it is otherwise satisfied by omission.
- [ ] The window boundary uses the same comparison as the cap — evidence: a test asserts a change exactly at the window boundary syncs (the comparison is "at least", matching the Python `>=`), not only one strictly older.
- [ ] One sync cycle performs stage → commit → pull → push — evidence, all strategy-independent: after one cycle on a clone holding a local edit and a new commit on the remote, (a) `git -C <clone> status --porcelain` is empty; (b) `git -C <clone> rev-list --count origin/master..HEAD` is 0, so nothing is unpushed; (c) `git -C <clone> rev-parse HEAD` equals `git -C <remote> rev-parse master`; (d) the edited content is on the remote — `git -C <remote> show master:<path>` matches the edit; (e) an auto-commit exists in the clone's history — `git -C <clone> log --format=%s | grep -c '^auto:'` returns 1; (f) the remote-only commit is an ancestor of the clone's HEAD. Clause (e) asserts the commit is *in history*, not that it is HEAD: under merge the pull creates a merge commit, so HEAD is the merge, and a HEAD-subject assertion would fail a correct implementation.
- [ ] A pull-only implementation cannot satisfy the previous criterion — evidence: clause (d) fails against a bare `git pull` (the edit stays uncommitted in the worktree) and clause (e) returns 0 (no auto-commit exists). The test asserts both directly rather than inferring them from HEAD equality, which a bare pull also satisfies.
- [ ] `--strategy rebase` is selectable and still lands the remote commit — evidence: the same scratch-clone test run with `--strategy rebase` passes clauses (a)–(f) unchanged. The assertions deliberately avoid commit counts: a merge cycle adds three commits (local, remote, merge) and a rebase cycle adds two (local, replayed), so no single count delta holds for both.
- [ ] The watch loop skips while files change and logs the decaying window — evidence: with `--interval 4` and a writer touching a file every 100 ms, three consecutive skip lines read `quiet window 4.0s`, then `quiet window 2.0s`, then `quiet window 1.0s`, the first of them prefixed `[1]`, and the clone's commit count is unchanged across those three cycles. The interval is 4 s rather than 1 s because one decimal place renders the third step of a 1 s interval as `0.2s`, which reads as a rounding artefact; 4 → 2 → 1 is unambiguous.
- [ ] The maximum-wait cap forces a sync — evidence: with `--interval 1`, `GIT_AI_SYNC_MAX_WAIT=3` and a writer touching a file every 100 ms, the line `Maximum wait 3s reached — syncing regardless of quiet` appears, and the clone's commit count increases after it.
- [ ] The cap line is suppressed when the window would have accepted anyway — evidence: a test asserts that a cycle where the cap fires but the newest change is older than the window in force emits no cap line. This mirrors the Python gate and stops a port logging the cap on every forced cycle.
- [ ] The change tracker ignores its own noise — evidence: a test asserts that writing to a `.tmp` file, to a `*~` backup, to a `.swp` file, to a dot-prefixed name, and to anything under `.git/` leaves seconds-since-change unchanged, while an ordinary file write resets it. Without the `.git/` rule the watcher's own git operations reset its quiet window and the loop degrades to cap-rate syncing.
- [ ] The watch loop pulls on a clean worktree — evidence: on a scratch clone with no local edits and a new commit on the remote, after one accepted cycle `git -C <clone> rev-list --count HEAD..origin/master` returns 0 and the remote commit is an ancestor of HEAD; the one-shot `sync` on the same clone reports no changes and returns without pulling. Every other sync AC runs on a dirty clone, so without this one an implementation that pulls only when it committed passes the whole set while a quiet vault never receives a remote change.
- [ ] The binary is a port, not a wrapper around the Python tool — evidence: with the Go watcher running, `pgrep -f 'python.*git_ai_sync' | wc -l` returns 0, `env PATH=/usr/bin:/bin ./git-ai-sync sync <scratch-clone>` completes a cycle and exits 0 with no Python entry point reachable, and `grep -rn 'python' --include='*.go' cmd/ internal/` returns 0 lines — the binary shells out to `git` only, never to an interpreter. Without this clause every other AC is satisfied by a binary that execs the existing CLI, and the 20 MB bound is satisfied by measuring only the Go parent while the Python child holds the memory.
- [ ] A second `watch` on the same repository refuses — evidence: with one `watch` running on the scratch clone, a second `watch` on the same repository exits non-zero within 5 s while the first is still running.
- [ ] The lock file is private (operator rung) — evidence: `stat -f '%Lp' <clone>/.git-ai-sync.lock` prints `600` on macOS, including when the lock file already existed at a looser mode before the run.
- [ ] `--version` prints a version and exits 0 — evidence: `./git-ai-sync --version` exits 0 and prints exactly one line.
- [ ] The binary is static and runs on this machine (operator rung) — evidence: `CGO_ENABLED=0 go build ./cmd/git-ai-sync` exits 0 and `file ./git-ai-sync` matches `Mach-O 64-bit executable arm64`.
- [ ] The Go Makefile targets exist and work — evidence: `grep -nE '^(test-go|precommit-go):' Makefile` returns two lines, `make test-go` exits 0, `make precommit-go` exits 0, and `diff <(git show origin/master:Makefile | grep -c 'uv ') <(grep -c 'uv ' Makefile)` is empty — the Go targets add no Python tooling invocation.
- [ ] CI runs the Go module — evidence: `git show origin/master:.github/workflows/ci.yml` contains a `setup-go` step and a `make test-go` step. The workflow file is landed by the operator with their own credentials and is **never part of this diff**: the CI GitHub App may not modify workflow files, so any commit touching `.github/workflows/` is rejected at push with `refusing to allow a GitHub App to create or update workflow`. Do not create, edit or delete anything under `.github/`; the box is met by the file already being on `origin/master` when the module lands.
- [ ] The Python tool is untouched — evidence: `git diff origin/master -- src/ tests/ pyproject.toml uv.lock` is empty, and `git diff origin/master -- Makefile | grep -cE '^-[^-]'` returns 0 — additive-only, no existing target body or line removed. The regex matches any removed content line, not just removed headers: rewriting the body of `test:` emits `-<tab>old` lines that a header-only pattern would miss.
- [ ] The stale strategy line in the repo's own documentation is corrected — evidence: `grep -c 'never merge' CLAUDE.md` returns 0, `grep -c 'pull --rebase' CLAUDE.md` returns 0, and `grep -n 'pull --no-rebase' CLAUDE.md` returns at least one line. Both stale lines are checked: the "always rebase, never merge" design decision and the `git_operations.py` description both name rebase, and fixing only one leaves the next reader misled.
- [ ] Idle memory stays bounded (operator rung) — evidence: `ps -o rss= -p <pid>` sampled once a minute for ten minutes against the idle watcher, the maximum of the ten samples is at or below 20480 KB. The bound is a third of the ~77 MB a Python watcher process holds today, so it is a meaningful target rather than an arbitrary one, and it is the number that makes the cutover worth doing: twelve Python processes today, one Go process after.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `go build ./...` — the module compiles
- `go vet ./...` — no vet findings
- `go test ./... -count=1` — unit tests pass, including the decay, reset, boundary and cap-suppression assertions
- `make test-go` and `make precommit-go` — both exit 0
- `grep -nE '^(test-go|precommit-go):' Makefile` — both targets present
- `git diff origin/master -- src/ tests/ pyproject.toml uv.lock` — empty, proving the Python package is untouched

### Operator-executable (runs on the host after merge)

- `./git-ai-sync sync <scratch-clone>` — one cycle against a throwaway clone holding a local edit and a remote-only commit
- `./git-ai-sync watch <scratch-clone> --interval 4` with a 100 ms writer — the decay sequence appears in the log
- `./git-ai-sync watch <scratch-clone> --interval 1` with `GIT_AI_SYNC_MAX_WAIT=3` — the cap line appears and a commit lands
- `./git-ai-sync watch <scratch-clone>` on a clean clone with a new remote commit — the remote commit arrives without any local edit
- a second `./git-ai-sync watch <scratch-clone>` — exits non-zero while the first runs
- `stat -f '%Lp' <scratch-clone>/.git-ai-sync.lock` — prints `600`
- `pgrep -f 'python.*git_ai_sync' | wc -l` while the Go watcher runs — returns 0
- `CGO_ENABLED=0 go build ./cmd/git-ai-sync && file ./git-ai-sync` — a Mach-O arm64 binary
- `ps -o rss= -p <pid>` ten times over ten minutes — the idle watcher stays under 20 MB

## Desired Behavior

1. **The module coexists with the Python package, and is configured from flags and `GIT_AI_SYNC_` environment variables.** A `go.mod` at the repo root, the binary under `cmd/git-ai-sync`, supporting packages under `internal/`. The Python `src/`, `tests/`, `pyproject.toml` and `uv.lock` are byte-identical to before the change, and the binary never invokes the Python implementation at runtime — the Python tree stays only for the vaults not yet cut over. Config: `interval` (default 30 s), `max_wait` (default 300 s), `commit_prefix` (default `auto`), `strategy` (default `merge`), `log_level` (default `INFO`). A flag overrides the environment variable. The finished binary runs the watcher as one process holding under 20 MB RSS when idle — the whole point of replacing twelve interpreter processes.

2. **The quiet window decays.** The required quiet window for a cycle is `interval × 0.5^consecutive_skips`. The first cycle after a success requires the full interval; each consecutive skip halves it. A successful sync resets the skip count to zero and restarts the maximum-wait clock. A failed cycle neither resets nor advances the skip count.

3. **The maximum-wait cap overrides the window.** A cycle is accepted when either the newest change is at least as old as the window in force, or `max_wait` seconds have elapsed since the last successful sync. Both comparisons are "at least", matching the Python `>=`, so a change exactly at a boundary is accepted rather than deferred.

4. **The skip and cap decisions are logged in the Python tool's format.** A skipped cycle logs `[<iteration>] Skipping - files changed <N>s ago (still editing; quiet window <W>s)` with one decimal place on both numbers. The iteration counter is **one-based** — the first emitted cycle is `[1]`, because the Python tool increments before logging. A cap-forced cycle logs `[<iteration>] Maximum wait <M>s reached — syncing regardless of quiet`, with the em dash. The cap line is emitted **only when the cap is what decided the cycle** — that is, when the cap fired *and* the newest change was still newer than the window in force. When the window would have accepted the cycle anyway, the line is suppressed and the cycle proceeds silently, because the cap was not the reason. These strings are the operator's only view of which mechanism acted, and existing log-scraping runbooks match them from the iteration prefix onward.

5. **A sync cycle is stage → commit → pull → push.** When the worktree is dirty: stage everything, commit with the configured prefix, then pull, then push. When the worktree is clean: pull, and push only if the local branch is ahead of its remote. The pull uses `git pull --no-rebase` under `--strategy merge` (the default) and `git pull --rebase` under `--strategy rebase`. The push is not skipped when the commit succeeded — a cycle that commits and does not push has not synced. The one-shot `sync` subcommand differs from the watch loop here, and the port keeps the difference: on a clean worktree `sync` reports "no changes to sync" and returns without pulling, while the watch loop pulls on every accepted cycle. The watch loop is the steady-state behaviour this spec reproduces; `sync` is a manual one-shot. Commit refusal by a pre-commit hook is logged and the pull still runs, so a local commit failure cannot suppress an inbound sync; the port does not distinguish the Python tool's marker-refusal class, because marker recovery is out of scope, so it treats every commit refusal as the continuing case rather than exiting.

6. **Watch mode runs the loop at the poll interval, and its change tracker ignores its own noise.** It validates that the path is a git repository, acquires the instance lock, starts tracking filesystem changes, then loops: sleep one interval, read seconds-since-last-change, decide, and either log the skip and continue or run the sync cycle. The loop is cancellable by SIGINT and SIGTERM, releasing the lock on exit. The tracker ignores directories, anything under `.git/`, `*~` backups, `*.swp`, `*.tmp`, and dot-prefixed names — without the `.git/` rule the watcher's own git operations reset its quiet window and the loop degrades to cap-rate syncing.

7. **A second instance on the same repository refuses.** The lock is an exclusive non-blocking `flock` on `<repo>/.git-ai-sync.lock`, created or corrected to mode 0600 on every acquisition (a pre-existing file left at a looser mode is tightened, as the Python tool does), holding the holder's PID. A process that cannot take the lock exits non-zero and names the holding PID when it can read one.

8. **Every timing input is injectable.** The quiet-window tracker takes its clock readings as parameters and reads no clock of its own, so tests advance time without sleeping. The watch loop accepts an injected clock, so the decay sequence can be driven deterministically instead of by waiting.

## Constraints

- The Python implementation, its test suite, `pyproject.toml` and `uv.lock` must not change. The Python `test`, `precommit`, `format`, `check` and `build` targets keep their current bodies.
- The Go binary must not exec the Python implementation or any interpreter at runtime — no `os/exec` of a Python entry point, in the watch loop or in the one-shot `sync`. `os/exec` of `git` is expected; `os/exec` of anything else is a defect.
- New Makefile targets are additive only: `test-go` and `precommit-go`. `precommit-go` runs format, vet and test for the Go module and must not invoke `uv`.
- `CGO_ENABLED=0` — the binary is static.
- The log strings in Desired Behavior 4 are frozen from the iteration prefix onward: existing runbooks and this task's probes match them literally.
- Go conventions follow bborbe's Go standards: `github.com/bborbe/errors` for wrapping, `log/slog` for logging, no package-level mutable state — the lock and the tracker are constructed and injected. The repo has no Go standards of its own; its `docs/dod.md` is Python-only and is the dark-factory validation prompt, so the Go equivalent of that checklist is: `gofmt` clean, `go vet` clean, `go test ./...` green, exported identifiers documented.
- The Go module must not require the network at test time; every git interaction in tests runs against a locally created repository. The scratch repository carries a `/.git-ai-sync.lock` ignore entry, because the lock file is created in the worktree root and a freshly created repo would otherwise stage and commit it on the first cycle.
- **`log_level` honours the unprefixed `LOG_LEVEL`**, matching the Python tool's argparse default; the other four settings are `GIT_AI_SYNC_`-prefixed. The port does not invent a prefixed alias for it.
- **`CLAUDE.md:116` is stale and is corrected in the same change.** It reads "stage → commit → pull --rebase → push (always rebase, never merge)", while the code default is `merge` (`__main__.py:43`, `GIT_AI_SYNC_STRATEGY` defaulting to `"merge"`) and all twelve live watchers run `--strategy merge`. The code wins; the line is corrected so the next reader is not sent the wrong way.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Pull conflicts (merge or rebase) | The cycle reports the conflict and the process stops non-zero; no resolution is attempted | Operator resolves by hand in the clone; out of scope by design |
| Commit refused by a pre-commit hook | The refusal is logged and the pull still runs, so a local commit failure cannot suppress an inbound sync; the loop continues rather than exiting | Fix the offending content; the next cycle commits it |
| Push rejected (non-fast-forward) | The cycle reports the failure and continues the loop; no force-push | The next cycle pulls first, then pushes |
| Process dies after commit, before push | The commit survives on the local branch; nothing is pushed | The next cycle's pull-then-push lands it — `git rev-list --count origin/master..HEAD` returns 0 |
| Remote unreachable | The cycle reports the failure and continues the loop; no data is lost | Next cycle retries; the worktree state is unchanged |
| Lock already held | The second process exits non-zero naming the holder's PID | `kill <pid>`, then the next `watch` on that repository acquires the lock and runs |
| Path is not a git repository | The process exits non-zero before acquiring the lock | Point at the repository root |
| `max_wait` below `interval` | The debounce is effectively disabled; the process warns at startup that every cycle will sync | Set `max_wait` above `interval` |
| Lock file pre-exists at a loose mode | The mode is corrected to 0600 on acquisition | None needed — corrected automatically |
| Wall clock steps backwards (NTP correction, suspend) | Elapsed-since-success is measured on a monotonic source, so the cap cannot be starved by a backwards step; the tracker's clock reading is injected and monotonic | None needed — monotonic by construction |

## Security / Abuse

- The lock file is created or corrected to `0600` and contains only a PID.
- The tool runs `git` against a caller-supplied path. It resolves the path and verifies a `.git` exists before doing anything; it never invokes a shell, so a path with spaces or metacharacters is passed as one argv element, not interpolated.
- No credentials are read, stored or logged. Authentication is git's own.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Module scaffold: `go.mod`, the binary with `sync`/`watch` stubs and `--version`, config loading, slog setup, `test-go` + `precommit-go` Makefile targets, CI job, corrected `CLAUDE.md` strategy line | 1 | 1, 18, 19, 20, 21, 22, 23 | — |
| 2 | Git operations: repo discovery, status, stage, commit, pull merge/rebase, push, ahead-of-remote | 5 | 7, 8, 9 | prompt 1 |
| 3 | Quiet window + change tracker: decayed window, tracker with reset, failure and boundary semantics, injected clock, filesystem change detection with the ignore set | 2, 3, 6, 8 | 2, 3, 4, 5, 6, 13 | prompt 1 |
| 4 | Instance lock: flock on `.git-ai-sync.lock`, mode 0600 with correction, PID, refusal path | 7 | 16, 17 | prompt 1 |
| 5 | Wire the watch loop and sync command: poll, decide, log skip/cap with suppression, run the cycle, clean-worktree pull, signal handling, no Python delegation | 1, 4, 5, 6 | 10, 11, 12, 14, 15, 24 | prompts 2, 3, 4 |

Rationale: prompt 1 establishes the module and the build targets every later prompt builds on; prompts 2, 3 and 4 are independent of each other and can run in parallel after it; prompt 5 is the only one that wires them together, so it comes last.

## Alternatives Considered

- **Port only the watch loop and keep Python for launchd, metrics and conflict resolution.** Rejected: it leaves two processes per vault, which is the memory cost the rewrite exists to remove, and it splits the sync cycle across two languages so neither can be tested end to end.
- **Skip the prototype and port straight onto a live vault.** Rejected: the debounce and cap semantics are exactly what a rewrite gets wrong, and a wrong port on a live vault either syncs mid-edit or stalls silently.

## Do-Nothing Option

The twelve vaults keep running the Python watcher: twelve interpreter processes and a rewrite that never gets a proven foundation. The cost of not doing this is not a bug — it is that the cutover task cannot start, because there is nothing to cut over to.
