---
status: completed
summary: Replaced the fixed 3s sleep in TestFailingVaultDoesNotStopOthers with bounded polling via the existing waitForOccurrences helper
execution_id: git-ai-sync-flaky-test-exec-050-fix-supervise-flaky-test
dark-factory-version: v0.198.17
created: "2026-11-10T00:00:00Z"
queued: "2026-11-10T22:41:55Z"
started: "2026-11-10T22:57:44Z"
completed: "2026-11-10T23:01:06Z"
---

# Make the failing-vault test wait for pushes instead of sleeping

<summary>
- The failing-vault isolation test becomes timing-independent instead of failing on a slow machine.
- It observes pushes and retries by polling, bounded by a timeout, rather than waiting a fixed interval.
- It keeps proving the failing vault is retried while the healthy ones keep pushing.
- A timeout failure still prints the collected log, so a real regression is diagnosable.
- No supervisor, sync, or watch behaviour changes.
</summary>

<objective>
The failing-vault isolation test observes pushes by polling until the expected counts are reached, bounded by a timeout, instead of waiting a fixed 3 s interval. **Why:** the fixed sleep races the 1 s sync interval plus the sync duration, so the second push lands after the assertion on a slower machine (macOS) and before it on a fast one (Linux CI) — the test is flaky, not the supervisor.
</objective>

<context>
Read these files before editing:
- `internal/supervise/supervise_test.go` — `TestFailingVaultDoesNotStopOthers` is the test to change. `supervisePushTimeout` is `20 * time.Second`. The imports already include `strings` and `time`.
- `internal/supervise/metrics_notifier_test.go:325` — `waitForOccurrences(t *testing.T, buffer *superviseLogBuffer, substr string, want int, timeout time.Duration)` already polls `buffer.String()` every 20 ms and calls `t.Fatalf` with the log on timeout. It is in the same `package supervise`, so it is directly callable; reuse it and do not re-implement it.
- `internal/watch/watch.go` `loop` — sleeps `Interval` (1 s) then runs a sync, so a fixed 3 s window captures one or two pushes depending on how long a sync takes.
- `internal/supervise/supervise.go` — the closure returned by `runnerFor` logs `push path=<clone> sha=<sha>` when a cycle pushed (`supervise.go:131`); `runVault` logs `vault loop returned path=<badPath>` when a vault's loop returns (`supervise.go:97`).

The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`), not `make precommit` — `make precommit` runs only the Python toolchain (`uv`, `ruff`, `mypy`, `pytest`) and does not touch the Go code this prompt changes. `docs/dod.md` is likewise the Python definition of done and does not apply here.
</context>

<requirements>
1. In `internal/supervise/supervise_test.go`, in `TestFailingVaultDoesNotStopOthers`, replace the `time.Sleep(3 * time.Second)` call with polling while the writer goroutine is still running:
   ```go
   for _, f := range []superviseFixture{first, second} {
       waitForOccurrences(t, buffer, "push path="+f.clone, 2, supervisePushTimeout)
   }
   waitForOccurrences(t, buffer, "path="+badPath, 3, supervisePushTimeout)
   stopWriter()
   ```
   The `stopWriter()` call moves after the polls; the writer must keep writing while the test waits for the pushes. Keep the existing `defer stopWriter()` above the writer goroutine — it is what stops the writer when a poll calls `t.Fatalf`.

2. Remove the now-redundant `strings.Count` assertions that followed the sleep — the `if got := strings.Count(buffer.String(), "path="+badPath); got < 3` block and the `for _, f := range []superviseFixture{first, second}` block asserting `>= 2` pushes. `waitForOccurrences` performs the same checks and prints the log on failure.

3. Keep the remaining assertions unchanged: the `strings.Contains(buffer.String(), "vault panicked")` check and the `select { case err := <-done: ... }` check must stay.

4. Do not add a new helper: `waitForOccurrences` already exists in `internal/supervise/metrics_notifier_test.go` and must not be renamed, moved, or duplicated. Do not change the writer goroutine, the fixtures, `superviseConfig`, `startSupervise`, `waitForLine`, or any non-test file.
</requirements>

<constraints>
- Change only `internal/supervise/supervise_test.go`.
- Do not change supervisor, watch, sync, or config code; do not alter the push cadence or interval.
- Do not lower the required counts: both healthy vaults must still reach at least 2 pushes and the failing vault at least 3 failures, so the isolation assertion stays as strong as before.
- Do not introduce a `runtime.GOOS` skip; the test must run on both macOS and Linux.
- Do not duplicate `waitForOccurrences` under a new name — call the existing helper.
- Do not edit `CHANGELOG.md`.
- Do NOT touch `.github/` or anything under `.github/workflows/`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/supervise -run TestFailingVaultDoesNotStopOthers -race -count=20
go test ./internal/supervise -count=1
make precommit-go
```

Expected: `gofmt -l cmd internal` prints nothing; every `go test` command exits 0; `make precommit-go` exits 0.

Confirm the shape of the change:

```
! grep -q 'time.Sleep(3 \* time.Second)' internal/supervise/supervise_test.go
! grep -q 'func waitForCount' internal/supervise/supervise_test.go
grep -c 'waitForOccurrences(t, buffer' internal/supervise/supervise_test.go
```

Expected: both absence checks print nothing; the call count prints `2`.

Before finishing, re-run the verification and confirm each command's expected result.
</verification>
