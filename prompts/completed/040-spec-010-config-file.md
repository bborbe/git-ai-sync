---
status: completed
spec: [010-multi-vault-supervisor]
summary: Added internal/config YAML config-file loader coverage (file_test.go) and the CHANGELOG Unreleased bullet; the loader itself (flags > file > environment > defaults, $HOME default paths, unknown-key/malformed rejection, 0600 check) was already present and verified against all requirements.
execution_id: 343bcb29-c431-540c-b449-0eef0950c240-exec-040-spec-010-config-file
dark-factory-version: dev
created: "2026-11-10T09:21:02Z"
queued: "2026-11-10T09:50:17Z"
started: "2026-11-10T09:54:12Z"
completed: "2026-11-10T09:55:38Z"
branch: dark-factory/multi-vault-supervisor
---

# Add the YAML config-file loader

<summary>
- The binary gains a YAML config file shared by the supervisor, install, uninstall and status.
- An explicit flag wins over the config file, the file wins over the environment, and the environment wins over the built-in defaults.
- The default config path is `$HOME/.config/git-ai-sync/config.yaml`; a missing or unreadable file is an error naming that path, never an empty vault set.
- The file carries the vault set, interval, max_wait, strategy, commit prefix, log level, stall threshold, attention settings, state file, and the pushgateway URL/username/password.
- The state file defaults to `$HOME/.local/state/git-ai-sync/status.json`.
- An unknown key or a malformed value is rejected with an error naming the file.
- A helper reports a group- or world-readable config file so `install` can refuse it.
- The three existing sync verbs keep their current flags → environment → defaults resolution and never read the config file.
- No secret is ever logged by the loader.
</summary>

<objective>
Add `internal/config`'s YAML config-file loader: the file shape, the `flags > file > environment > defaults` precedence, `$HOME`-derived default paths, unknown-key rejection, and the `0600` permission check that `install` will call. **Why:** this is the single source of truth every later supervisor and lifecycle command reads; without it the twelve plists keep drifting and the pushgateway password keeps living in world-readable files.
</objective>

<context>
Read the approved spec at `specs/in-progress/010-multi-vault-supervisor.md` — Desired Behavior 1-2, Acceptance Criteria 1, 14 and 15, the Failure Modes rows for a missing file / world-readable file / malformed YAML / unknown key, Constraints, and Security. The Go gate is `make precommit-go`, not `make precommit`.

Read `internal/config/config.go` in full. It already has `Config`, `Flags`, `Load`, `resolveInt`, `resolveString`, `resolveStrategy`, `resolveLogLevel`, `lookupNonEmpty`, and the unexported defaults `defaultInterval` (30), `defaultMaxWait` (300), `defaultCommitPrefix` ("auto"), `defaultStrategy` ("merge"), `defaultLogLevel` ("INFO"). Do not change `Load`'s behaviour; the three existing sync verbs keep it.

Read `internal/config/config_test.go` for the existing `clearEnv` helper (it sets `GIT_AI_SYNC_INTERVAL`, `GIT_AI_SYNC_MAX_WAIT`, `GIT_AI_SYNC_COMMIT_PREFIX`, `GIT_AI_SYNC_STRATEGY`, `LOG_LEVEL`, `GIT_AI_SYNC_LOG_LEVEL` to the empty string), `intPtr`, `strPtr`, and the stdlib-`testing` style this repo uses.

