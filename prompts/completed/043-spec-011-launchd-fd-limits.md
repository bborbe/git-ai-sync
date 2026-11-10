---
status: completed
spec: [011-launchd-file-descriptor-limits]
summary: Raised the rendered launchd NumberOfFiles limits to 65536 soft / 245760 hard in the renderer and updated the shape and install-path tests plus CHANGELOG.md.
execution_id: 2615257c-2e6a-5403-84b5-f7ea650cf3d6-exec-043-spec-011-launchd-fd-limits
dark-factory-version: dev
created: "2026-11-10T13:10:00Z"
queued: "2026-11-10T13:19:11Z"
started: "2026-11-10T13:19:13Z"
completed: "2026-11-10T13:21:04Z"
branch: dark-factory/launchd-file-descriptor-limits
---

# Raise the rendered launchd file-descriptor limits to 65536 / 245760

<summary>
- A fresh `git-ai-sync install` now renders file-descriptor limits that fit the twelve-vault workload instead of the 4096 / 8192 baseline.
- The soft limit becomes 65536 and the hard limit becomes 245760 in the plist the renderer produces.
- The renderer's shape test pins both numbers and their placement, so a later edit that lowers or swaps them fails a test instead of silently reintroducing an outage.
- The install-path test asserts the same two numbers on the plist a real `install` writes, so the renderer and the install path cannot drift apart.
- No other plist key changes: the throttle interval, process type, `PATH`, log paths and the absence of a process-count limit all stay as they are.
- There is no new flag, config field or operator knob — the value is a property of the workload.
- A second `install` still writes a byte-identical plist.
</summary>

<objective>
Change the two `NumberOfFiles` literals in the launchd plist renderer from 4096 / 8192 to 65536 / 245760, and update both tests that assert them, so the supervisor's per-watched-path file descriptors fit the twelve-vault / 64,945-file workload. **Why:** under 4096 every vault loop died at startup with `too many open files` while the agent still reported `running`, and nothing synced; the current renderer silently reintroduces that outage on every install.
</objective>

<context>
Read the approved spec at `specs/in-progress/011-launchd-file-descriptor-limits.md` — Desired Behavior 1-3, Acceptance Criteria 1, 2 and 5, Constraints, and the Failure Modes rows for a lowered constant and a host `kern.maxfilesperproc` below 245760.

Read these files before editing:
- `internal/launchd/launchd.go` — `RenderPlist` writes the plist as a fixed sequence of `b.WriteString(...)` calls. The soft block is around the `SoftResourceLimits` key and the hard block around `HardResourceLimits`; each contains one `<integer>` line.
- `internal/launchd/launchd_test.go` — `TestRenderPlistShape` builds `testOptions(t.TempDir())`, calls `RenderPlist`, and asserts the document with `assertContains`, `assertInOrder` and `assertWellFormedXML`.
- `internal/cli/lifecycle_test.go` — `TestInstallWritesPlistAndBootstraps` runs `install` against a `launchctl` stub, reads `<HOME>/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist`, and asserts a slice of needles with `requireContains`. The two integer literals sit in that slice.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`

The Go module path is `github.com/bborbe/git-ai-sync`. The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`) — **not** `make precommit`, which is Python-only and never compiles a Go file.

<!-- OPEN QUESTION for the reviewer: spec Acceptance Criterion 1 says `TestInstallWritesPlistAndBootstraps` "parses the plist it writes". The existing test reads the file bytes and asserts substrings with `requireContains`; it does not decode the plist with an XML/plist parser. This prompt keeps that existing style — the two literals are unique in the document, so the assertions are unambiguous, and adding a plist parser would add a dependency for no extra coverage. Confirm this satisfies the criterion. -->
</context>

<requirements>
1. In `internal/launchd/launchd.go`, inside `RenderPlist`, change exactly two literals and nothing else:
   - under `<key>SoftResourceLimits</key>`, the `NumberOfFiles` integer line becomes:
     ```go
     b.WriteString("    <integer>65536</integer>\n")
     ```
   - under `<key>HardResourceLimits</key>`, the `NumberOfFiles` integer line becomes:
     ```go
     b.WriteString("    <integer>245760</integer>\n")
     ```
   Keep the four-space indentation, the key order, and every other line of the document byte-identical. Do **not** extract these into named constants or any kind of configurable value: the renderer hard-codes its other frozen values (`ThrottleInterval` 30, `ProcessType` `Standard`) and this one follows the same pattern. Do not add a flag, struct field or config knob for the limit — the spec's Non-goals forbid it.

