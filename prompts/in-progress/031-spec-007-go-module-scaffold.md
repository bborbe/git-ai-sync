---
status: approved
spec: [007-go-sync-loop-prototype]
created: "2026-11-09T18:54:14Z"
queued: "2026-11-09T19:23:12Z"
branch: dark-factory/go-sync-loop-prototype
---

<!--
Open question surfaced at generation time: the spec's Suggested Decomposition lists a
"CI job" under prompt 1, but Acceptance Criterion 21 and CLAUDE.md forbid this change from
touching `.github/workflows/` (the GitHub App holds no `workflows` permission). This prompt
follows AC21 and leaves CI wiring out of the diff. Do not create, edit, or delete anything
under `.github/`.
-->

# Create the Go module scaffold and its build targets

<summary>
- The repository gains a Go module alongside the Python package, with a `git-ai-sync` binary that has `sync` and `watch` subcommands and a working `--version`.
- All five settings (poll interval, maximum wait, commit prefix, pull strategy, log level) are read from flags and environment variables, with a flag winning over the environment.
- Logging goes to stderr at the configured level.
- `make test-go` and `make precommit-go` run the Go build checks without touching the Python targets.
- The stale "always rebase, never merge" note in the project instructions is corrected to match the actual default.
- The Python package, its tests, and its existing Makefile targets are untouched.
- CI wiring is not part of this change; the workflow file is landed separately.
- The binary never calls the Python tool or any interpreter.
- The module builds, vets cleanly, and its tests pass.
</summary>

<objective>
Create the Go module scaffold at the repository root: `go.mod`, the `cmd/git-ai-sync` binary with cobra `sync`/`watch` stubs and `--version`, `internal/config`, `internal/logging`, `internal/clock`, `internal/clock/clocktest`, `internal/version`, and additive `test-go` / `precommit-go` Makefile targets; correct the stale strategy line in `CLAUDE.md`. **Why:** every later prompt builds on this module, its package layout, and its targets.
</objective>

<context>
Read `CLAUDE.md` for project conventions. Read the approved spec at `specs/in-progress/007-go-sync-loop-prototype.md` — it is the authority for this change.

Read `Makefile` — the existing `test`, `precommit`, `format`, `check`, `build` and other targets keep their current bodies; the new Go targets are additive only.

Read `src/git_ai_sync/__main__.py` and `src/git_ai_sync/config.py` for the settings, defaults and environment variable names the port must mirror. Do not port the Python tool; only mirror its configuration surface.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md` — use `github.com/spf13/cobra` (never the stdlib `flag` package) and the `Execute()` / `Run(ctx, args)` split.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — inject time; the real clock is the only place that calls `time.Now()`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — use `log/slog` to stderr with structured key-value pairs.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors` (`Wrapf`, `Wrap`, `Errorf`, `New`); never `fmt.Errorf`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md` for the general layout, but note that the spec explicitly places the binary under `cmd/git-ai-sync` and supporting packages under `internal/`; the spec wins over the generic flat-`pkg/` default.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` for the quality checklist.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the test conventions, but note that this prototype uses stdlib `testing` (not Ginkgo/Gomega) so the module adds no test-framework dependency; the spec's Go DoD is `gofmt` clean, `go vet` clean, `go test ./...` green, and exported identifiers documented. `docs/dod.md` is Python-only and does not apply to the Go tree.

The module path is `github.com/bborbe/git-ai-sync` (from the repository remote). Do not include the word `python` (any case) in any Go source file under `cmd/` or `internal/` — Acceptance Criterion 15 greps for it and treats any occurrence, including a comment, as a failure.
</context>

<requirements>
1. Create `go.mod` at the repository root with `module github.com/bborbe/git-ai-sync`. Set the `go` directive to a version the container toolchain supports (use `go 1.24` unless a dependency requires newer; `go mod tidy` may raise it). Add `github.com/spf13/cobra` and `github.com/bborbe/errors` as dependencies and run `go mod tidy` to produce `go.sum`. Do not vendor.