Read `go.mod` and `go.sum`. The module is `github.com/bborbe/git-ai-sync`, Go 1.27.3. `go.sum` and the module graph already carry `go.yaml.in/yaml/v3 v3.0.5` as a transitive dependency of `github.com/bborbe/errors`; the module cache has it. Use that module; it is the maintained successor of `gopkg.in/yaml.v3` with the same API, including `yaml.NewDecoder` and `Decoder.KnownFields`.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — wrap with `github.com/bborbe/errors` (`Errorf`, `Wrap`, `Wrapf`); sentinel errors use stdlib `errors` aliased as `stderrors`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — stdlib `testing`, not Ginkgo/Gomega.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` section goes after the SemVer preamble and before the newest version heading.

The module path is `github.com/bborbe/git-ai-sync`.

<!-- OPEN QUESTION for the reviewer: the spec names `gopkg.in/yaml.v3` as the YAML decoder. The repo's `go.sum` and module graph already carry `go.yaml.in/yaml/v3 v3.0.5` (the maintained successor module, same `yaml.NewDecoder` / `KnownFields` API) as a transitive dependency of `github.com/bborbe/errors`; `gopkg.in/yaml.v3` is not in `go.sum`. This prompt uses the already-present `go.yaml.in/yaml/v3` so no new module is fetched and the build stays reproducible. Confirm the substitution at audit; the spec's intent (a real decoder that rejects unknown keys, never a hand-rolled parser) is preserved. -->
<!-- OPEN QUESTION for the reviewer: the spec's Acceptance Criterion 15 says a malformed or unknown key makes `Load` error. `Load` already exists with the signature `Load(ctx, Flags)` and must keep its behaviour for `watch`/`sync`/`resolve`, so the new file loader is named `LoadFile`. The criterion's probe is satisfied by `LoadFile`; the test calls `LoadFile`. -->
</context>

<requirements>
1. Make `go.yaml.in/yaml/v3` a direct dependency. Import it from the new file as `yaml "go.yaml.in/yaml/v3"`, then run `go mod tidy` (the module is already in `go.sum`, so this needs no new download). The `require` block must end up listing `go.yaml.in/yaml/v3 v3.0.5` directly.

2. Create `internal/config/file.go` (package `config`) with exactly this exported surface:

   ```go
   // Pushgateway holds the metrics endpoint and its credentials.
   type Pushgateway struct {
       URL      string
       Username string
       Password string
   }

   // FileConfig is the resolved configuration for the config-file commands:
   // supervise, install, uninstall and status.
   type FileConfig struct {
       ConfigPath        string // the resolved path the file was read from
       Vaults            []string
       Interval          time.Duration
       MaxWait           time.Duration
       CommitPrefix      string
       Strategy          string // "merge" or "rebase"
       LogLevel          slog.Level
       StallThreshold    int
       AttentionScript   string
       AttentionStoreURL string
       StateFile         string
       Pushgateway       Pushgateway
   }

   // FileOptions carries the values the config-file commands were given.
   // A nil field means the value was not provided on the command line.
   type FileOptions struct {
       ConfigPath *string
       Interval   *int
       Strategy   *string
       LogLevel   *string
   }

   // DefaultConfigPath returns $HOME/.config/git-ai-sync/config.yaml.
   func DefaultConfigPath(ctx context.Context) (string, error)

   // DefaultStateFile returns $HOME/.local/state/git-ai-sync/status.json.
   func DefaultStateFile(ctx context.Context) (string, error)

   // LoadFile resolves FileConfig from the explicit flags, then the config
   // file, then the environment, then the built-in defaults.
   func LoadFile(ctx context.Context, opts FileOptions) (FileConfig, error)

   // CheckPermissions reports an error when path is group- or world-accessible.
   func CheckPermissions(ctx context.Context, path string) error
   ```

   Implementation rules:
   - `DefaultConfigPath` / `DefaultStateFile`: `home, err := os.UserHomeDir()`; wrap a failure with `errors.Wrapf(ctx, err, "resolve home directory")`; join `home` with `.config/git-ai-sync/config.yaml` and `.local/state/git-ai-sync/status.json` respectively using `filepath.Join`.
   - `LoadFile`:
     1. Resolve the path: use `*opts.ConfigPath` when non-nil and non-empty, else `DefaultConfigPath(ctx)`. Record it in `FileConfig.ConfigPath`.
     2. `data, err := os.ReadFile(path)`. On error return `errors.Wrapf(ctx, err, "read config file %s", path)` — the message must contain the resolved path (Acceptance Criteria 2 and 15). A missing file is this error, never an empty vault set.
     3. Decode into an unexported `fileConfig` whose fields are pointers so "absent" is distinguishable from a zero value:
        ```go
        type fileConfig struct {
            Vaults            []string        `yaml:"vaults"`
            Interval          *int            `yaml:"interval"`
            MaxWait           *int            `yaml:"max_wait"`
            Strategy          *string         `yaml:"strategy"`
            CommitPrefix      *string         `yaml:"commit_prefix"`
            LogLevel          *string         `yaml:"log_level"`
            StallThreshold    *int            `yaml:"stall_threshold"`
            AttentionScript   *string         `yaml:"attention_script"`
            AttentionStoreURL *string         `yaml:"attention_store_url"`
            StateFile         *string         `yaml:"state_file"`
            Pushgateway       *struct {
                URL      *string `yaml:"url"`
                Username *string `yaml:"username"`
                Password *string `yaml:"password"`
            } `yaml:"pushgateway"`
        }
        ```
        Decode with `dec := yaml.NewDecoder(bytes.NewReader(data)); dec.KnownFields(true); err := dec.Decode(&raw)`. On error return `errors.Wrapf(ctx, err, "parse config file %s", path)`. `KnownFields(true)` is what rejects an unknown key; do not add a bespoke key checker. An empty file decodes to `io.EOF`; treat it as an error naming the path.
     4. Vaults: the resolved `Vaults` must be non-empty; return `errors.Errorf(ctx, "config file %s names no vaults", path)` otherwise. Every entry must be an absolute path (`filepath.IsAbs`); otherwise return `errors.Errorf(ctx, "config file %s: vault path %q is not absolute", path, vault)`. Do not resolve or clean the paths beyond this check.
     5. Resolve `Interval` with this order: `opts.Interval` flag, then `raw.Interval`, then `GIT_AI_SYNC_INTERVAL`, then `defaultInterval`. Parse the env value with `strconv.Atoi` and wrap a parse failure with `errors.Wrapf(ctx, err, "parse %s=%q", "GIT_AI_SYNC_INTERVAL", value)`. Reject a negative value with `errors.Errorf(ctx, "interval must not be negative: %d", value)`. Store as `time.Duration(value) * time.Second`.
     6. Resolve `MaxWait` with: `raw.MaxWait`, then `GIT_AI_SYNC_MAX_WAIT`, then `defaultMaxWait`; same parse/negative rules and `time.Second` conversion.
     7. Resolve `CommitPrefix` with: `raw.CommitPrefix`, then `GIT_AI_SYNC_COMMIT_PREFIX`, then `defaultCommitPrefix`.
     8. Resolve `Strategy` with: `opts.Strategy` flag, then `raw.Strategy`, then `GIT_AI_SYNC_STRATEGY`, then `defaultStrategy`. Validate the final value is `merge` or `rebase`; otherwise `errors.Errorf(ctx, "invalid strategy %q: must be merge or rebase", value)`.
     9. Resolve `LogLevel` with: `opts.LogLevel` flag, then `raw.LogLevel`, then `LOG_LEVEL`, then `defaultLogLevel`. Parse with the same mapping `resolveLogLevel` uses (DEBUG/INFO/WARNING/ERROR/CRITICAL, case-insensitive); an unknown value is an error. Extract the existing switch into a new unexported `parseLogLevel(ctx context.Context, value string) (slog.Level, error)` and have both `resolveLogLevel` and `LoadFile` call it. This is a behaviour-preserving refactor of `config.go`.
     10. `StallThreshold`: `raw.StallThreshold` when set, else `10`. Reject `< 2` with `errors.Errorf(ctx, "stall_threshold must be at least 2: %d", value)`.
     11. `AttentionScript`: `raw.AttentionScript` when set, else the empty string.
     12. `AttentionStoreURL`: `raw.AttentionStoreURL` when set, else `"http://localhost:18080"`.
     13. `StateFile`: `raw.StateFile` when set, else `DefaultStateFile(ctx)`. Do not create the file or its directory here.
     14. `Pushgateway`: copy `URL`, `Username`, `Password` from `raw.Pushgateway` when set; each absent field is the empty string.
   - `CheckPermissions`: `os.Stat(path)`; wrap a stat failure with `errors.Wrapf(ctx, err, "stat config file %s", path)`. If `info.Mode().Perm()&0o077 != 0`, return `errors.Errorf(ctx, "config file %s is group/world-accessible (mode %04o); run chmod 0600", path, info.Mode().Perm())`. `0600` returns nil; `0644`, `0640`, `0604` and `0666` are refused. Do not call this from `LoadFile`; only `install` calls it (a later prompt).
   - The loader must never log. It must not print, and it must not pass the file contents to any logger. No secret is read into a log line.

3. Create `internal/config/file_test.go` (package `config`) covering:
   - `TestLoadFilePrecedence` (Acceptance Criterion 1): write a config file naming two vaults (both absolute temp paths) with `interval: 45` and `strategy: rebase`. Call `clearEnv(t)` first.
     - File only: `LoadFile(ctx, FileOptions{ConfigPath: &path})` gives `Interval == 45*time.Second`, `Strategy == "rebase"`, `len(Vaults) == 2`.
     - File plus environment: `t.Setenv("GIT_AI_SYNC_INTERVAL", "99")` and `t.Setenv("GIT_AI_SYNC_STRATEGY", "merge")`; the same call still gives 45 s and `rebase` (the file wins over the environment on both a numeric and a string field).
     - File plus flag: `LoadFile(ctx, FileOptions{ConfigPath: &path, Interval: intPtr(7)})` gives `Interval == 7*time.Second`.
   - `TestLoadFileEnvironmentFallbacks`: a file naming one vault and no interval/max_wait/commit_prefix/strategy/log_level; set `GIT_AI_SYNC_INTERVAL=7`, `GIT_AI_SYNC_MAX_WAIT=11`, `GIT_AI_SYNC_COMMIT_PREFIX=sync`, `GIT_AI_SYNC_STRATEGY=rebase`, `LOG_LEVEL=DEBUG`; assert the environment values are used (environment beats defaults).
   - `TestLoadFileMissingFile`: a path that does not exist; assert `err != nil` and `strings.Contains(err.Error(), path)`.
   - `TestLoadFileUnknownKey` (Acceptance Criterion 15): a config with an unknown top-level key (for example `bogus: 1`) and one valid vault; assert the error is non-nil and names the file path.
   - `TestLoadFileMalformedInterval` (Acceptance Criterion 15): `interval: not-a-number`; assert the error is non-nil and names the file path.
   - `TestLoadFileStallThreshold`: `stall_threshold: 1` errors; `stall_threshold: 2` succeeds with 2; an absent key yields 10.
   - `TestLoadFileDefaults`: with `t.Setenv("HOME", tmp)` and a file naming one vault, `StateFile` equals `filepath.Join(tmp, ".local/state/git-ai-sync/status.json")`, `AttentionStoreURL` equals `http://localhost:18080`, and `DefaultConfigPath(ctx)` / `DefaultStateFile(ctx)` return the expected `$HOME` paths.
   - `TestLoadFileRejectsRelativeVault`: `vaults: ["relative/path"]` errors and names the file.
   - `TestLoadFileRejectsEmptyVaults`: `vaults: []` errors and names the file.
   - `TestCheckPermissions`: a `0600` file returns nil; a `0644` file returns an error whose message contains `mode` and `0600`; a `0640` file is refused too.
   Each test that relies on `$HOME` calls `t.Setenv("HOME", t.TempDir())`; every test calls `clearEnv(t)` so the developer's environment cannot change the result. Do not use `t.Parallel()`.