2. In `internal/launchd/launchd_test.go`, in `TestRenderPlistShape`, replace the two existing limit assertions:
   ```go
   assertContains(t, document, "<integer>4096</integer>")
   assertContains(t, document, "<integer>8192</integer>")
   ```
   with assertions that pin both the values and their placement:
   ```go
   assertContains(t, document, "<integer>65536</integer>")
   assertContains(t, document, "<integer>245760</integer>")
   assertInOrder(t, document,
       "<key>SoftResourceLimits</key>",
       "<integer>65536</integer>",
       "<key>HardResourceLimits</key>",
       "<integer>245760</integer>",
   )
   ```
   `assertInOrder` already exists in this file and fails when a needle is missing or out of order, so a swap of soft and hard is also caught. Leave the rest of the test unchanged, including the forbidden-substring list (`NumberOfProcesses`, `ResidentSetSize`, `super-secret`) and the well-formed-XML check.

3. In `internal/cli/lifecycle_test.go`, in `TestInstallWritesPlistAndBootstraps`, replace these two entries in the `requireContains` needle slice:
   ```go
   "<integer>4096</integer>",
   "<integer>8192</integer>",
   ```
   with:
   ```go
   "<integer>65536</integer>",
   "<integer>245760</integer>",
   ```
   Change nothing else in the file. This edit is mandatory: `make precommit-go` runs `go test ./...`, so leaving the old literals here fails the repo gate even though the renderer is correct.

4. Do not add new tests beyond the edits above. The shape test traverses the renderer boundary with the new values and the install test traverses the CLI-to-renderer boundary; together they cover both the soft and hard limits. Do not touch `TestRenderPlistDeterministic`, `TestRenderPlistEscapesValues`, `TestRenderPlistRequiresPaths`, `TestPaths`, or any other test in either file.
</requirements>

<constraints>
- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go` and `internal/cli/lifecycle_test.go`. `docs/launchd-service.md` and `CHANGELOG.md` are handled by a separate prompt — do not edit them here.
- The soft limit is 65536 and the hard limit is 245760. The hard limit must stay at or below `kern.maxfilesperproc` (245760 on the reference host): launchd silently clamps a larger value, which would read as a fix that did nothing.
- No other plist key changes: `ThrottleInterval` stays 30, `ProcessType` stays `Standard`, `PATH`, the log paths and the absence of `NumberOfProcesses` / `ResidentSetSize` all stay as they are.
- No change to `internal/supervise`, `internal/watch`, `internal/changetracker`, the config schema, or any Python file under `src/` or `tests/`.
- Do NOT add a per-install flag, config field or operator preference for the limit (spec Non-goal). The value is a property of the workload.
- Tests must not touch the real launchd domain, the real `~/Library/LaunchAgents`, or the operator's `~/.config`; the existing temp-`HOME` and `launchctl`-stub fixtures already guarantee this. Do not mark tests parallel.
- Do NOT touch `.github/` or anything under `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit there is rejected at push.
- New code follows the existing style; this prompt changes literals and test assertions only, so no new error paths are introduced.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/launchd -run TestRenderPlistShape -count=1
go test ./internal/cli -run TestInstallWritesPlistAndBootstraps -count=1
go test ./internal/launchd ./internal/cli -count=1
make precommit-go
```

Expected: `gofmt -l cmd internal` prints nothing, every `go test` command exits 0, and `make precommit-go` exits 0.

Confirm the old values are gone from the Go sources and the new ones are present:

```
test "$(grep -c '<integer>65536</integer>' internal/launchd/launchd.go)" = "1"
test "$(grep -c '<integer>245760</integer>' internal/launchd/launchd.go)" = "1"
test -z "$(grep -rn '4096\|8192' internal/launchd internal/cli)"
```

Before finishing, walk the assigned criteria against the tests:
- the rendered plist carries soft 65536 and hard 245760 (Acceptance Criterion 1);
- `TestRenderPlistShape` fails if either literal is reverted or the two are swapped, and `TestRenderPlistDeterministic` still passes (Acceptance Criterion 2);
- `TestInstallWritesPlistAndBootstraps` asserts both new numbers on the plist a real `install` writes, and `TestInstallIsIdempotent` still passes (Acceptance Criteria 1 and 5);
- the rendered plist still contains no `NumberOfProcesses` key (Acceptance Criterion 5).
</verification>
