---
status: completed
spec: [012-install-reload-race]
summary: Made launchd.Install wait for teardown and retry bootstrap once, with an injectable bound and tests modelling the race, always-failing, and uninstall→install cases
execution_id: 96a045ad-9b3b-5449-808f-6b11ba77d4fd-exec-045-spec-012-launchd-reload-race
dark-factory-version: dev
created: "2026-11-10T14:30:00Z"
queued: "2026-11-10T14:50:58Z"
started: "2026-11-10T14:50:59Z"
completed: "2026-11-10T14:55:21Z"
branch: dark-factory/install-reload-race
---

# Make the launchd reload converge: wait for teardown, retry the bootstrap once

<summary>
- Reinstalling on an already-loaded agent now waits for the old job to disappear before starting the new one.
- The wait is bounded, so a job that never unloads fails the command instead of hanging it.
- A start attempt that still races the teardown is retried once automatically.
- If the agent still cannot start, the command fails loudly and prints the exact manual command.
- An uninstall immediately followed by an install now converges.
- The wait stops promptly when the caller cancels the context.
- The wait bound can be shortened in tests, so the race is covered without a real fifteen-second sleep.
- The plist the install writes is byte-for-byte unchanged.
- No new configuration option or command-line flag is introduced.
</summary>

<objective>
Make `launchd.Install` converge against an already-loaded agent: after `launchctl bootout`, poll `launchctl print gui/<uid>/<label>` every 100 ms until launchd reports the job absent (bounded, 15 s by default and injectable for tests), then `launchctl bootstrap`; if the bootstrap still races, poll for absence once more and retry the bootstrap exactly once. **Why:** today `bootout` returns before launchd has torn the job down, so `bootstrap` fails with `Input/output error` and leaves the agent stopped — every reinstall and every rollback step is an outage.
</objective>

<context>
Read the approved spec at `specs/in-progress/012-install-reload-race.md` — Summary, Problem, Goal, Non-goals, Acceptance Criteria 1-5, Desired Behavior 1-4 and 6, Constraints, and every row of the Failure Modes table. The spec's Suggested Decomposition row 1 is this prompt.

Read these files before editing:
- `internal/launchd/launchd.go` — `Options` (lines ~30-37), `ResolveOptions`, `Install` (lines ~135-171), `Uninstall`, `IsLoaded` (lines ~201-215), and `runLaunchctl` (lines ~217-220). `Install` currently runs `bootout` (tolerating failure with a warning) and then `bootstrap` immediately; `IsLoaded` reports `(false, nil)` when `launchctl print` exits non-zero and `(false, err)` when the command cannot run.
- `internal/launchd/launchd_test.go` — the existing `testOptions(home)` helper and the `assertContains` / `assertInOrder` / `assertWellFormedXML` helpers. The package is `launchd`, so tests can call the unexported `Install`, `Uninstall`, `PlistPath`, `writePlist`.
- `internal/cli/lifecycle_test.go` — `stubLaunchctl(t)` (lines ~29-53), `runCLI`, `runStatus`, `writeConfig`, and the install/uninstall/status tests that call `install`. These run the real `Install` through the CLI, so their `launchctl` stub must model the lifecycle after this change.
- `internal/supervise/secret_test.go` — `installLaunchctlStub(t)` (lines ~172-182) and `TestCredentialsNeverLeaveTheConfigFile` (line ~86 calls `launchd.Install`). This is the only other `launchd.Install` call site in the repository.
- `internal/supervise/state.go` — not changed here; read only to confirm `Status`/`ReadState` are out of scope.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every wait must honour the caller's `ctx`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors` (`errors.Wrapf` / `errors.Errorf`), never `fmt.Errorf` or a bare `return err`, in new code.