2. Create `internal/version/version.go`:

   ```go
   package version

   // Version is the version string printed by --version. It may be overridden at
   // build time with -ldflags "-X github.com/bborbe/git-ai-sync/internal/version.Version=<v>".
   var Version = "dev"
   ```

3. Create `internal/clock/clock.go` with the injectable clock every timing-sensitive package uses:

   ```go
   package clock

   import "time"

   // Clock supplies the current time. Production uses New(); tests use clocktest.NewFake.
   type Clock interface {
       Now() time.Time
   }

   // New returns a Clock backed by time.Now, which carries a monotonic reading.
   func New() Clock { return realClock{} }

   type realClock struct{}

   func (realClock) Now() time.Time { return time.Now() }
   ```

   `time.Now()` appears only in this file. Do not call `time.Now()` anywhere else in production code.

4. Create `internal/clock/clocktest/clocktest.go` — a test helper package (imported only by `_test.go` files) providing a controllable clock:

   ```go
   package clocktest

   // Fake is a Clock whose time is set by the test.
   type Fake struct {
       mu  sync.Mutex
       now time.Time
   }

   // NewFake returns a Fake clock reading now.
   func NewFake(now time.Time) *Fake

   // Now returns the fake's current time.
   func (f *Fake) Now() time.Time

   // SetNow sets the fake's current time.
   func (f *Fake) SetNow(now time.Time)

   // Advance moves the fake's current time forward by d.
   func (f *Fake) Advance(d time.Duration)
   ```

   Implement every body; the `Fake` satisfies `clock.Clock`.

5. Create `internal/config/config.go`:

   ```go
   package config

   import (
       "context"
       "log/slog"
       "time"
   )

   // Config is the resolved runtime configuration.
   type Config struct {
       Interval     time.Duration
       MaxWait      time.Duration
       CommitPrefix string
       Strategy     string // "merge" or "rebase"
       LogLevel     slog.Level
   }

   // Flags carries the command-line values that were explicitly set. A nil field
   // means the flag was not provided and the environment or default applies.
   type Flags struct {
       Interval *int
       Strategy *string
       LogLevel *string
   }

   // Load resolves Config from the explicitly set flags, then the environment, then
   // the defaults. An environment variable set to the empty string is treated as unset.
   func Load(ctx context.Context, flags Flags) (Config, error)
   ```

   Resolve each setting in this order (flag, then environment, then default):

   a. `interval`: flag `*flags.Interval` (integer seconds); environment `GIT_AI_SYNC_INTERVAL` (integer seconds); default `30`. Convert to `time.Duration` with `time.Duration(n) * time.Second`. A negative value is an error.

   b. `max_wait`: no flag; environment `GIT_AI_SYNC_MAX_WAIT` (integer seconds); default `300`. Convert to `time.Duration`. A negative value is an error. `0` is allowed and means "force every cycle"; `max_wait < interval` is not an error here — the watch loop warns at startup.

   c. `commit_prefix`: no flag; environment `GIT_AI_SYNC_COMMIT_PREFIX`; default `auto`.

   d. `strategy`: flag `*flags.Strategy`; environment `GIT_AI_SYNC_STRATEGY`; default `merge`. Accept only `merge` and `rebase`; any other value is an error.

   e. `log_level`: flag `*flags.LogLevel`; environment `LOG_LEVEL` (unprefixed, matching the Python argparse default); default `INFO`. Accept `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL` (case-insensitive) and map to `slog.LevelDebug`, `slog.LevelInfo`, `slog.LevelWarn`, `slog.LevelError`, and `slog.LevelError + 4` respectively. Any other value is an error. Do NOT read `GIT_AI_SYNC_LOG_LEVEL`; the port does not invent a prefixed alias for it.

   Read the environment only inside `Load` (`os.LookupEnv`), never elsewhere. Return every parse/validation failure through `github.com/bborbe/errors` (`errors.Errorf(ctx, ...)`).

6. Create `internal/logging/logging.go`:

   ```go
   package logging

   import (
       "io"
       "log/slog"
   )

   // New returns a text logger writing to w at the given level.
   func New(w io.Writer, level slog.Level) *slog.Logger
   ```

   Use `slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))`. Do not call `slog.SetDefault`; the logger is passed explicitly.

