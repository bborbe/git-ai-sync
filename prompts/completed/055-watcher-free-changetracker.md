---
status: completed
summary: Replaced the fsnotify-based change tracker with a git status --porcelain poller, removed the dependency, and updated tests, docs, and changelog.
execution_id: git-ai-sync-watcherfree-exec-055-watcher-free-changetracker
dark-factory-version: v0.198.17
created: "2026-11-25T17:30:33Z"
queued: "2026-11-25T17:30:33Z"
started: "2026-11-25T17:31:17Z"
completed: "2026-11-25T17:34:25Z"
---

# Replace the fsnotify file watcher with a watcher-free git-status poller

<summary>
- git-ai-sync's watch loop no longer holds an open file descriptor per watched vault file
- Change detection is driven by polling `git status --porcelain` instead of a filesystem watcher
- The process's open-file count no longer grows with the vault's size
- A large vault (tens of thousands of files) can no longer exhaust the process file-descriptor limit
- The watch loop's public behaviour (poll → debounce → sync) is unchanged
- The fsnotify dependency is removed
- Existing tests are updated, and new tests cover change detection both with a fake status source and against a real git repository
</summary>

<objective>
Remove the fsnotify dependency from the change tracker so the watch loop detects changes by polling `git status --porcelain` instead of holding a kqueue watch per watched directory. This bounds the process's file-descriptor use to O(1) — the twelve vaults hold ~68k files and the fsnotify kqueue backend opens a descriptor per file in each watched directory, so the daemon runs at ~70k descriptors and a burst of file creation drove it to the 245,760 `RLIMIT_NOFILE` limit on 2026-11-25, stalling every vault's sync.
</objective>

<context>
Read CLAUDE.md for project conventions and docs/dod.md for the repo's Definition of Done (new functions need tests).
Read docs/launchd-service.md — the NumberOfFiles paragraph requirement 8 rewrites.
Spec 011 (specs/completed/011-launchd-file-descriptor-limits.md) raised the descriptor limits as headroom and noted the watcher replacement "deserves its own spec". Per the Development Guide's business-why test this is a focused single-package technical refactor with no business-level document, so it ships as this prompt; the runtime verification (200k-file burst, 24 h soak, `GitAiSyncSyncStalled` clear) is tracked in the owning vault task's Success Criteria, not here.
Read internal/changetracker/changetracker.go — the current `Tracker` (fsnotify-based).
Read internal/watch/watch.go — the loop that calls `tracker.SecondsSinceLastChange(now)` and decides via quietwindow.
Read internal/quietwindow — the `Decide` debounce (unchanged).
Read internal/git — `runGit` (uses `exec.CommandContext`, never a shell) is the exec seam to reuse.
Read internal/clock — the `Clock` interface the Tracker already takes.

Note: `git status --porcelain` reports tracked changes and untracked files (respecting .gitignore), which is exactly the set git-ai-sync would commit — a closer signal than watching every file including ignored ones.
</context>