The module path is `github.com/bborbe/git-ai-sync`. The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`) — **not** `make precommit`, which is Python-only and never compiles a Go file. `go test ./...` compiles and runs `internal/supervise`, which is why the stub there matters.

<!-- OPEN QUESTION for the reviewer: the spec's Constraints allow only internal/launchd/launchd.go, internal/launchd/launchd_test.go, internal/cli/cli.go, internal/cli/lifecycle_test.go, docs/launchd-service.md and CHANGELOG.md. That allowlist is not sufficient: internal/supervise/secret_test.go calls launchd.Install with a launchctl stub whose `print` always exits 0. Under the new bounded wait, Install would poll for the full 15 s and then fail, breaking TestCredentialsNeverLeaveTheConfigFile and therefore `make precommit-go` (`go test ./...`). The only resolution that keeps the gate green is the test-only stub change in Requirement 6. Confirm this one-file exception at audit time; no production file outside the allowlist is touched. -->

<!-- OPEN QUESTION for the reviewer: the spec says the bound is "injectable on Options for tests only" without naming the field. This prompt names it TeardownTimeout and documents it as tests-only. Confirm the name at audit time. -->
</context>

<requirements>
1. In `internal/launchd/launchd.go`, add `"time"` to the stdlib import block (it is not imported today; `context`, `fmt`, `strings`, `os/exec` and `stderrors "errors"` are already there).

2. In `internal/launchd/launchd.go`, add the tests-only bound to `Options` and two unexported constants. The `Options` struct becomes:
   ```go
   // Options configures install, uninstall and status.
   type Options struct {
       Config          config.FileConfig
       Logger          *slog.Logger
       HomeDir         string        // defaults to os.UserHomeDir()
       Launchctl       string        // defaults to "launchctl"
       BinaryPath      string        // defaults to the symlink-resolved os.Executable()
       UID             int           // defaults to os.Getuid()
       TeardownTimeout time.Duration // tests only; zero means defaultTeardownTimeout
   }
   ```
   Add, next to `Label`:
   ```go
   const (
       defaultTeardownTimeout = 15 * time.Second
       teardownPollInterval   = 100 * time.Millisecond
   )
   ```
   These are constant expressions (`time.Second` / `time.Millisecond` are typed constants), not function calls, so `const` is valid. Do **not** add `TeardownTimeout` to `ResolveOptions` defaulting logic — zero must keep meaning "use `defaultTeardownTimeout`", and `ResolveOptions` already returns the struct unchanged for this field. Do **not** add it to the YAML config, any cobra flag, or `config.FileConfig`; the spec's Non-goals forbid a config knob.

3. In `internal/launchd/launchd.go`, update the `Install` doc comment and replace the `bootout` → `bootstrap` tail of `Install` (everything from `target := fmt.Sprintf(...)` through the final `return nil`) with this logic. Keep everything above it (resolve, permissions check, mkdirs, `RenderPlist`, `writePlist`) byte-identical:
   ```go
   target := fmt.Sprintf("gui/%d/%s", resolved.UID, Label)
   if out, err := runLaunchctl(ctx, resolved, "bootout", target); err != nil {
       logger.Warn("launchctl bootout failed", "target", target, "output", strings.TrimSpace(out), "error", err)
   }

   timeout := resolved.TeardownTimeout
   if timeout <= 0 {
       timeout = defaultTeardownTimeout
   }
   deadline := time.Now().Add(timeout)

   if err := waitForUnload(ctx, resolved, deadline); err != nil {
       if stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded) {
           return err
       }
       return errors.Wrapf(ctx, err, "wait for launchd job %s to unload; run manually:\n  launchctl bootstrap gui/%d %s", target, resolved.UID, plistPath)
   }

   out, err := runLaunchctl(ctx, resolved, "bootstrap", fmt.Sprintf("gui/%d", resolved.UID), plistPath)
   if err == nil {
       return nil
   }

   logger.Warn("launchctl bootstrap failed, retrying once", "target", target, "output", strings.TrimSpace(out), "error", err)

   // The failed bootstrap may itself have registered nothing; poll for absence
   // once more before the single retry, sharing the same overall deadline.
   if waitErr := waitForUnload(ctx, resolved, deadline); waitErr != nil {
       if stderrors.Is(waitErr, context.Canceled) || stderrors.Is(waitErr, context.DeadlineExceeded) {
           return waitErr
       }
       return bootstrapError(ctx, resolved, plistPath, err, out)
   }

   if out2, err2 := runLaunchctl(ctx, resolved, "bootstrap", fmt.Sprintf("gui/%d", resolved.UID), plistPath); err2 != nil {
       return bootstrapError(ctx, resolved, plistPath, err2, out2)
   }
   return nil
   ```
   The `bootstrap` retry is exactly one; a third attempt must not exist. The overall wait is bounded by the single `deadline`, so the first wait plus the retry wait never exceed `timeout` in total.

4. In `internal/launchd/launchd.go`, add these two unexported functions. `waitForUnload` polls `launchctl print` through `IsLoaded` (which already maps a non-zero `print` exit to `(false, nil)`); it must use the caller's context and stop promptly on cancellation:
   ```go
   // waitForUnload polls `launchctl print gui/<uid>/<label>` every
   // teardownPollInterval until the job is absent or the deadline passes. It
   // uses the caller's context, so cancellation returns promptly.
   func waitForUnload(ctx context.Context, opts Options, deadline time.Time) error {
       for {
           if err := ctx.Err(); err != nil {
               return err
           }
           loaded, err := IsLoaded(ctx, opts)
           if err != nil {
               if ctxErr := ctx.Err(); ctxErr != nil {
                   return ctxErr
               }
               return errors.Wrapf(ctx, err, "check launchd job %s", Label)
           }
           if !loaded {
               return nil
           }
           if !time.Now().Before(deadline) {
               return errors.Errorf(ctx, "launchd job %s did not unload before the teardown deadline", Label)
           }
           timer := time.NewTimer(teardownPollInterval)
           select {
           case <-ctx.Done():
               timer.Stop()
               return ctx.Err()
           case <-timer.C:
           }
       }
   }

   // bootstrapError is the unchanged manual-recovery error Install returns when
   // the bootstrap fails on both attempts.
   func bootstrapError(ctx context.Context, opts Options, plistPath string, cause error, out string) error {
       return errors.Errorf(ctx, "launchctl bootstrap failed: %v\nRun manually:\n  launchctl bootstrap gui/%d %s\n%s", cause, opts.UID, plistPath, strings.TrimSpace(out))
   }
   ```
   `bootstrapError` must produce the same message text `Install` produces today (the spec's Non-goals forbid changing the manual-recovery message): `launchctl bootstrap failed: <cause>` followed by `Run manually:` and the exact `launchctl bootstrap gui/<uid> <plist>` line.

5. In `internal/launchd/launchd_test.go`, add tests and helpers that drive the real `Install` with a `launchctl` shell stub on `PATH`. The package is `launchd`, so no import cycle is created. Add the stdlib imports `context`, `log/slog`, `os`, `time`, and `stderrors "errors"` (`io` is already imported for `assertWellFormedXML`).

   Add a stub writer that embeds the log path and a per-invocation counter directory, and returns the log path:
   ```go
   func writeLaunchctlStub(t *testing.T, body string) string {
       t.Helper()
       dir := t.TempDir()
       binDir := filepath.Join(dir, "bin")
       if err := os.MkdirAll(binDir, 0o755); err != nil {
           t.Fatalf("create launchctl bin directory: %v", err)
       }
       stateDir := filepath.Join(dir, "state")
       if err := os.MkdirAll(stateDir, 0o755); err != nil {
           t.Fatalf("create launchctl state directory: %v", err)
       }
       logPath := filepath.Join(dir, "launchctl.log")
       if err := os.WriteFile(logPath, nil, 0o600); err != nil {
           t.Fatalf("create launchctl log: %v", err)
       }
       script := "#!/bin/sh\n" +
           "echo \"$*\" >> \"" + logPath + "\"\n" +
           body
       if err := os.WriteFile(filepath.Join(binDir, "launchctl"), []byte(script), 0o755); err != nil {
           t.Fatalf("write launchctl stub: %v", err)
       }
       t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
       t.Setenv("LAUNCHCTL_STATE", stateDir)
       return logPath
   }

   func launchctlCalls(t *testing.T, logPath string) []string {
       t.Helper()
       data, err := os.ReadFile(logPath)
       if err != nil {
           t.Fatalf("read launchctl log: %v", err)
       }
       var calls []string
       for _, line := range strings.Split(string(data), "\n") {
           if line = strings.TrimSpace(line); line != "" {
               calls = append(calls, line)
           }
       }
       return calls
   }

   // installOptions returns Options pointing at a real 0600 config file so
   // config.CheckPermissions passes, with a discard logger.
   func installOptions(t *testing.T, home string) Options {
       t.Helper()
       configPath := filepath.Join(home, "config.yaml")
       if err := os.WriteFile(configPath, []byte("vaults: []\n"), 0o600); err != nil {
           t.Fatalf("write config file: %v", err)
       }
       if err := os.Chmod(configPath, 0o600); err != nil {
           t.Fatalf("chmod config file: %v", err)
       }
       opts := testOptions(home)
       opts.Config.ConfigPath = configPath
       opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
       return opts
   }
   ```

   Use these stub bodies (each is appended after the log-writing line):
   ```go
   // print exits non-zero from its third invocation onward; bootstrap succeeds.
   const raceBody = "" +
       "if [ \"$1\" = \"print\" ]; then\n" +
       "  n=0\n" +
       "  if [ -f \"$LAUNCHCTL_STATE/print-count\" ]; then n=$(cat \"$LAUNCHCTL_STATE/print-count\"); fi\n" +
       "  n=$((n+1))\n" +
       "  echo \"$n\" > \"$LAUNCHCTL_STATE/print-count\"\n" +
       "  if [ \"$n\" -ge 3 ]; then exit 1; fi\n" +
       "fi\n" +
       "exit 0\n"

   // print reports absent; the first bootstrap exits 5, the second exits 0.
   const bootstrapFailOnceBody = "" +
       "if [ \"$1\" = \"print\" ]; then exit 1; fi\n" +
       "if [ \"$1\" = \"bootstrap\" ]; then\n" +
       "  n=0\n" +
       "  if [ -f \"$LAUNCHCTL_STATE/bootstrap-count\" ]; then n=$(cat \"$LAUNCHCTL_STATE/bootstrap-count\"); fi\n" +
       "  n=$((n+1))\n" +
       "  echo \"$n\" > \"$LAUNCHCTL_STATE/bootstrap-count\"\n" +
       "  if [ \"$n\" -eq 1 ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
       "fi\n" +
       "exit 0\n"

   // print reports absent; every bootstrap exits 5.
   const bootstrapAlwaysFailsBody = "" +
       "if [ \"$1\" = \"print\" ]; then exit 1; fi\n" +
       "if [ \"$1\" = \"bootstrap\" ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
       "exit 0\n"

   // print always reports loaded; bootstrap always exits 5 (never reached).
   const printLoadedBootstrapFailsBody = "" +
       "if [ \"$1\" = \"print\" ]; then exit 0; fi\n" +
       "if [ \"$1\" = \"bootstrap\" ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
       "exit 0\n"
   ```

   Add these tests:
   - `TestInstallWaitsForTeardownThenBootstraps` (Acceptance Criterion 1): `writeLaunchctlStub(t, raceBody)`, `Install(context.Background(), installOptions(t, home))` returns nil. Parse `launchctlCalls`; locate the first `bootout ` index, the first and last `print ` index, and the first `bootstrap ` index. Assert `bootout >= 0`, `bootstrap >= 0`, the `print` count is **at least 3**, and `bootout < firstPrint <= lastPrint < bootstrap`. This fails an implementation that does one `print`, sleeps, then bootstraps.
   - `TestUninstallThenInstallConverges` (Acceptance Criterion 2): seed a stale plist at `PlistPath(home)` (mkdir `filepath.Dir`, write any bytes), run `Uninstall(context.Background(), opts)` (must return nil), record `before := len(launchctlCalls(t, logPath))`, then run `Install` (must return nil). Slice the calls from `before`: the first call must start with `bootout `, the last must start with `bootstrap `, at least one `print ` must appear, and exactly one `bootstrap ` must appear in the install leg.
   - `TestInstallRetriesBootstrapOnce` (Acceptance Criterion 3): `writeLaunchctlStub(t, bootstrapFailOnceBody)`; `Install` returns nil; the call log contains exactly two lines starting with `bootstrap `.
   - `TestInstallFailsWhenBootstrapKeepsFailing` (Acceptance Criterion 4): `writeLaunchctlStub(t, bootstrapAlwaysFailsBody)`; `Install` returns a non-nil error whose `Error()` contains `launchctl bootstrap gui/`.
   - `TestInstallBoundsTeardownWait` (Acceptance Criterion 5): `writeLaunchctlStub(t, printLoadedBootstrapFailsBody)`; `opts := installOptions(t, home)`; `opts.TeardownTimeout = 200 * time.Millisecond`; measure `time.Now()` around `Install(context.Background(), opts)`. Assert the error is non-nil, `time.Since(start) < 2*time.Second`, and the error contains `launchctl bootstrap gui/` (the Failure Modes row for a job that never disappears requires the manual command in the message).
   - `TestInstallReturnsPromptlyWhenContextCancelled` (Constraints: the wait uses the caller's context): `writeLaunchctlStub(t, printLoadedBootstrapFailsBody)`; `opts.TeardownTimeout = 10 * time.Second`; create `ctx, cancel := context.WithCancel(context.Background()); cancel()`; measure the call. Assert the error is non-nil, `stderrors.Is(err, context.Canceled)` is true, and `time.Since(start) < 2*time.Second` (it must not sleep out the bound).

   Do not mark any test parallel (`t.Setenv` forbids it). Leave every existing test in the file unchanged.

6. Update the `launchctl` stub in `internal/cli/lifecycle_test.go` so the existing CLI tests keep passing against the new bounded wait. In `stubLaunchctl`, after `binDir` is created, create a state directory and replace the script with a lifecycle-aware one:
   ```go
   stateDir := filepath.Join(dir, "state")
   if err := os.MkdirAll(stateDir, 0o755); err != nil {
       t.Fatalf("create launchctl state directory: %v", err)
   }
   script := "#!/bin/sh\n" +
       "echo \"$@\" >> \"$LAUNCHCTL_LOG\"\n" +
       "if [ \"$1\" = \"bootout\" ]; then\n" +
       "  : > \"$LAUNCHCTL_STATE/unloaded\"\n" +
       "fi\n" +
       "if [ \"$1\" = \"bootstrap\" ]; then\n" +
       "  rm -f \"$LAUNCHCTL_STATE/unloaded\"\n" +
       "fi\n" +
       "if [ \"$1\" = \"print\" ]; then\n" +
       "  if [ -n \"$LAUNCHCTL_PRINT_FAIL\" ]; then\n" +
       "    exit 1\n" +
       "  fi\n" +
       "  if [ -f \"$LAUNCHCTL_STATE/unloaded\" ]; then\n" +
       "    exit 1\n" +
       "  fi\n" +
       "fi\n" +
       "exit 0\n"
   ```
   and add `t.Setenv("LAUNCHCTL_STATE", stateDir)` next to the existing `t.Setenv("LAUNCHCTL_LOG", logPath)`. The semantics: before any `bootout` the agent is loaded (`print` exits 0, so the status tests still see `agent: loaded`); after `bootout` and before `bootstrap` the agent is absent (`print` exits 1, so `waitForUnload` returns immediately); `LAUNCHCTL_PRINT_FAIL` still forces `print` to exit 1 for the not-loaded status assertion. Change nothing else in the file.

7. Update the `launchctl` stub in `internal/supervise/secret_test.go` (flagged exception — see the OPEN QUESTION in `<context>`). Replace the stub string in `installLaunchctlStub`:
   ```go
   stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LAUNCHCTL_LOG\"\nexit 0\n"
   ```
   with:
   ```go
   stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LAUNCHCTL_LOG\"\nif [ \"$1\" = \"print\" ]; then\n  exit 1\nfi\nexit 0\n"
   ```
   This makes `print` report the job absent so `waitForUnload` returns immediately; `bootout` and `bootstrap` still exit 0. Change nothing else in that file.
</requirements>

<constraints>
- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`, `internal/cli/cli.go`, `internal/cli/lifecycle_test.go`, `docs/launchd-service.md` and `CHANGELOG.md`. The doc is in the list because it currently states the old two-step flow at lines 69 and 180. This prompt does not edit `internal/cli/cli.go`, `docs/launchd-service.md` or `CHANGELOG.md` — later prompts own them.
- **Flagged deviation requiring reviewer sign-off:** the spec's allowlist above does not include `internal/supervise/secret_test.go`, but its stub must change (Requirement 7) or `make precommit-go` fails. No production file outside the allowlist is touched. Confirm at audit time.
- The `launchctl` invocations stay injectable through the existing seam, so the tests never touch the real launchd domain.
- The wait uses the caller's context; a cancelled context returns promptly rather than sleeping out the bound.
- The injected bound exists for tests only and is documented as such — it is not a config key and must not appear in the YAML schema.
- The plist written by `Install` is unchanged — same bytes, same keys, same values. Do not touch `RenderPlist`.
- `Uninstall` is unchanged: only `Install` waits for teardown.
- The manual-recovery message must stay exactly `launchctl bootstrap failed: <cause>` followed by `Run manually:` and `launchctl bootstrap gui/<uid> <plist>`.
- The `bootstrap` retry is exactly one; do not add a loop or a third attempt.
- Use `github.com/bborbe/errors` for wrapping in new code. No `fmt.Errorf` and no bare `return err`.
- No new CLI flag, config field or YAML key. No change to `internal/config`, `internal/supervise` production code, or `internal/launchd.RenderPlist`.
- Do NOT touch `.github/` or anything under `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit there is rejected at push.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/launchd -run 'TestInstall|TestUninstallThenInstall' -count=1 -v
go test ./internal/cli -run 'TestInstall|TestUninstall' -count=1
go test ./internal/supervise -run TestCredentialsNeverLeaveTheConfigFile -count=1
go test ./internal/launchd ./internal/cli -count=1
make precommit-go
```