7. Create `internal/cli/cli.go` following the `Execute()` / `Run(ctx, args)` split from `go-cli-guide.md`:

   ```go
   package cli

   import (
       "context"
   )

   // Execute wires signal handling, runs the CLI, and exits non-zero on error.
   func Execute()

   // Run parses args and runs the selected command.
   func Run(ctx context.Context, args []string) error

   // newRootCommand builds the cobra command tree. Tests use it to capture --version output.
   func newRootCommand(ctx context.Context) *cobra.Command
   ```

   `Execute` must use `signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)`, call `Run(ctx, os.Args[1:])`, print a non-nil error to stderr and `os.Exit(1)`, and exit 0 when `Run` returns nil. A `context.Canceled` error is a graceful shutdown: return without printing and exit 0.

   `newRootCommand` builds:

   a. The root command: `Use: "git-ai-sync"`, `Short: "Automatic Git repository sync"`, `SilenceUsage: true`, `Version: version.Version`, and `SetVersionTemplate("git-ai-sync {{.Version}}\n")`. The `--version` flag is cobra's built-in; it must print exactly one line and exit 0.

   b. A `watch` subcommand with local flags `--interval` (int, default sentinel `-1`), `--strategy` (string, default `""`), `--log-level` (string, default `""`), and an optional positional `path` (default `"."`). In `RunE`, build `config.Flags` using `cmd.Flags().Changed("interval")`, `cmd.Flags().Changed("strategy")` and `cmd.Flags().Changed("log-level")`; call `config.Load`; call `logging.New(os.Stderr, cfg.LogLevel)`; then return `errors.New(ctx, "watch is not implemented")`. Prompt 5 replaces this stub with the real wiring.

   c. A `sync` subcommand with local flags `--strategy` (string, default `""`), `--log-level` (string, default `""`), and an optional positional `path` (default `"."`). In `RunE`, do the same config/logging resolution and return `errors.New(ctx, "sync is not implemented")`. Prompt 5 replaces this stub.

   Do not add `resolve`, `status`, `doctor`, `config`, `version` or any other subcommand. The only commands are `sync` and `watch`, plus the root `--version` flag.

   `Run` calls `newRootCommand(ctx)`, `rootCmd.SetArgs(args)`, and returns `rootCmd.ExecuteContext(ctx)`.

8. Create `cmd/git-ai-sync/main.go`:

   ```go
   package main

   import "github.com/bborbe/git-ai-sync/internal/cli"

   func main() {
       cli.Execute()
   }
   ```

9. Append to `Makefile` — additive only, do not modify or remove any existing line. Recipe lines must start with a tab, not spaces:

   ```make
   .PHONY: test-go precommit-go

   test-go:
   	go test ./... -count=1

   precommit-go:
   	@test -z "$$(gofmt -l cmd internal)" || (echo "gofmt needed:"; gofmt -l cmd internal; exit 1)
   	go vet ./...
   	go test ./... -count=1
   ```

   Do not modify the existing `.PHONY` line or any existing target body. `precommit-go` must not invoke `uv`. Do not add `precommit-go` to the existing `precommit` target.

10. Correct the two stale strategy lines in `CLAUDE.md`. Replace exactly:

    - ``- `src/git_ai_sync/git_operations.py` — Git operations: status, stage, commit, pull --rebase, push, conflict detection``
      with ``- `src/git_ai_sync/git_operations.py` — Git operations: status, stage, commit, pull (merge or rebase), push, conflict detection``

    - `- **Sync flow:** stage → commit → pull --rebase → push (always rebase, never merge)`
      with ``- **Sync flow:** stage → commit → pull --no-rebase → push (merge by default; `--strategy rebase` opts into rebase)``

    Do not change any other line in `CLAUDE.md`.

