---
status: completed
approved: "2026-11-09T21:55:17Z"
generating: "2026-11-09T22:09:56Z"
prompted: "2026-11-09T22:15:13Z"
verifying: "2026-11-09T22:21:32Z"
completed: "2026-11-09T22:21:40Z"
branch: dark-factory/pin-core-hookspath-in-sync-test-fixture
---

## Summary

- Two tests in `internal/sync` write git hooks and rely on git running them
- On a machine whose global git config sets `core.hooksPath`, those hooks are silently ignored and both tests fail
- The suite is therefore red on the developer's machine and green in CI, which trains readers to ignore it
- The test fixture pins its own hooks path, so the tests stop depending on machine configuration
- The tests keep testing what they claim — none is deleted, skipped or weakened
- The change touches test files only: no production code, no Makefile target, no Python source
- A red suite stops hiding real regressions, and the prototype's `go test ./...` criterion becomes provable on the machine that owns the repo

## Problem

`TestCommitRefusalStillPulls` writes an executable `pre-commit` hook into `<clone>/.git/hooks` and asserts that the commit is refused; `TestPushRejectedKeepsLocalCommit` writes a `pre-receive` hook into the fixture's bare remote at `<remote>/hooks` and asserts that the push is rejected. Both depend on git consulting those directories.

This machine sets `core.hooksPath` globally, so git consults a different directory, neither hook runs, and the two tests fail with `CommitErr is nil, want the refusal` and `Run succeeded, want a push error`. The same suite passes under `GIT_CONFIG_GLOBAL=/dev/null`, which isolates the cause to git configuration rather than to the code under test.

The consequence is worse than two red tests. `go test ./...` is permanently red on the machine that owns this repo, so a genuine regression is indistinguishable from the two known failures. It also blocks the port's own success criterion — the Go watcher cannot be declared correct while its suite cannot be run clean — and it makes the tests environment-dependent: a test whose result depends on the developer's global git config is not testing the product.

## Goal

`go test ./...` passes on a machine with an arbitrary global `core.hooksPath`, because the fixture pins the hooks path in each repository it creates. The two tests remain intact and still fail if the production behaviour they cover regresses.

## Non-goals

- Changing the developer machine's global `core.hooksPath`. The suite must not depend on how any one machine is configured.
- Any change to sync behaviour, the quiet-window gate, the instance lock, or the git operations themselves.
- Broadening `internal/sync` test coverage beyond making these two tests hermetic.

## Assumptions

- `git` resolves `core.hooksPath` from the repository's own local config in preference to the global config, which is the documented precedence.
- The YOLO container's git config has no `core.hooksPath`, so a plain `go test` there passes either way. The container can still prove the fix, by running the two tests once with `GIT_CONFIG_GLOBAL` pointed at a hostile config — that simulates the developer's machine without modifying the tests. The operator rung below is the confirmation against the real global config.

## Acceptance Criteria

- [ ] `go test ./... -count=1` exits 0 on this machine with the developer's normal global git config — probe: `cd ~/Documents/workspaces/git-ai-sync && go test ./... -count=1` exits 0, with no `GIT_CONFIG_GLOBAL` override in the environment.
- [ ] The two tests still exercise what they claim — probe: on a machine whose global `core.hooksPath` points outside the repository, reverting the fixture change makes exactly `TestCommitRefusalStillPulls` and `TestPushRejectedKeepsLocalCommit` fail again, so the fix is not achieved by deleting or skipping them. (On a clean CI config the revert is a no-op; the machine condition is part of the criterion.)
- [ ] The fix is local to the test fixture, not to the machine — probe: `git diff --name-only origin/master` lists no file outside `internal/sync/*_test.go` and any shared test helper under `internal/sync/`.
- [ ] The suite passes both with and without a clean global config — probe: `go test ./... -count=1` exits 0 both normally and with `GIT_CONFIG_GLOBAL=/dev/null`, so the fix does not merely invert which environment it depends on.
- [ ] The fix is achieved by pinning the fixture's own config, not by making the tests ignore the global one — probe: `grep -rn 'GIT_CONFIG_GLOBAL' internal/sync/` returns 0 lines, and `grep -c 'hooksPath' internal/sync/sync_test.go` is at least 1. This is the negative evidence for the laziest implementation: setting `GIT_CONFIG_GLOBAL` in the test (or `TestMain`) also turns the suite green on a hostile machine, and it would satisfy every other criterion here while leaving the fixture just as environment-dependent as before. A constraint stated only in prose is not enforced.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit-go` — must exit 0. **Not `make precommit`**: in this repo that target is Python-only (`precommit: sync format test check` → `uv sync`, `ruff`, `pytest`, `mypy`) and never compiles or runs a single Go file, so a syntax error or a wrong hook path in the changed test file would pass it. `precommit-go` is the Go gate: `gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`.
- Prove the fix behaviourally by running the two tests once under a hostile global config. This does not modify the tests — the constraint forbids `GIT_CONFIG_GLOBAL` in the test code, not in a one-off verification shell:

  ```
  printf '[core]\n\thooksPath = /nonexistent\n' > /tmp/hostile.gitconfig
  GIT_CONFIG_GLOBAL=/tmp/hostile.gitconfig go test ./internal/sync -run 'TestCommitRefusalStillPulls|TestPushRejectedKeepsLocalCommit' -count=1
  ```

  Before the fix this fails with `CommitErr is nil, want the refusal` / `Run succeeded, want a push error`; after the fix it passes. The container **can** run this — it has git, and `GIT_CONFIG_GLOBAL` simulates the hostile config without touching the tests. A `grep -c 'core.hooksPath'` count is not a substitute: it counts source lines rather than pinned repositories, so a shared helper pins both repos with one occurrence while a duplicated wrong line passes.