<requirements>
1. Keep the `Tracker` type and its public surface unchanged: `New(watchPath string, clk clock.Clock) *Tracker`, `Start(ctx context.Context) error`, `Stop()`, `SecondsSinceLastChange(now time.Time) time.Duration`, and `NeverChanged`. `internal/watch/watch.go` calls exactly these — do not edit watch.go's call sites or the quietwindow semantics.
2. Replace the fsnotify internals: `Start` launches a goroutine that, every `pollInterval` (a `time.Duration` field set to 10s in `New`, so the integration test can use ~50ms), runs `git status --porcelain` through a new `StatusPorcelain(ctx context.Context, repo string) (string, error)` in internal/git/git.go reusing the existing unexported `runGit` (which uses `exec.CommandContext`, never a shell; do NOT use `HasChanges` — it returns a bool and cannot distinguish dirty from different-dirty). Compare the output to the previous poll. When the output differs — empty→dirty or dirty→different-dirty — set `lastChange` to `clk.Now()` under the existing mutex. Do NOT record dirty→clean: that transition is the sync loop's own commit, and recording it resets the quiet window after every successful sync (quietwindow.Decide then skips one cycle because secondsSinceChange ≈ interval equals window). The first poll establishes the baseline and does not count as a change; take it synchronously in `Start` before launching the goroutine, so a write issued immediately after `Start` is not folded into the baseline. If the `git status` call errors, log it with `slog.Default()` (requirement 1 fixes `New`'s signature, so no logger field can be added) and leave `lastChange` untouched — never treat an error as a change or as a clean tree.
3. `SecondsSinceLastChange` returns `now.Sub(lastChange)`, or `NeverChanged` when no change has been recorded — same contract as today.
4. `Stop` stops the goroutine, is safe to call more than once (keep the `sync.Once`), and waits for the goroutine to exit (keep the `sync.WaitGroup`). The poll goroutine selects on `t.done` and its ticker, and runs git through `runGit`/`exec.CommandContext`, so `Stop` cannot block on a hung `git status`.
5. Remove the `github.com/fsnotify/fsnotify` import and every fsnotify-only helper: `addRecursive`, `handleWatcherEvent`, `run`, `ShouldIgnore` and `HandleEvent`. The only remaining references to `ShouldIgnore`/`HandleEvent` are their own tests (`TestShouldIgnore`, `TestHandleEventResetsOnlyForOrdinaryFiles`) — delete those tests with the helpers. Keep `NeverChanged`. Update the package doc comment, which still says "newest qualifying filesystem change".
6. After the code no longer imports fsnotify, run `go mod tidy` so it drops from go.mod and go.sum. (Order matters: remove the import first, then tidy — a tidy before the import is gone leaves the dep in place.)
7. Rewrite internal/changetracker/changetracker_test.go:
   a. Delete `TestShouldIgnore` and `TestHandleEventResetsOnlyForOrdinaryFiles` (they test removed helpers). Replace `TestStartStopDeliversOrdinaryEvents` — it writes into a non-git `t.TempDir()` and expects an fsnotify event within 2s; under the poller `git status` exits 128 there, so it can never pass. Keep a small separate test that calls `Stop()` twice and returns (the `sync.Once` contract in req 4).
   b. Unit — extract the per-poll body into a `poll(ctx context.Context)` method the ticker calls, and drive `poll` directly from the test through an injected git-status source (a `gitStatus func(ctx context.Context, repo string) (string, error)` field on the Tracker, defaulting to `git.StatusPorcelain`), so the test never waits on a ticker. Cover: empty→empty (no change), empty→dirty (change recorded), dirty→same-dirty (no change), dirty→different-dirty (change recorded — the reason `HasChanges` is not reused), dirty→clean (no change — the sync's own commit), and a source that returns an error (no change, `lastChange` untouched).
   c. Integration (boundary) — create a real temp git repo (`git init`, `git config user.email test@example.com`, `git config user.name "Test User"`, one commit — same fixture as internal/conflict/conflict_test.go), start the Tracker with a ~50ms pollInterval, write a tracked file, and assert `SecondsSinceLastChange` drops below `NeverChanged` within a bounded wait. This traverses the real `git status` subprocess, which the injected unit test does not.
8. Rewrite the `NumberOfFiles` rationale in docs/launchd-service.md (the paragraph that explains 65536/245760 by `internal/changetracker` being built on `fsnotify`). Keep the history (the 4096 outage happened under the watcher) and the numbers (spec 011 pins them, no knob is added), but the paragraph must no longer claim fsnotify is the current mechanism.
9. Add one bullet to `CHANGELOG.md` under `## Unreleased`, creating that section above `## v0.20.1` if it is absent (the releaser rewrites it into the version on merge) — e.g. `- chore: replace the fsnotify change tracker with a git status --porcelain poller, bounding file-descriptor use`.
10. Before finishing, re-run the `<verification>` command and confirm it passes; walk requirements 1-9 against the diff.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do not change internal/watch/watch.go's call sites or quietwindow behaviour.
- Existing tests must still pass; `make precommit` must pass.
- No new CLI flag or config key.
- Accepted behaviour change: the watcher's noise filter is gone; detection follows each vault's `.gitignore`. Do not reimplement the filter in the poller.
- Repo-relative paths only.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass. Then confirm the dependency is gone: `! grep -q fsnotify go.mod`, `! grep -q fsnotify go.sum`, and `! grep -rq fsnotify internal/`.
</verification>