Expected: `gofmt -l cmd internal` prints nothing; every `go test` command exits 0; `make precommit-go` exits 0 (it also runs `go test ./...`, covering `internal/supervise`).

Confirm the shape of the change:

```
test "$(grep -c 'defaultTeardownTimeout' internal/launchd/launchd.go)" -ge 1
test "$(grep -c 'TeardownTimeout' internal/launchd/launchd.go)" -ge 2
test "$(grep -c 'teardownPollInterval' internal/launchd/launchd.go)" -ge 2
! grep -q 'TeardownTimeout' internal/config/file.go
```

Before finishing, walk the assigned criteria against the tests:
- `Install` on a loaded agent records `bootout`, at least three `print` calls, then `bootstrap`, and returns nil (Acceptance Criterion 1);
- `Uninstall` then `Install` both return nil and the install leg is `bootout, print…, bootstrap` (Acceptance Criterion 2);
- a bootstrap that fails once and succeeds on the retry returns nil and records two `bootstrap` calls (Acceptance Criterion 3);
- a bootstrap that fails every time returns a non-nil error containing `launchctl bootstrap gui/` (Acceptance Criterion 4);
- with a job that never unloads and an injected 200 ms bound, `Install` returns a non-nil error in under 2 s and names the manual command (Acceptance Criterion 5 and the "job never disappears" Failure Modes row);
- a cancelled context returns `context.Canceled` promptly (Constraints);
- the existing CLI install/uninstall/status tests and `TestCredentialsNeverLeaveTheConfigFile` still pass.
</verification>