11. Add tests using stdlib `testing`:

    a. `internal/config/config_test.go` — table or subtests covering: the defaults with every environment variable set to the empty string (`Interval == 30*time.Second`, `MaxWait == 300*time.Second`, `CommitPrefix == "auto"`, `Strategy == "merge"`, `LogLevel == slog.LevelInfo`); each environment override; each flag overriding its environment value; `LOG_LEVEL` (unprefixed) controlling the level while `GIT_AI_SYNC_LOG_LEVEL` has no effect; an invalid integer, an invalid strategy and an invalid log level each returning an error; `max_wait = 0` being accepted; a negative interval being rejected. Use `t.Setenv`; do not use `t.Parallel` with `t.Setenv`.

    b. `internal/logging/logging_test.go` — `New(&buf, slog.LevelInfo)` writes an info record to the buffer and suppresses a debug record.

    c. `internal/clock/clocktest/clocktest_test.go` — `Now`, `Advance` and `SetNow` behave as documented.

    d. `internal/cli/cli_test.go` — `TestVersionPrintsOneLine` builds `newRootCommand(context.Background())`, calls `SetOut(&buf)` and `SetArgs([]string{"--version"})`, executes, and asserts the buffer has exactly one newline and starts with `git-ai-sync `.
</requirements>

<constraints>
- Do NOT modify `src/`, `tests/`, `pyproject.toml`, `uv.lock`, `Dockerfile`, `entrypoint.sh`, `README.md`, `docs/`, `hack/`, or `CHANGELOG.md`.
- Do NOT create, edit, or delete anything under `.github/` — CI wiring is outside this diff by Acceptance Criterion 21 and `CLAUDE.md`.
- Do NOT modify any existing Makefile line; append the two new targets and a new `.PHONY` line only.
- Do NOT add Ginkgo, Gomega or Counterfeiter; use stdlib `testing`.
- Do NOT include the word `python` (any case) in any Go source file under `cmd/` or `internal/` — Acceptance Criterion 15 greps for it.
- Do NOT execute any interpreter or subprocess in this prompt; the only subprocess the finished binary may run is `git`, and that lands in prompt 2.
- Do NOT add `resolve`, `status`, `doctor`, `config` or any other subcommand. The binary has `sync`, `watch` and `--version`.
- Do NOT add metrics, notifier, conflict resolution, launchd, or any Python-side change.
- Do NOT add a refresh-interval knob or any setting beyond the five listed in the spec.
- Use `github.com/bborbe/errors` for wrapping; never `fmt.Errorf`.
- `gofmt`, `go vet ./...` and `go test ./... -count=1` must pass. Run `make precommit-go`, not `make precommit` — the Python toolchain is out of scope for this prompt.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
go test ./... -count=1
make test-go
make precommit-go
CGO_ENABLED=0 go build ./cmd/git-ai-sync
grep -nE '^(test-go|precommit-go):' Makefile
go build -o /tmp/git-ai-sync ./cmd/git-ai-sync
/tmp/git-ai-sync --version | wc -l
/tmp/git-ai-sync --version >/dev/null; echo $?
grep -c 'never merge' CLAUDE.md || true
grep -c 'pull --rebase' CLAUDE.md || true
grep -n 'pull --no-rebase' CLAUDE.md
git diff origin/master -- src/ tests/ pyproject.toml uv.lock
git diff origin/master -- Makefile | grep -cE '^-[^-]' || true
grep -rn 'python' --include='*.go' cmd/ internal/ | wc -l
grep -A2 -E '^(test-go|precommit-go):' Makefile | grep -c 'uv ' || true
```

Expected: `go build`, `go vet`, `go test`, `make test-go`, `make precommit-go` and `CGO_ENABLED=0 go build` exit 0; the Makefile grep prints two lines; `--version` prints exactly one line and exits 0; both stale-string greps print `0`; the `pull --no-rebase` grep prints at least one line; the Python diff is empty; the Makefile removed-line count is `0`; the Go-source `python` grep prints `0`; the `uv` count in the new targets is `0`.

The first `go test`/`make precommit-go` may fetch Go modules over the network; that is expected. The tests themselves must not use the network.

Re-run this verification before finishing and walk each acceptance criterion this prompt covers: build and tests pass; `--version` exits 0 with one line; `CGO_ENABLED=0 go build ./cmd/git-ai-sync` exits 0; both Makefile targets exist and pass; the Python tree is untouched; the stale strategy lines are corrected.
</verification>
