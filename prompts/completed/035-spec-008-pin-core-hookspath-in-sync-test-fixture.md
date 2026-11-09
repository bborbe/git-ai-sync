---
status: completed
spec: [008-pin-core-hookspath-in-sync-test-fixture]
summary: Pinned core.hooksPath to each fixture repository's own absolute hooks directory (clone, intermediate clone, and bare remote) in internal/sync/sync_test.go, keeping both hook-based tests intact and environment-independent, and added an Unreleased CHANGELOG entry.
execution_id: 9a97ef56-2b4c-51be-9bdb-c642b8358a78-exec-035-spec-008-pin-core-hookspath-in-sync-test-fixture
dark-factory-version: dev
created: "2026-11-09T22:20:00Z"
queued: "2026-11-09T22:20:04Z"
started: "2026-11-09T22:20:06Z"
completed: "2026-11-09T22:21:32Z"
branch: dark-factory/pin-core-hookspath-in-sync-test-fixture
---

# Pin `core.hooksPath` in the sync test fixture

<summary>
- The two sync tests that rely on git running a hook stop depending on the developer machine's global git config.
- The test fixture pins each repository it creates to its own hooks directory.
- The clone, the intermediate clone, and the bare remote are all pinned, so both hook-based tests pass on a hostile machine.
- No test is deleted, skipped, renamed, or weakened; both still fail if the behaviour they cover regresses.
- The fix is confined to the sync test file; no production code, Makefile, Python source, or CI workflow changes.
- The suite passes both with a normal global config and with `GIT_CONFIG_GLOBAL=/dev/null`.
- The fixture does not set `GIT_CONFIG_GLOBAL` and does not modify the machine's configuration.
</summary>

<objective>
Make `go test ./... -count=1` pass on a machine whose global git config sets `core.hooksPath`, by pinning `core.hooksPath` in every repository the `internal/sync` test fixture creates. **Why:** today `TestCommitRefusalStillPulls` and `TestPushRejectedKeepsLocalCommit` write hooks that a global `core.hooksPath` silently redirects away from, leaving the suite permanently red on the machine that owns the repo and hiding real regressions.
</objective>

<context>
Read the approved spec at `specs/in-progress/008-pin-core-hookspath-in-sync-test-fixture.md` — the Problem, Goal, Constraints, and Failure Modes sections are the contract for this prompt.

Read `internal/sync/sync_test.go` in full before editing. The relevant pieces are:
- `newFixture` calls `configure(t, f.clone)` after cloning (currently around the `runGit(t, root, "clone", ...)` line).
- `addRemoteCommit` calls `configure(t, other)` for the intermediate clone it creates.
- `configure(t, dir string)` currently sets `user.email`, `user.name`, and `commit.gpgsign`.
- `runGit(t, dir string, args ...string) string` runs git in `dir`, trims stdout, and calls `t.Fatalf` on error — use it for every git invocation.
- `TestCommitRefusalStillPulls` writes `filepath.Join(f.clone, ".git", "hooks", "pre-commit")`.
- `TestPushRejectedKeepsLocalCommit` writes `filepath.Join(f.remote, "hooks", "pre-receive")`.

Read `Makefile` to confirm the Go gate is `precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`). Do NOT run `make precommit`: in this repo that target is Python-only and never compiles or runs a Go file.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for test conventions; this package uses stdlib `testing`, not Ginkgo/Gomega.

Verified git behaviour this fix relies on: `git rev-parse --absolute-git-dir` returns `<clone>/.git` for a clone and `<remote>` (the bare repo root) for a bare repository, so joining `hooks` onto it yields `<clone>/.git/hooks` and `<remote>/hooks` — exactly the directories the two tests write their hooks into. `git rev-parse --git-path hooks` returns the *effective* `core.hooksPath` (the global value being overridden), so it must NOT be used.
</context>

<requirements>
1. Extend the existing `configure` helper in `internal/sync/sync_test.go` so that, after the existing `user.email` / `user.name` / `commit.gpgsign` lines, it pins `core.hooksPath` to the repository's own hooks directory as an absolute path:

   ```go
   func configure(t *testing.T, dir string) {
       t.Helper()
       runGit(t, dir, "config", "user.email", "test@example.com")
       runGit(t, dir, "config", "user.name", "Test User")
       runGit(t, dir, "config", "commit.gpgsign", "false")
       // Pin core.hooksPath to this repository's own hooks directory so a global
       // core.hooksPath on the developer's machine cannot redirect git away from
       // the hooks these tests write. --absolute-git-dir yields <clone>/.git for a
       // clone and <bare> for a bare repository; --git-path hooks would return the
       // effective (global) hooks path and reproduce the bug.
       hooksPath := filepath.Join(runGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
       runGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
   }
   ```

   Keep the `configure(t *testing.T, dir string)` signature unchanged so the two existing call sites keep compiling. Use `runGit` for the `rev-parse` and `config` invocations. Do not add new imports: `path/filepath` is already imported.

