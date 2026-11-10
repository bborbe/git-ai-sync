---
status: completed
spec: [010-multi-vault-supervisor]
summary: Added internal/launchd with install/uninstall/status and their CLI wiring, deterministic baseline-compliant plist rendering, bootout-then-bootstrap loading, idempotence, tests, and the Go-path launchd doc rewrite; also supplied the missing prerequisite config-file loader and supervisor surface the task depends on.
execution_id: 343bcb29-c431-540c-b449-0eef0950c240-exec-038-spec-010-launchd-lifecycle
dark-factory-version: dev
created: "2026-11-10T09:21:02Z"
queued: "2026-11-10T09:40:50Z"
started: "2026-11-10T09:40:52Z"
completed: "2026-11-10T09:48:08Z"
branch: dark-factory/multi-vault-supervisor
---

# Add the launchd lifecycle: install, uninstall and status

<summary>
- `install` writes one baseline-compliant launchd plist for the supervisor and loads it with bootout then bootstrap.
- The plist names the resolved binary, runs `supervise --config <resolved path>`, and carries no secret.
- `install` creates the log directory and is idempotent: a second run writes a byte-identical plist and exits 0.
- `uninstall` boots the job out and removes the plist; with nothing installed it exits 0.
- `status` prints the configured vaults, the age of the last metrics push, and whether the agent is loaded.
- `status` exits non-zero when the agent is not loaded.
- `install` refuses a config file other users can read.
- `install`, `uninstall` and `status` require a config file; `sync` still works with no config file.
- The launchd doc describes the Go path, not the superseded per-vault one.
</summary>

<objective>
Add `internal/launchd` and the `install`, `uninstall` and `status` commands: a deterministic, baseline-compliant single launchd agent for the supervisor, a `launchctl` bootout-then-bootstrap lifecycle, and a status report of the configured vaults, the last metrics push and the agent's loaded state. **Why:** this is the only lifecycle surface the operator needs, and it reproduces the whole setup from the one `0600` config file instead of twelve hand-maintained plists.
</objective>

<context>
Read the approved spec at `specs/in-progress/010-multi-vault-supervisor.md` — Desired Behavior 1-2 and 7-8, Acceptance Criteria 2, 10, 11, 12, 14 and 16, the Failure Modes rows for a world-readable config / a `launchctl bootstrap` failure / an already-loaded agent / a second `install` / disk full / a clock jump, Constraints (including the expanded launchd baseline rules 1-6 and 10), and Security. The Go gate is `make precommit-go`, not `make precommit`.

Read `docs/launchd-service.md` in full — it is the document to rewrite. It currently documents `setup-launchd` / `remove-launchd`, `/tmp` logs, `NumberOfFiles` 1024/2048, credentials in `EnvironmentVariables`, and a `kickstart -k` fallback. All of that is superseded for the Go path.

Read `internal/config/file.go` (from the first prompt) for `FileConfig` (notably `ConfigPath`, `Vaults`, `StateFile`, `Pushgateway`), `FileOptions`, `LoadFile`, and `CheckPermissions`. Read `internal/supervise/state.go` (from the previous prompt) for `State`, `VaultState` and `ReadState` (a missing file wraps `fs.ErrNotExist`). Read `internal/supervise/supervise.go` for `Label`.

Read `internal/cli/cli.go` for `newRootCommand`, `commandContext`, `addConfigFlag`, `fileOptions`, and the `newSuperviseCommand` wiring pattern. Read `internal/cli/cli_test.go` for the `Run` / `newRootCommand` test style and `clearEnv`. Read `internal/sync/sync_test.go` for the bare-remote fixture (for the `sync` no-config test).

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every `launchctl` call takes the caller's `ctx`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

The module path is `github.com/bborbe/git-ai-sync`. `internal/launchd` must NOT import `internal/cli`; the CLI wires it. The repo has no plist library; render the XML deterministically in Go with a fixed field order and `encoding/xml`'s `EscapeText` for values.

<!-- OPEN QUESTION for the reviewer: the spec's Acceptance Criterion 12 says `status` "exits non-zero" when the agent is not loaded, and the probe captures output. The test drives `newRootCommand` and asserts the returned error names the label rather than calling `cli.Execute` (which calls `os.Exit` and is not testable). The returned error is what `Execute` prints to stderr before exiting 1, so the seam is equivalent. Confirm. -->
</context>

