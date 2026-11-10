---
status: approved
spec: [010-multi-vault-supervisor]
created: "2026-11-10T09:21:02Z"
queued: "2026-11-10T09:44:52Z"
branch: dark-factory/multi-vault-supervisor
---

# Prove the pushgateway password never leaks

<summary>
- A full install plus two supervisor cycles never leak the pushgateway password.
- The password stays only in the `0600` config file.
- It is absent from the plist, the state file, the logs, and the captured stdout/stderr.
- The test walks the home tree and asserts zero occurrences outside the config file.
- The pushgateway still receives the credential, so the zero-leak result is meaningful.
- If the test finds a leak, the production code is fixed rather than the assertion weakened.
</summary>

<objective>
Add the end-to-end secret-isolation test the individual prompts cannot make on their own: install the agent, run two supervisor cycles against a stub pushgateway, and prove a distinctive config password appears nowhere under `$HOME/Library` or `$HOME/.local/state` and nowhere in the captured log. **Why:** the whole point of moving the credentials out of twelve world-readable plists is that the new surface does not reintroduce them in the plist, the state file or a log line.
</objective>

<context>
Read the approved spec at `specs/in-progress/010-multi-vault-supervisor.md` — Acceptance Criterion 13, the Security section, and the Constraints sentence "Secrets are never logged, never written into a vault file, never placed in the state file, and never echoed by `status`. The config loader must not log its own contents, at any level." The Go gate is `make precommit-go`, not `make precommit`.

Read `internal/config/file.go` for `FileConfig`, `FileOptions`, `LoadFile` and `CheckPermissions`. Read `internal/launchd/launchd.go` for `Install`, `Options`, `PlistPath`, `LogDir` and `LogPath`. Read `internal/supervise/supervise.go`, `internal/supervise/state.go`, `internal/supervise/metrics.go` and `internal/supervise/notifier.go` for `Run`, `Options`, `State`, `ReadState`, `newMetrics`, and the redaction helper. Read `internal/sync/sync_test.go` for the bare-remote fixture pattern.

Read the existing test helpers added by the earlier prompts (`newFixture`, `configure`, `runGit`, `writeFile`, the synchronized log buffer, `waitForLine`) and reuse them; do not duplicate them.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`

The module path is `github.com/bborbe/git-ai-sync`.

<!-- OPEN QUESTION for the reviewer: this prompt is deliberately test-only in the expected case. If the test exposes a leak, fix the production code (redact or remove the value) rather than weakening the assertion; the fix belongs in this prompt's diff so the criterion is actually met. -->
</context>

<requirements>
1. Create `internal/supervise/secret_test.go` (package `supervise`) with `TestCredentialsNeverLeaveTheConfigFile`:
   - `home := t.TempDir()`; `t.Setenv("HOME", home)`; `clearEnv(t)` if a helper is available in the package (otherwise set the relevant `GIT_AI_SYNC_*` variables to the empty string).
   - Create two throwaway repositories with bare local remotes using the package's existing fixture helper; write a change into each clone.
   - Write `<home>/.config/git-ai-sync/config.yaml` with mode `0600`:
     ```yaml
     vaults:
       - <absolute clone A>
       - <absolute clone B>
     interval: 1
     max_wait: 60
     strategy: merge
     commit_prefix: auto
     pushgateway:
       url: <httptest server URL>
       username: u
       password: <distinctive value>
     ```
     Use a distinctive password such as `s3cr3t-do-not-log-<random hex>` so a substring search cannot collide with ordinary output.
   - Start an `httptest.NewServer` that records each request's `Authorization` header and returns 200. The server is the pushgateway; do not use a real one.
   - Write an executable `launchctl` stub into a temp dir, prepend that dir to `PATH`, and `t.Setenv("LAUNCHCTL_LOG", <temp log path>)`. The stub appends `"$@"` to the log and exits 0.
   - `cfg, err := config.LoadFile(context.Background(), config.FileOptions{})` (the default path under the temp `HOME`); fail the test on an error.
   - Build a synchronized log buffer and `logger := logging.New(buf, slog.LevelDebug)`.
   - `if err := launchd.Install(context.Background(), launchd.Options{Config: cfg, Logger: logger}); err != nil { t.Fatalf(...) }`.
   - Start `Run` in a goroutine with a cancellable context and `Options{Config: cfg, Logger: logger, Clock: clock.New()}`; wait for at least two `push path=` lines (one per repository, or two cycles across both) using the existing `waitForLine` helper; cancel; wait for `Run` to return within 5 s.
   - Assert the stub pushgateway received at least one request whose `Authorization` header is `Basic <base64("u:"+password)>` — this proves the credential was actually read and used, so the zero-leak assertions are meaningful.
   - Walk `<home>/Library` and `<home>/.local/state` with `filepath.WalkDir`; for every regular file read its bytes and assert `!bytes.Contains(data, []byte(password))`. Report the offending path on failure. This is the Go equivalent of `grep -R -c '<value>' <home>/Library <home>/.local/state` returning 0.
   - Assert `!bytes.Contains(buf.Bytes(), []byte(password))` for the captured log.
   - Assert the plist at `launchd.PlistPath(home)` does not contain the password.
   - Do not read `<home>/.config` for the leak check: the config file is the one file that legitimately holds the value.
   - No test is parallel; `t.Setenv` is used throughout.

2. If the test fails, fix the production code in this prompt's diff — for example redact URL-shaped runs in any logged error, or stop logging a config field — and keep the assertion. Do not weaken the assertion to make the test pass, and do not add the password to the config file's exclusion beyond `<home>/.config`.

3. Do not add a CHANGELOG bullet; the earlier prompts already add them.
</requirements>

<constraints>
- The config file is the only file allowed to contain the password; the test excludes `<home>/.config` from the leak walk for that reason.
- The password must not appear in the plist, the state file, the log buffer, or any file under `$HOME/Library` or `$HOME/.local/state`.
- The pushgateway must still receive the credential (Basic auth), so the test proves the credential is used and not merely absent everywhere.
- No test may touch the real launchd domain, the real `~/Library/LaunchAgents`, a real pushgateway, or the operator's `~/.config`; every path derives from the temp `HOME`, and the pushgateway is an `httptest` server.
- New code (if any production fix is needed) uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err`.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT change the config, plist, state or notifier contracts; this prompt adds the integration test and, only if a leak is found, the minimal redaction fix.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/supervise/... -count=1 -v
go test ./... -count=1
make precommit-go
```

Expected: the build, vet, both test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing.

Before finishing, walk the assigned criterion against the test:
- with a distinctive password in the `0600` config, a full `install` plus two `supervise` cycles leave zero occurrences under `<tmp-home>/Library` and `<tmp-home>/.local/state`, and zero occurrences in the captured log; the plist and the state file are explicitly checked; the pushgateway received the credential (Acceptance Criterion 13).

The config file itself is excluded because it is the one file that legitimately holds the value. A loader that logs its own contents, a plist that carries a credential, or a state file that stores one fails this test.

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