### Operator-executable (runs on the host after merge)

- `cd ~/Documents/workspaces/git-ai-sync && go test ./... -count=1` — exits 0 with the developer's real global git config. This confirms the fix against the real machine, independent of the simulated hostile config the container rung uses, and it runs after merge.

## Desired Behavior

Running the suite on a machine with `core.hooksPath` set globally:

- Before: two failures, both caused by hooks that never fire.
- After: no failures. The hooks fire because the repository's own config names the directory they are written into.
- Reverting the pinning returns both tests to failing, so the pinning is what makes them pass rather than something masking them.

## Constraints

- Test files only. The diff may touch `internal/sync/sync_test.go` and any shared test helper under `internal/sync/`. It must not touch `internal/sync/sync.go`, any other production file, the `Makefile`, `src/`, `tests/`, `pyproject.toml` or `uv.lock`.
- `core.hooksPath` must be pinned as an **absolute** path — a relative value resolves unpredictably and would reintroduce the same class of bug. Derive it generically so the same helper is correct for both a normal clone and a bare repo: `git rev-parse --absolute-git-dir` returns `<clone>/.git` for a clone and `<remote>` for a bare repo, so joining `hooks` onto it yields `<clone>/.git/hooks` and `<remote>/hooks`. Do **not** use `git rev-parse --git-path hooks` — it returns the *effective* `core.hooksPath`, i.e. the very global value being overridden, so it reproduces the bug.
- `configure` has **two** callers today — `newFixture` for the clone (`sync_test.go:33`) and `addRemoteCommit` for the intermediate clone it creates (`:46`). The fix adds a third for the bare remote. Whatever helper is chosen must keep all of them correct, and a signature change must update every call site so the package still builds.
- No test may be deleted, skipped, renamed or have its assertions relaxed. On a machine whose global `core.hooksPath` points outside the repository, reverting this change must make exactly `TestCommitRefusalStillPulls` and `TestPushRejectedKeepsLocalCommit` fail again. (State the machine condition explicitly — on a clean CI config reverting is a no-op, so the revert is not a universal break.)
- The machine's global git configuration must not be modified, and `GIT_CONFIG_GLOBAL` must not be set **in the test code**. A one-off verification-shell run may set it to simulate a hostile global config — that is the behavioural check above, not a test dependency.
- Nothing may touch `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit under `.github/` is rejected at push.
- Use the existing `runGit` helper for git invocations so failures are reported consistently with the rest of the file.

## Failure Modes

- **Pinning the clone but not the bare remote.** `TestCommitRefusalStillPulls` goes green and `TestPushRejectedKeepsLocalCommit` stays red. Both repositories must be pinned.
- **Pinning a relative path.** Works in the clone, silently resolves elsewhere in the bare remote — the same class of bug, now harder to see.
- **Skipping the tests instead of fixing the fixture.** Satisfies the suite and defeats the tests; the revert probe in the Acceptance Criteria is what catches it.
- **Setting `GIT_CONFIG_GLOBAL` in the test.** Makes the suite pass by depending on the machine again, in the opposite direction.

## Security / Abuse

None. The change is confined to test fixtures, adds no network surface, and touches no credential or configuration outside the temporary repositories each test creates.

## Suggested Decomposition

One prompt: pin `core.hooksPath` in both fixture repositories, with a comment stating why.

## Alternatives Considered

- **Set `GIT_CONFIG_GLOBAL=/dev/null` in the test or the Makefile** — rejected: it makes the suite pass by ignoring the developer's configuration rather than by removing the dependency, and it hides the same class of bug in any future test that writes hooks.
- **Change the machine's global `core.hooksPath`** — rejected: out of scope, and it leaves every other checkout exposed.
- **Delete or skip the two tests** — rejected: they cover the fail-closed contract that the v0.13.1 notes exist for.

## Do-Nothing Option

The suite stays red on the machine that owns the repo. The prototype's `go test ./...` criterion cannot be ticked honestly, two known failures mask any future regression, and every developer on this machine learns to read a red suite as normal.