<requirements>
1. Create `internal/launchd/launchd.go` (package `launchd`) with exactly this surface:

   ```go
   // Label is the single supervisor agent's launchd label.
   const Label = "com.github.bborbe.git-ai-sync"

   // Options configures install, uninstall and status.
   type Options struct {
       Config     config.FileConfig
       Logger     *slog.Logger
       HomeDir    string // defaults to os.UserHomeDir()
       Launchctl  string // defaults to "launchctl"
       BinaryPath string // defaults to the symlink-resolved os.Executable()
       UID        int    // defaults to os.Getuid()
   }

   func ResolveOptions(ctx context.Context, opts Options) (Options, error)
   func PlistPath(home string) string // <home>/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist
   func LogDir(home string) string    // <home>/Library/Logs/git-ai-sync
   func LogPath(home string) string   // <home>/Library/Logs/git-ai-sync/git-ai-sync.log

   // RenderPlist returns the deterministic plist XML.
   func RenderPlist(opts Options) ([]byte, error)

   // Install creates the log directory, writes the plist atomically, boots the
   // job out, and bootstraps it. A failed bootstrap returns an error naming the
   // manual bootstrap command.
   func Install(ctx context.Context, opts Options) error

   // Uninstall boots the job out (only when a plist exists) and removes the plist.
   func Uninstall(ctx context.Context, opts Options) error

   // IsLoaded reports whether `launchctl print gui/<uid>/<label>` exits 0.
   func IsLoaded(ctx context.Context, opts Options) (bool, error)
   ```

   Rules:
   - `ResolveOptions`: fill `HomeDir` from `os.UserHomeDir()` (wrap an error), `Launchctl` with `"launchctl"`, `UID` with `os.Getuid()` when zero, and `BinaryPath` from `os.Executable()` followed by `filepath.EvalSymlinks` (wrap either error). Return the filled struct.
   - `RenderPlist`: require `opts.BinaryPath != ""` and `opts.Config.ConfigPath != ""`; otherwise return an error. Produce exactly this document, with values XML-escaped via `xml.EscapeText` and a fixed field order (byte-identical across calls):
     ```xml
     <?xml version="1.0" encoding="UTF-8"?>
     <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
     <plist version="1.0">
     <dict>
       <key>Label</key>
       <string>com.github.bborbe.git-ai-sync</string>
       <key>ProgramArguments</key>
       <array>
         <string><BinaryPath></string>
         <string>supervise</string>
         <string>--config</string>
         <string><Config.ConfigPath></string>
       </array>
       <key>KeepAlive</key>
       <true/>
       <key>RunAtLoad</key>
       <true/>
       <key>ThrottleInterval</key>
       <integer>30</integer>
       <key>ProcessType</key>
       <string>Standard</string>
       <key>SoftResourceLimits</key>
       <dict>
         <key>NumberOfFiles</key>
         <integer>4096</integer>
       </dict>
       <key>HardResourceLimits</key>
       <dict>
         <key>NumberOfFiles</key>
         <integer>8192</integer>
       </dict>
       <key>EnvironmentVariables</key>
       <dict>
         <key>PATH</key>
         <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
       </dict>
       <key>StandardOutPath</key>
       <string><LogPath(HomeDir)></string>
       <key>StandardErrorPath</key>
       <string><LogPath(HomeDir)></string>
     </dict>
     </plist>
     ```
     The plist carries no `NumberOfProcesses` key, no `ResidentSetSize`, and no pushgateway credential. `EnvironmentVariables` carries only `PATH`.
   - `Install`:
     1. `resolved, err := ResolveOptions(ctx, opts)`; return the error.
     2. `if err := config.CheckPermissions(ctx, resolved.Config.ConfigPath); err != nil { return err }` (Acceptance Criterion 14).
     3. `os.MkdirAll(LogDir(resolved.HomeDir), 0o755)` and `os.MkdirAll(filepath.Dir(PlistPath(resolved.HomeDir)), 0o755)`; wrap failures.
     4. `data, err := RenderPlist(resolved)`.
     5. Write atomically: `os.CreateTemp(filepath.Dir(plistPath), ".plist-*")`, write `data`, `Chmod(0o644)`, `Sync`, `Close`, `os.Rename` to the plist path; `defer os.Remove(tmpName)`.
     6. `runLaunchctl(ctx, resolved, "bootout", fmt.Sprintf("gui/%d/%s", resolved.UID, Label))`; ignore a non-zero exit (the job may not be loaded) — log it at WARNING at most.
     7. `runLaunchctl(ctx, resolved, "bootstrap", fmt.Sprintf("gui/%d", resolved.UID), plistPath)`; on failure return `errors.Errorf(ctx, "launchctl bootstrap failed: %v\nRun manually:\n  launchctl bootstrap gui/%d %s", err, resolved.UID, plistPath)`. Never call `kickstart`.
     8. Return nil. A second `Install` writes the same bytes and exits 0.
   - `Uninstall`: resolve; if `os.Stat(plistPath)` reports `fs.ErrNotExist`, return nil **without** calling `launchctl`. Otherwise `runLaunchctl(ctx, resolved, "bootout", "gui/<uid>/<label>")` (tolerate non-zero), then `os.Remove(plistPath)` (ignore `fs.ErrNotExist`), then return nil.
   - `IsLoaded`: resolve; `out, err := runLaunchctl(ctx, resolved, "print", fmt.Sprintf("gui/%d/%s", resolved.UID, Label))`; if the command ran and exited 0 return `(true, nil)`; if it ran and exited non-zero return `(false, nil)`; if it could not start return `(false, err)`.
   - `runLaunchctl(ctx, opts, args ...string) (string, error)`: `exec.CommandContext(ctx, opts.Launchctl, args...)` with `CombinedOutput`; return the output and the error.
   - `Install` and `Uninstall` must be testable without touching the real launchd domain: the `launchctl` command is resolved from `Options.Launchctl` (a PATH stub works) and every path derives from `Options.HomeDir`.