4. Add a `## Unreleased` section to `CHANGELOG.md` directly after the SemVer preamble block and before `## v0.17.0`, containing one bullet:

   ```markdown
   - feat: add `internal/config`'s YAML config-file loader with `flags > file > environment > defaults` precedence, `$HOME`-derived default config and state paths, unknown-key and malformed-value rejection, and a `0600` permission check
   ```

   If `## Unreleased` already exists, append the bullet instead of creating a second section. Do not move or edit the `# Changelog` title or the preamble.
</requirements>

<constraints>
- `internal/config` gains the file loader. Do not change `Load`'s behaviour or the three existing sync verbs' flags → environment → defaults resolution.
- The four config-file commands (`supervise`, `install`, `uninstall`, `status`) are wired in later prompts; this prompt adds no CLI command.
- Precedence is fixed: explicit flags, then the config file, then the environment, then the built-in defaults. On `interval` and `strategy` the file must beat the environment; on `interval` the flag must beat the file.
- A missing or unreadable file is an error naming the resolved path, never an empty vault set.
- Unknown keys and malformed values are errors naming the file. Use `yaml.Decoder.KnownFields(true)`; do not hand-roll a parser.
- The config file is `0600`; `CheckPermissions` refuses any group- or world-accessible mode. `LoadFile` itself does not refuse the mode.
- The loader must never log its own contents or any credential.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT modify `internal/cli`, `internal/watch`, `internal/sync`, `internal/git`, `internal/lock`, `internal/quietwindow`, `internal/changetracker`, `internal/logging`, or `internal/clock`; this prompt is package-local to `internal/config` (plus `go.mod`/`go.sum` and `CHANGELOG.md`).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/config/... -count=1 -v
go test ./... -count=1
make precommit-go
```

Expected: the build, vet, both test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing.

Before finishing, walk the assigned criteria against the tests:
- a file naming two vaults with `interval: 45` and `strategy: rebase` resolves to 45 s / rebase, keeps those values when `GIT_AI_SYNC_INTERVAL=99` and `GIT_AI_SYNC_STRATEGY=merge` are set, and lets an explicit `--interval 7` flag win (Acceptance Criterion 1);
- an unknown top-level key and `interval: not-a-number` each return an error naming the file (Acceptance Criterion 15);
- `CheckPermissions` refuses `0644` and `0640` and accepts `0600`; the end-to-end `install` refusal is tested in the lifecycle prompt (Acceptance Criterion 14);
- a missing file errors naming the resolved path, and the three sync verbs are untouched (Acceptance Criterion 2's loader half; the command-level half is tested in the lifecycle prompt).

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