2. Pin the bare remote as well. In `newFixture`, immediately after `runGit(t, root, "init", "--bare", f.remote)` (and its `symbolic-ref` line), add:

   ```go
   configure(t, f.remote)
   ```

   This is the third `configure` call site. Both repositories that receive hooks — the clone (pinned by the existing `configure(t, f.clone)` call) and the bare remote (pinned by this new call) — must end up with their own absolute hooks path. Pinning only the clone leaves `TestPushRejectedKeepsLocalCommit` red.

3. Leave `TestCommitRefusalStillPulls` and `TestPushRejectedKeepsLocalCommit` unchanged: do not delete, skip, rename, or relax them, and do not change the hook paths they write. After step 2, `<clone>/.git/hooks` and `<remote>/hooks` are the pinned paths, so the hooks they write are the ones git consults.

4. Do NOT set `GIT_CONFIG_GLOBAL` anywhere in `internal/sync/` (no `os.Setenv`, no `t.Setenv`, no `TestMain` override) and do NOT run `git config --global`. The fix must remove the dependency on the machine's configuration, not invert it.

5. Do NOT add new tests. The spec's Non-goals forbid broadening `internal/sync` coverage; the two existing tests plus the hostile-config verification below are the proof.
</requirements>

<constraints>
- Test files only. The diff may touch `internal/sync/sync_test.go` and any shared test helper under `internal/sync/`. It must NOT touch `internal/sync/sync.go`, any other production file, the `Makefile`, `src/`, `tests/`, `pyproject.toml`, or `uv.lock`.
- `core.hooksPath` must be pinned as an **absolute** path — derive it from `git rev-parse --absolute-git-dir` joined with `hooks`. A relative value resolves unpredictably in a bare repo and reintroduces the bug.
- Do NOT use `git rev-parse --git-path hooks`; it returns the effective (global) hooks path and reproduces the bug.
- No test may be deleted, skipped, renamed, or have its assertions relaxed.
- Do not modify the machine's global git configuration, and do not set `GIT_CONFIG_GLOBAL` in the test code. (A one-off verification-shell run may set it to simulate a hostile config — see verification — but that is not a test dependency.)
- Nothing may touch `.github/workflows/`.
- Use the existing `runGit` helper for git invocations so failures are reported consistently with the rest of the file.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root.

1. The Go gate (not `make precommit`):

   ```
   make precommit-go
   ```

   Must exit 0.

2. Prove the fix behaviourally under a hostile global config, without modifying the tests:

   ```
   printf '[core]\n\thooksPath = /nonexistent\n' > /tmp/hostile.gitconfig
   GIT_CONFIG_GLOBAL=/tmp/hostile.gitconfig go test ./internal/sync -run 'TestCommitRefusalStillPulls|TestPushRejectedKeepsLocalCommit' -count=1
   ```

   Must exit 0. Before this change the same command fails with `CommitErr is nil, want the refusal` and `Run succeeded, want a push error`; after it, both pass.

3. The suite passes both ways, so the fix does not merely invert which environment it depends on:

   ```
   go test ./... -count=1
   GIT_CONFIG_GLOBAL=/dev/null go test ./... -count=1
   ```

   Both must exit 0.

4. Negative-evidence checks the spec requires:

   ```
   grep -rn 'GIT_CONFIG_GLOBAL' internal/sync/ | wc -l
   grep -c 'hooksPath' internal/sync/sync_test.go
   grep -c 'absolute-git-dir' internal/sync/sync_test.go
   ```

   Expected: the `GIT_CONFIG_GLOBAL` grep prints `0`; the `hooksPath` count is at least `1`; the `absolute-git-dir` count is at least `1`.

5. Scope check — only the test file changed:

   ```
   git status --porcelain -- internal/ Makefile src/ tests/ pyproject.toml uv.lock
   ```

   Expected: only `internal/sync/sync_test.go` appears; no production file, Makefile, Python source, or lockfile is listed.

Note: the confirmation against the developer's real global config (`cd ~/Documents/workspaces/git-ai-sync && go test ./... -count=1`) is an operator-side step after merge and is not run here.
</verification>