2. Wire three commands in `internal/cli/cli.go`, each registering `--config` with `addConfigFlag`, and register all three in `newRootCommand`:
   - `newInstallCommand(ctx)` / `newUninstallCommand(ctx)`: `Use: "install"` / `"uninstall"`, `Args: cobra.NoArgs`, `RunE` that calls `fileOptions(runCtx, cmd)`, `config.LoadFile`, builds `logger := logging.New(cmd.ErrOrStderr(), cfg.LogLevel)`, and calls `launchd.Install(runCtx, launchd.Options{Config: cfg, Logger: logger})` or `launchd.Uninstall(...)`.
   - `newStatusCommand(ctx)`: `Use: "status"`, `Args: cobra.NoArgs`, `RunE`:
     1. `opts, err := fileOptions(runCtx, cmd)`; `cfg, err := config.LoadFile(runCtx, opts)`.
     2. `out := cmd.OutOrStdout()`; for each `cfg.Vaults`, `fmt.Fprintf(out, "vault: %s\n", vault)`.
     3. `state, err := supervise.ReadState(runCtx, cfg.StateFile)`:
        - `stderrors.Is(err, fs.ErrNotExist)` → `fmt.Fprintln(out, "last metrics push: never")`;
        - another error → return it;
        - otherwise `age := time.Since(state.LastMetricsPush)`; if `age < 0 { age = 0 }` (clamp a backwards clock); `fmt.Fprintf(out, "last metrics push: %s ago\n", age.Round(time.Second))`.
     4. `loaded, err := launchd.IsLoaded(runCtx, launchd.Options{Config: cfg, Logger: logger})`; return a non-nil error; if `!loaded`, print `agent: not loaded` and return `errors.Errorf(runCtx, "launchd agent %s is not loaded", launchd.Label)`; otherwise print `agent: loaded`.
     - Imports: stdlib `errors` aliased as `stderrors` (already present), `io/fs`, `time`, and `internal/supervise`.
   - All four config-file commands (`supervise`, `install`, `uninstall`, `status`) therefore read `LoadFile` first; a missing file returns an error naming the resolved path before any side effect.

