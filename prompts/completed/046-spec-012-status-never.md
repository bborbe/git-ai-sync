---
status: completed
spec: [012-install-reload-race]
summary: status now prints `never` for a zero recorded push time, with a new exact-line test and a strengthened positive-age assertion; the CHANGELOG bullet remains with its owning prompt per the prompt constraints.
execution_id: 96a045ad-9b3b-5449-808f-6b11ba77d4fd-exec-046-spec-012-status-never
dark-factory-version: dev
created: "2026-11-10T14:30:00Z"
queued: "2026-11-10T14:55:00Z"
started: "2026-11-10T14:55:23Z"
completed: "2026-11-10T14:57:14Z"
branch: dark-factory/install-reload-race
---

# Print `never` from status when no metrics push is recorded

<summary>
- `git-ai-sync status` now prints `last metrics push: never` whenever the state file records no push time.
- That covers a state file that exists but has a zero `last_metrics_push`, not only a missing state file.
- The int64 duration overflow (`2562047h47m16.854775807s ago`) no longer appears between a restart and the first push.
- A real timestamp still prints its age, and a backwards clock still clamps the age at zero.
- A test pins the exact output line for a zero timestamp.
- The existing freshness test now also asserts the printed age is positive.
- No configuration, flag or launchd behaviour changes.
</summary>

<objective>
Fix `git-ai-sync status` so it prints `last metrics push: never` when the state file exists but `last_metrics_push` is the zero time, instead of computing `time.Since` on the zero value and printing a duration near the int64 maximum. **Why:** immediately after a reload the state file exists but records no push, so the documented `never` becomes a bogus 2.9-million-hour age that a script reading the output mistakes for a live value.
</objective>

<context>
Read the approved spec at `specs/in-progress/012-install-reload-race.md` — Desired Behavior 5, Acceptance Criteria 6 and 7, Constraints, and the Failure Modes rows for a zero push time and a backwards clock. The spec's Suggested Decomposition row 2 is this prompt.

Read these files before editing:
- `internal/cli/cli.go` — `newStatusCommand` (lines ~182-233). The state branch currently reads `supervise.ReadState`, prints `last metrics push: never` only when the error wraps `fs.ErrNotExist`, and otherwise computes `age := time.Since(state.LastMetricsPush)`, clamps `age < 0` to 0, and prints `age.Round(time.Second)`.
- `internal/supervise/state.go` — `State.LastMetricsPush time.Time` (JSON `last_metrics_push`) and `ReadState`. The zero value is `time.Time{}`; `time.Time.IsZero()` is available (Go 1.27).
- `internal/cli/lifecycle_test.go` — `runStatus(t, home)` (captures stdout), `writeConfig`, `stubLaunchctl`, `parseMetricsAge`, and `TestStatusReportsVaultsAndFreshness` (the existing freshness test that must keep passing).

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`

The module path is `github.com/bborbe/git-ai-sync`. The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`) — **not** `make precommit`, which is Python-only.

Prompt 1 of this spec changes `internal/launchd/launchd.go` and the `stubLaunchctl` helper in this same test file. This prompt runs after it: `stubLaunchctl` is lifecycle-aware, and with no prior `bootout` its `print` exits 0 (agent loaded), so the status tests here still see `agent: loaded`. Do not edit `internal/launchd`.
</context>

<requirements>
1. In `internal/cli/cli.go`, in `newStatusCommand`, change the state branch so a zero `LastMetricsPush` prints `never` and no duration is computed:
   ```go
   } else if state.LastMetricsPush.IsZero() {
       fmt.Fprintln(out, "last metrics push: never")
   } else {
       age := time.Since(state.LastMetricsPush)
       if age < 0 {
           age = 0
       }
       fmt.Fprintf(out, "last metrics push: %s ago\n", age.Round(time.Second))
   }
   ```
   The existing `fs.ErrNotExist` branch above it (`last metrics push: never`) stays as it is. The negative-age clamp stays as it is for a real timestamp. Do not otherwise change the vault loop, the `IsLoaded` call, or the `agent: loaded` / `agent: not loaded` handling.

2. In `internal/cli/lifecycle_test.go`, add `TestStatusReportsNeverForZeroPush` (Acceptance Criterion 6):
   - `home := t.TempDir()`; `stubLaunchctl(t)`; write a config naming one absolute temp vault with `writeConfig`.
   - Create `<home>/.local/state/git-ai-sync/status.json` (mkdir the parent) with mode `0600` and exactly `{"last_metrics_push":"0001-01-01T00:00:00Z","vaults":{}}`.
   - `out, err := runStatus(t, home)`; assert `err == nil`.
   - Assert one line of `out` is exactly `last metrics push: never` (split on `\n` and compare the line, not a substring).
   - Do not set `LAUNCHCTL_PRINT_FAIL`; the stub's `print` must exit 0 so status reaches the agent-loaded line and returns nil.

3. In `internal/cli/lifecycle_test.go`, in `TestStatusReportsVaultsAndFreshness`, strengthen the freshness assertion (Acceptance Criterion 7). The existing block is:
   ```go
   if age, ok := parseMetricsAge(t, out); !ok {
       t.Fatalf("status has no last metrics push line: %q", out)
   } else if age >= 60*time.Second {
       t.Errorf("last metrics push age = %v, want under 60s", age)
   }
   ```
   Change it to also reject a non-positive age:
   ```go
   if age, ok := parseMetricsAge(t, out); !ok {
       t.Fatalf("status has no last metrics push line: %q", out)
   } else if age <= 0 {
       t.Errorf("last metrics push age = %v, want a positive duration", age)
   } else if age >= 60*time.Second {
       t.Errorf("last metrics push age = %v, want under 60s", age)
   }
   ```
   Leave the rest of the test unchanged, including the missing-state-file `never` assertion and the `LAUNCHCTL_PRINT_FAIL` not-loaded assertion.
</requirements>

<constraints>
- Change only `internal/cli/cli.go` and `internal/cli/lifecycle_test.go` for this prompt. Do not touch `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`, `internal/supervise/state.go`, `docs/launchd-service.md` or `CHANGELOG.md` (other prompts own them).
- `status` prints `never` whenever there is no recorded push, whether the state file is absent or merely records a zero time.
- The existing clamp that keeps a real age at zero when the clock jumps backwards is left as it is.
- The documented output format is unchanged for real timestamps: `last metrics push: <duration> ago`.
- No new CLI flag, config field or YAML key.
- Do NOT touch `.github/` or anything under `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit there is rejected at push.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
gofmt -l cmd internal
go test ./internal/cli -run 'TestStatusReportsNeverForZeroPush|TestStatusReportsVaultsAndFreshness' -count=1 -v
go test ./internal/cli -count=1
go test ./internal/launchd ./internal/cli -count=1
make precommit-go
```

Expected: `gofmt -l cmd internal` prints nothing; every `go test` command exits 0; `make precommit-go` exits 0.

Confirm the fix is present:

```
test "$(grep -c 'LastMetricsPush.IsZero()' internal/cli/cli.go)" = "1"
test "$(grep -c 'age := time.Since(state.LastMetricsPush)' internal/cli/cli.go)" = "1"
```

Before finishing, walk the assigned criteria against the tests:
- a state file with `last_metrics_push` `0001-01-01T00:00:00Z` prints exactly `last metrics push: never` (Acceptance Criterion 6);
- the existing freshness test still prints a parseable age and now asserts it is positive and under 60 s (Acceptance Criterion 7);
- a missing state file still prints `never`; a not-loaded agent still exits non-zero naming the label.
</verification>