3. Create `internal/launchd/launchd_test.go` (package `launchd`):
   - `TestRenderPlistShape` (Acceptance Criterion 10's plist half): `Options{HomeDir: t.TempDir(), BinaryPath: "/tmp/bin/git-ai-sync", UID: 501, Config: config.FileConfig{ConfigPath: "/tmp/config.yaml", Pushgateway: config.Pushgateway{URL: "https://pg", Username: "u", Password: "super-secret"}}}`. Render and assert the document contains: the label; the four `ProgramArguments` strings in order; `KeepAlive` + `<true/>`; `RunAtLoad` + `<true/>`; `ThrottleInterval` + `<integer>30</integer>`; `ProcessType` + `<string>Standard</string>`; `NumberOfFiles` + `<integer>4096</integer>` and `<integer>8192</integer>`; a `PATH` value containing `/usr/local/bin`; `StandardOutPath` and `StandardErrorPath` both `<HomeDir>/Library/Logs/git-ai-sync/git-ai-sync.log`; and it does **not** contain `NumberOfProcesses`, `ResidentSetSize`, or `super-secret`. Also assert the bytes are well-formed XML by reading every token with `xml.NewDecoder` to `io.EOF`.
   - `TestRenderPlistDeterministic`: two renders of the same options are byte-identical.
   - `TestPaths`: `PlistPath`, `LogDir`, `LogPath` return the expected joins under a temp home.
   - `TestRenderPlistEscapesValues`: a config path containing `&` renders as `&amp;` and still parses as XML.

4. Create `internal/cli/lifecycle_test.go` (package `cli`) with these helpers and cases:
   - `writeConfig(t, home, body string) string`: write `$HOME/.config/git-ai-sync/config.yaml` with mode `0600` (creating the directory) and return the path. The body names absolute temp vault directories.
   - `stubLaunchctl(t) (logPath string)`: write an executable `launchctl` shell script into a temp dir, prepend that dir to `PATH`, and `t.Setenv("LAUNCHCTL_LOG", logPath)`. The script appends `"$@"` to `$LAUNCHCTL_LOG` and exits 0, except when `$1` is `print` and `$LAUNCHCTL_PRINT_FAIL` is non-empty, in which case it exits 1.
   - `runCLI(t, args ...string) error`: `return Run(context.Background(), args)`. Call `clearEnv(t)` and `t.Setenv("HOME", home)` first.
   - `TestInstallWritesPlistAndBootstraps` (Acceptance Criterion 10): temp HOME, stub `launchctl`, a `0600` config naming one absolute temp vault; `runCLI(t, "install")` returns nil. Read `<HOME>/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist` and assert the shape from `TestRenderPlistShape`, that `ProgramArguments[0]` equals the symlink-resolved `os.Executable()`, and that the log directory `<HOME>/Library/Logs/git-ai-sync` exists. Read the `launchctl` log: it records a `bootout gui/...` line before a `bootstrap gui/...` line and no `kickstart` line.
   - `TestInstallIsIdempotent` (Acceptance Criterion 11): run `install` twice; both return nil; the plist bytes after the first and second run are equal.
   - `TestUninstallRemovesPlist` (Acceptance Criterion 11): install; record the `launchctl` log length; `uninstall` returns nil and removes the plist; exactly one new `bootout` line was appended. Run `uninstall` again: it returns nil and appends no new `bootout` line (the plist is already gone).
   - `TestInstallRefusesWorldReadableConfig` (Acceptance Criterion 14): write the config at `0644`; `install` returns an error whose message contains `mode` and `0600`; `chmod 0600`; `install` returns nil.
   - `TestStatusReportsVaultsAndFreshness` (Acceptance Criterion 12): temp HOME, stub `launchctl` whose `print` exits 0, a config naming two absolute vaults, and a state file at `<HOME>/.local/state/git-ai-sync/status.json` containing `{"last_metrics_push":"<time.Now().Add(-10*time.Second) in RFC3339Nano>","vaults":{}}`. Run `status` through `newRootCommand` with `cmd.SetOut(&buf)`; assert nil, both vault paths appear, and a `last metrics push: <duration> ago` line parses to under 60 s. Rewrite the config to name a third vault and drop one; run `status`; assert the new set appears and neither dropped path does. Remove the state file; run `status`; assert `last metrics push: never`. Set `LAUNCHCTL_PRINT_FAIL=1`; run `status`; assert the returned error is non-nil and contains `com.github.bborbe.git-ai-sync`.
   - `TestConfigRequiringCommandsRequireConfig` (Acceptance Criterion 2): temp HOME with no config file. For each of `status`, `install`, `uninstall`, `supervise`, `runCLI(t, name)` returns an error containing `filepath.Join(home, ".config/git-ai-sync/config.yaml")`. Then build a throwaway repository with a bare local remote (the `internal/sync/sync_test.go` fixture pattern), write a change, and `runCLI(t, "sync", repo)` returns nil with the bare remote's `master` advanced and no config file anywhere.
   - No test touches the real launchd domain, the real `~/Library/LaunchAgents`, or the operator's `~/.config`; every path derives from the temp `HOME`. No test is parallel. The suite must pass with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.

5. Rewrite `docs/launchd-service.md` for the Go path (Acceptance Criterion 16). It must:
   - describe one agent, `com.github.bborbe.git-ai-sync`, running `git-ai-sync supervise --config <path>`;
   - document `git-ai-sync install`, `git-ai-sync uninstall`, `git-ai-sync status`, and the `0600` config file;
   - state that the plist carries `ThrottleInterval` 30, `ProcessType` `Standard`, `NumberOfFiles` 4096 soft / 8192 hard, `KeepAlive`, `RunAtLoad`, an explicit `PATH`, and **no** credential in `EnvironmentVariables`;
   - point logs at `~/Library/Logs/git-ai-sync/git-ai-sync.log`;
   - keep the SSH / no-GUI-domain fallback but print only `launchctl bootstrap gui/$(id -u) <plist>` (never `kickstart -k`);
   - contain zero occurrences of `kickstart -k`, zero occurrences of `/tmp/git-ai-sync`, no `NumberOfFiles` value of 1024, and at least one occurrence of `git-ai-sync supervise`.
   Do not modify `docs/systemd-user-service.md`.

6. Append one bullet under the existing `## Unreleased` section in `CHANGELOG.md` (create it if absent, never a second one):

   ```markdown
   - feat: add `internal/launchd` plus `git-ai-sync install`, `uninstall` and `status`, a baseline-compliant single launchd agent for the supervisor, bootout-then-bootstrap loading, an idempotent plist, and a `docs/launchd-service.md` rewrite for the Go path
   ```
</requirements>

<constraints>
- The plist meets launchd baseline rules 1-6 and 10: `ThrottleInterval` 30, `SoftResourceLimits` `NumberOfFiles` 4096, `HardResourceLimits` `NumberOfFiles` 8192, no `NumberOfProcesses`, `ProcessType` `Standard`, an explicit `PATH`, logs under `~/Library/Logs/git-ai-sync/`, and no secret in `EnvironmentVariables`.
- `ProgramArguments` is the absolute, symlink-resolved binary path followed by `supervise --config <resolved config path>`.
- `install` loads with `bootout` then `bootstrap`; never `kickstart -k`, which re-runs the cached plist.
- `install` is idempotent (byte-identical plist, exit 0 both times). `uninstall` removes the plist and records one `bootout`; with no plist it exits 0 and records no `bootout`.
- `install` refuses a group- or world-readable config file, naming the permission. `0600` proceeds.
- `install`, `uninstall`, `status` and `supervise` require a config file; `watch`, `sync` and `resolve` do not and must keep working with no config file.
- `status` prints the configured vaults, the age of the last metrics push (fresh under 60 s), and whether the agent is loaded; it exits non-zero naming the unloaded agent when it is not loaded. A backwards clock clamps the age at zero.
- Secrets are never written into the plist, never echoed by `status`, and never logged.
- The `launchctl` invocation and the `~/Library/LaunchAgents` directory are injectable through `Options`, so `install`/`uninstall`/`status` are testable without touching the real launchd domain.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code. (`fmt.Fprintf`/`fmt.Fprintln` for status output are fine.)
- No test may touch the real launchd domain, the real `/usr/local/bin`, or the operator's `~/.config`.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT modify `internal/config`, `internal/supervise`, `internal/watch`, `internal/sync`, `internal/git`, or `internal/lock`; this prompt adds `internal/launchd`, the three CLI commands, the tests, the doc and the changelog bullet. Do not change `docs/systemd-user-service.md`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/launchd/... -count=1 -v
go test ./internal/cli/... -count=1 -v
go test ./... -count=1
make precommit-go
test "$(grep -c 'kickstart -k' docs/launchd-service.md)" = "0"
test "$(grep -c '/tmp/git-ai-sync' docs/launchd-service.md)" = "0"
test "$(grep -c 'git-ai-sync supervise' docs/launchd-service.md)" -ge 1
! grep -q 'NumberOfFiles.*1024' docs/launchd-service.md
```

Expected: the build, vet, both package test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing; the four documentation checks pass.

Before finishing, walk the assigned criteria against the tests:
- `install` writes one plist with the required label, `ProgramArguments`, `KeepAlive`, `RunAtLoad`, `ThrottleInterval` 30, `ProcessType` Standard, `NumberOfFiles` 4096/8192, an explicit `PATH`, the log paths under the test `HOME`, no `NumberOfProcesses`, no credential, and a `bootout`-before-`bootstrap` (no `kickstart`) sequence (Acceptance Criterion 10);
- a second `install` is byte-identical and exits 0; `uninstall` removes the plist and records one `bootout`; `uninstall` with nothing installed exits 0 with no `bootout` (Acceptance Criterion 11);
- `status` prints both vaults, a `last metrics push` age under 60 s, `never` when the state file is absent, follows a changed vault set, and exits non-zero naming the label when `print` fails (Acceptance Criterion 12);
- `install` refuses a `0644` config naming the permission and proceeds at `0600` (Acceptance Criterion 14);
- `status`, `install`, `uninstall` and `supervise` each fail naming `$HOME/.config/git-ai-sync/config.yaml` with no config file, while `sync` still pushes with none (Acceptance Criterion 2);
- the four `docs/launchd-service.md` checks pass (Acceptance Criterion 16).

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
