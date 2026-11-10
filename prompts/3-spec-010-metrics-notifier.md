---
status: draft
spec: [010-multi-vault-supervisor]
created: "2026-11-10T09:21:02Z"
branch: dark-factory/multi-vault-supervisor
---

# Add metrics, the serialised state file, and the stall notifier

<summary>
- Every sync cycle pushes a per-vault metrics heartbeat to the pushgateway with POST, never PUT.
- A successful cycle also pushes the last-success timestamp in the same POST; a failed cycle updates only the heartbeat.
- One collector serialises every vault's state-file update; the state directory is created at startup.
- The state file records each vault's consecutive failures and last success for `status` to read.
- N consecutive failed cycles post exactly one attention card at the threshold; the next success closes it.
- A missing or failing notifier script disables alerting with one startup warning and never fails a cycle.
- A hanging notifier call is abandoned at 5 s with one warning, no retry, and the next cycle still runs.
- The child gets `ATTENTION_STORE_URL` from the same config value the close uses, so a post and its close cannot disagree.
- No secret is written to the state file or logged.
</summary>

<objective>
Extend `internal/supervise` with the two out-of-band channels: `POST` metrics heartbeats plus last-success per vault, a single serialised state-file collector, and a best-effort stall notifier that posts one card at the configured consecutive-failure threshold and closes it on recovery. **Why:** the metrics heartbeat and the stall alert are the only signals that a supervisor running twelve vaults is still healthy; a `PUT` heartbeat or a per-loop state writer would silently destroy both.
</objective>

<context>
Read the approved spec at `specs/in-progress/010-multi-vault-supervisor.md` — Desired Behavior 5-6, Acceptance Criteria 5, 6, 7 and 8, the Failure Modes rows for an unreachable pushgateway / a 429 / an unreachable or missing attention store / a hanging script / a store that rejects the card / two loops updating the state file concurrently, Constraints, and Security. The Go gate is `make precommit-go`, not `make precommit`.

Read the reference implementation in `src/git_ai_sync/metrics.py` and `src/git_ai_sync/notifier.py` (read-only reference; do not modify Python). Match:
- `metrics.py`: `JOB = "git_ai_sync"`, `HEARTBEAT_METRIC = "git_ai_sync_heartbeat_timestamp"`, `LAST_SUCCESS_METRIC = "git_ai_sync_last_success_timestamp"`, `PUSH_TIMEOUT_SECONDS = 5`, the URL `{base}/metrics/job/{JOB}/instance/{quote(vault)}`, the `# TYPE <metric> gauge` body, the `text/plain; version=0.0.4` content type, Basic auth when a username is set, and **POST never PUT**.
- `notifier.py`: `PRODUCER_KIND = "cron"`, the 32-lowercase-hex item id, the `ITEM_ID: ` stdout prefix, `dedup_key = "git-ai-sync-<vault>-stalled"`, `producer_id = "git-ai-sync-<vault>"`, the two options and the `--recommend` pointing at the actionable one, the 5 s subprocess timeout, the `POST {store}/api/1.0/attention/{item_id}/close` close, and the URL redaction before truncation.

Read `docs/attention-store.md` — it is the card contract; do not re-derive it. Read `internal/supervise/supervise.go` from the previous prompt for `Run`, `runVault`, `runnerFor`, `Options`, `Label` and `sleepContext`. Extend `runnerFor`; do not replace the concurrency or restart logic.

Read `internal/config/file.go` for `FileConfig.Pushgateway` (`URL`, `Username`, `Password`), `AttentionScript`, `AttentionStoreURL`, `StallThreshold`, and `StateFile`. Read `internal/git/git.go` for `RevParse` and the wrapping style. Read `internal/sync/sync.go` for `Result` (`Pushed`, `CommitErr`). Read `internal/sync/sync_test.go` for the local bare-remote fixture.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — one mutex-protected collector; no goroutine outlives the call.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every HTTP request and subprocess takes the caller's `ctx`, with a 5 s bound.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

The module path is `github.com/bborbe/git-ai-sync`. `internal/supervise` must NOT import `internal/cli`.

<!-- OPEN QUESTION for the reviewer: `metrics.py`'s `vault_label` returns `repo_path.name` unsanitised, while the spec's Desired Behavior 6 says the label is the sanitised derivation `docs/launchd-service.md` documents and `metrics.py` implements. This port uses `supervise.Label(vault)` (the sanitised derivation) for both the metrics body label and the notifier dedup/producer ids, so the two channels name a vault the same way. Confirm the sanitised label for metrics. -->
<!-- OPEN QUESTION for the reviewer: the spec does not name the interpreter for `attention-ask.py`. The docs say every caller runs it through the interpreter (the installed script is mode 100644), so this port resolves `python3` (falling back to `python`) once at startup and invokes `python3 <script> post ...` as an argument vector. If no interpreter is found the notifier is disabled with one warning, exactly like a missing script. Confirm. -->
</context>

<requirements>
1. Create `internal/supervise/metrics.go` (package `supervise`):

   ```go
   const (
       metricsJob         = "git_ai_sync"
       heartbeatMetric    = "git_ai_sync_heartbeat_timestamp"
       lastSuccessMetric  = "git_ai_sync_last_success_timestamp"
       metricsTimeout     = 5 * time.Second
   )

   type metricsClient struct {
       pushgateway config.Pushgateway
       http        *http.Client
       logger      *slog.Logger
   }

   func newMetrics(pg config.Pushgateway, logger *slog.Logger) *metricsClient
   func (m *metricsClient) enabled() bool
   // push sends one POST carrying the heartbeat always and the last-success metric
   // when success is true. It returns true only on a 2xx response.
   func (m *metricsClient) push(ctx context.Context, label string, now time.Time, success bool) bool
   ```

   Rules:
   - `enabled()` is `m.pushgateway.URL != ""`.
   - `push`: build `base := strings.TrimRight(m.pushgateway.URL, "/")`; URL `base + "/metrics/job/" + metricsJob + "/instance/" + url.PathEscape(label)`.
   - Body (one POST per cycle):
     ```text
     # TYPE git_ai_sync_heartbeat_timestamp gauge
     git_ai_sync_heartbeat_timestamp{vault="<label>"} <now.Unix()>
     ```
     followed, when `success`, by:
     ```text
     # TYPE git_ai_sync_last_success_timestamp gauge
     git_ai_sync_last_success_timestamp{vault="<label>"} <now.Unix()>
     ```
     Escape `\` and `"` in the label as `metrics.py` does.
   - `http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))`; set `Content-Type: text/plain; version=0.0.4`; when `Username != ""`, set `Authorization: Basic <base64(username:password)>`.
   - Wrap the request context with `context.WithTimeout(ctx, metricsTimeout)`; the client is `&http.Client{}`.
   - On a transport error, a non-2xx status, or a 429, log `logger.Warn("metric push failed", "vault", label, "error", <redacted error>)` and return false. Never return an error and never fail a cycle.
   - Never log the password. The URL carries no password; redact URL-shaped runs from any error string before logging.

2. Create `internal/supervise/state.go` (package `supervise`):

   ```go
   // VaultState is one vault's persisted state.
   type VaultState struct {
       ConsecutiveFailures int       `json:"consecutive_failures"`
       LastSuccess         time.Time `json:"last_success,omitempty"`
       LastError           string    `json:"last_error,omitempty"`
   }

   // State is the persisted supervisor state read by status.
   type State struct {
       LastMetricsPush time.Time              `json:"last_metrics_push"`
       Vaults          map[string]*VaultState `json:"vaults"`
   }

   // ReadState reads the state file. A missing file returns an error that wraps
   // fs.ErrNotExist.
   func ReadState(ctx context.Context, path string) (State, error)

   type stateCollector struct { /* path, logger, mutex, state */ }

   func newStateCollector(ctx context.Context, path string, logger *slog.Logger) (*stateCollector, error)
   func (c *stateCollector) RecordSuccess(repo string, now time.Time)
   func (c *stateCollector) RecordFailure(repo string, now time.Time, consecutiveFailures int, lastError string)
   func (c *stateCollector) RecordMetricsPush(now time.Time)
   ```

   Rules:
   - `ReadState`: `os.ReadFile(path)`; on error return `errors.Wrapf(ctx, err, "read state file %s", path)` (a missing file therefore wraps `fs.ErrNotExist`). `json.Unmarshal` into `State`; wrap a decode error naming the path. Ensure `State.Vaults` is non-nil.
   - `newStateCollector`: `os.MkdirAll(filepath.Dir(path), 0o755)`; wrap a failure naming the directory. Start from an empty `State{Vaults: map[string]*VaultState{}}`; do not load an existing file. Return the collector.
   - All three `Record*` methods take a single mutex, mutate `c.state`, then call an unexported `write(ctx)`.
   - `write` is atomic: `os.CreateTemp(filepath.Dir(c.path), ".status-*")`, `json.MarshalIndent(c.state, "", "  ")`, write, `Chmod(0o600)`, `Sync`, `Close`, `os.Rename` to `c.path`, and `defer os.Remove(tmpName)` so a failure leaves no temp file. Log a write failure at WARNING; never fail a cycle.
   - `RecordSuccess`: set the vault's `ConsecutiveFailures = 0`, `LastSuccess = now`, `LastError = ""`.
   - `RecordFailure`: set `ConsecutiveFailures` to the passed count and `LastError` to `redactURLs(lastError)` (the same helper the notifier uses; define it once).
   - `RecordMetricsPush`: set `LastMetricsPush = now`.
   - One collector instance is the only writer of the state file. Every vault's cycle goes through it; no loop writes the file directly.

3. Create `internal/supervise/notifier.go` (package `supervise`):

   ```go
   const (
       producerKind   = "cron"
       itemIDHexLen   = 32
       notifierTimeout = 5 * time.Second
       optionActionable = "Investigate the vault's sync failure now"
       optionDeferring  = "Leave it — it will retry automatically"
   )

   type notifier struct {
       active      bool // true only when the script and its interpreter are usable
       script      string
       interpreter string
       storeURL    string
       logger      *slog.Logger
   }

   func newNotifier(ctx context.Context, scriptPath, storeURL string, logger *slog.Logger) *notifier
   func (n *notifier) enabled() bool // returns n.active
   func (n *notifier) post(ctx context.Context, label, repo string, count int, firstFailureAt time.Time, lastError string) (string, bool)
   func (n *notifier) close(ctx context.Context, itemID string)

   // stallTracker owns the per-vault consecutive-failure counters and outstanding
   // card ids. It is mutex-protected because the vault goroutines share it.
   type stallTracker struct { /* mutex, map[string]*stallState */ }
   type stallState struct {
       consecutiveFailures int
       outstandingItemID   string
       firstFailureAt      time.Time
   }

   func newStallTracker() *stallTracker
   func (t *stallTracker) recordFailure(repo string, now time.Time) (count int, firstFailureAt time.Time)
   func (t *stallTracker) recordSuccess(repo string) (outstandingItemID string)
   func (t *stallTracker) outstanding(repo string) string
   func (t *stallTracker) setOutstanding(repo, itemID string)
   ```

   Rules:
   - `newNotifier`: if `scriptPath == ""`, return a disabled notifier and log nothing. Otherwise `os.Stat` it: it must be a regular, readable file (the executable bit is deliberately NOT required — the installed script is mode 100644). On failure log exactly one `logger.Warn("attention stall alert disabled", "script", scriptPath, "reason", ...)` and return disabled. Resolve the interpreter with `exec.LookPath("python3")`, then `exec.LookPath("python")`; if neither exists, log the same single warning naming the script and return disabled. Store the resolved interpreter.
   - `post`: build the payload `"<label>: sync has failed <count> consecutive cycles"` and the context `"Repository <repo> has not synced since <firstFailureAt UTC RFC3339>. Last error: <redactURLs(lastError)>"`. Build the argv as a slice (never a shell string):
     ```go
     []string{n.interpreter, n.script, "post",
       "--dedup-key", "git-ai-sync-" + label + "-stalled",
       "--producer-id", "git-ai-sync-" + label,
       "--producer-kind", producerKind,
       "--payload", payload,
       "--context", contextText,
       "--option", optionActionable,
       "--option", optionDeferring,
       "--recommend", optionActionable,
     }
     ```
     Use `context.WithTimeout(ctx, notifierTimeout)`, `exec.CommandContext(timeoutCtx, argv[0], argv[1:]...)`, `cmd.Env = append(os.Environ(), "ATTENTION_STORE_URL="+n.storeURL)`, and `cmd.CombinedOutput()`.
     - On `timeoutCtx.Err() == context.DeadlineExceeded`: log `logger.Warn("attention post timed out", "vault", label, "timeout", "5s")` and return `("", false)`. Exactly one invocation; no retry.
     - On a non-zero exit or a start failure: log `logger.Warn("attention post failed", "vault", label, "error", ...)` with a bounded output tail and return `("", false)`.
     - Parse the first stdout line with prefix `ITEM_ID: `; the candidate is the trimmed remainder. Accept it only when it is exactly 32 characters of `0-9a-f`. On an unreadable id, log a WARNING and return `("", false)`.
     - Return `(id, true)`.
   - `close`: return immediately when `itemID` is not a valid 32-hex id (log a WARNING). `url := strings.TrimRight(n.storeURL, "/") + "/api/1.0/attention/" + itemID + "/close"`; `http.NewRequestWithContext` with `http.MethodPost`, a 5 s timeout, and no body; on a transport error, a non-2xx status (including 409) log `logger.Warn("attention close failed", "item_id", itemID, "error", ...)`. Never retry.
   - `redactURLs(s string) string`: replace every match of `[A-Za-z][A-Za-z0-9+.-]*://\S+` with `<url>`, then truncate to 200 characters (redact before truncating). Define it once in this package and use it for the card context and the state file's `last_error`.
   - `stallTracker`: `recordFailure` stamps `firstFailureAt = now` on the 0→1 transition and increments; `recordSuccess` returns and clears the outstanding id, resets the count and `firstFailureAt`; `outstanding` returns the id or `""`; `setOutstanding` stores it.

4. Wire the three channels into `internal/supervise/supervise.go`:
   - In `Run`, before starting any goroutine:
     1. `metrics := newMetrics(opts.Config.Pushgateway, opts.Logger)`.
     2. `collector, err := newStateCollector(ctx, opts.Config.StateFile, opts.Logger)`; if it returns an error, return it (no loop starts, no state file is written).
     3. `notifier := newNotifier(ctx, opts.Config.AttentionScript, opts.Config.AttentionStoreURL, opts.Logger)` (the threshold is checked by the runner, not the notifier).
     4. `tracker := newStallTracker()`.
     Pass these to `runnerFor` (change its signature to accept them).
   - Extend `runnerFor`'s closure, after `base(ctx, repo, sopts)` returns:
     1. Capture `now := opts.Clock.Now()` once. `success := err == nil && res.CommitErr == nil`; `lastError` is `err.Error()` when `err != nil`, else `res.CommitErr.Error()` when `res.CommitErr != nil`, else `""`.
     2. Keep the existing `push` log: when `err == nil && res.Pushed`, resolve `git.RevParse(ctx, repo, "HEAD")` and `logger.Info("push", "path", repo, "sha", sha)`.
     3. When `metrics.enabled()`: `if metrics.push(ctx, Label(repo), now, success) { collector.RecordMetricsPush(now) }`.
     4. On success: `if id := tracker.recordSuccess(repo); id != "" { notifier.close(ctx, id) }`; `collector.RecordSuccess(repo, now)`.
     5. On failure: `count, first := tracker.recordFailure(repo, now)`; `collector.RecordFailure(repo, now, count, lastError)`; when `notifier.enabled() && count >= opts.Config.StallThreshold && tracker.outstanding(repo) == ""`, call `notifier.post(...)` and, on `ok`, `tracker.setOutstanding(repo, id)`. A post is attempted only once per stall run because the outstanding guard blocks a second one.
   - The notifier and metrics are best-effort: their failures never change `res`/`err` and never abort a cycle.
   - Never log `opts.Config`, the pushgateway password, or any config field value.

5. Create `internal/supervise/metrics_notifier_test.go` (package `supervise`) with the local bare-remote fixture from the previous prompt and these cases:
   - `TestMetricsHeartbeatIsPOST` (Acceptance Criterion 5): `httptest.NewServer` recording each `r.Method`, `r.URL.Path` and body. One repository with a change; `Config{Interval: time.Second, MaxWait: time.Minute, CommitPrefix: "auto", Strategy: "merge", Pushgateway: config.Pushgateway{URL: server.URL, Username: "u", Password: "p"}}`. Start `Run`, wait for two `push` lines, cancel. Assert every recorded method is `POST` and no request is `PUT`; assert the union of bodies contains `git_ai_sync_heartbeat_timestamp` and `git_ai_sync_last_success_timestamp`; assert at least one request carried a Basic `Authorization` header.
   - `TestStallCardPostsOnceAndClosesOnRecovery` (Acceptance Criterion 6): one repository; make the remote unreachable with `git remote set-url origin <missing>`. Write a Python stub `attention-ask.py` into a temp dir (mode 0644), prepend its dir to `PATH`, and set `Config.AttentionScript` to its absolute path and `Config.AttentionStoreURL` to an `httptest.NewServer` that records `r.URL.Path`. The stub appends `json.dumps(sys.argv[1:])` and `os.environ.get("ATTENTION_STORE_URL")` to a log file and prints `ITEM_ID: <32 lowercase hex>`. Use `StallThreshold: 2`, `Interval: time.Second`. Start `Run`; poll until exactly one invocation is recorded (timeout 10 s). Assert the argv contains `--dedup-key git-ai-sync-<label>-stalled`, `--producer-id git-ai-sync-<label>`, `--producer-kind cron`, exactly two `--option` values with `--recommend` naming one, a `--payload` containing `2`, and a `--context` containing the repository path; assert the recorded `ATTENTION_STORE_URL` equals the configured store URL; assert the post happened on the second failed cycle (the payload names 2). Restore the remote with `git remote set-url origin <bare>`, poll for exactly one store request whose path is `/api/1.0/attention/<id>/close`, assert no second close, then cancel. Assert the stub was invoked exactly once in total.
   - `TestMissingNotifierDisablesAlerting` (Acceptance Criterion 7): one repository with an unreachable remote; `AttentionScript` points at a non-existent path; `StallThreshold: 2`; `Interval: time.Second`. Start `Run`, poll the state file until the vault's `consecutive_failures` is at least 3 (timeout 10 s), then cancel. Assert the log contains exactly one line naming the missing path and containing `attention stall alert disabled`; assert no `attention post` line; assert the state file records `consecutive_failures: 3` for the vault (read it with `ReadState`).
   - `TestHangingNotifierIsBounded` (Acceptance Criterion 8): one repository with an unreachable remote; a Python stub that appends its invocation to a log and then `time.sleep(10)`; `StallThreshold: 2`; `Interval: time.Second`. Start `Run`; poll the log for the second failed-cycle marker, record `t2 := time.Now()`, then poll for a WARNING containing `timed out` (timeout 8 s) and assert `time.Since(t2) < 7*time.Second`; assert the stub invocation count is exactly 1; poll for a subsequent cycle line (a third failure or `Checking`) proving the loop continued; cancel.
   - Use a synchronized log buffer for all four tests; `t.Setenv("GIT_EDITOR", "true")`; no test is parallel. The suite must pass with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.

6. Append one bullet under the existing `## Unreleased` section in `CHANGELOG.md` (create it if absent, never a second one):

   ```markdown
   - feat: push per-vault metrics heartbeats with `POST`, serialise state-file updates through one collector, and post one attention card per stalled vault at the configured threshold, closing it on recovery with a 5 s bound on both calls
   ```
</requirements>

<constraints>
- Metrics are `POST`ed, never `PUT`. A `PUT` replaces the whole grouping-key group and erases the last-success series.
- One writer owns the state file: every update goes through the single collector, which serialises with a mutex and writes atomically. The state directory is created at startup.
- Consecutive failures are counted per vault; at the configured threshold (default 10, minimum 2, already validated by the config loader) exactly one card is posted, and the next successful cycle closes it.
- The notifier is best-effort with a 5 s bound on both the post and the close: a missing script, a non-zero exit, a timeout or an unreachable store is logged at WARNING and never changes a cycle's outcome. No retry within a call.
- `ATTENTION_STORE_URL` is exported into the child's environment from the same config value the close uses.
- Secrets are never logged, never written to the state file, and never placed in a card. Redact URL-shaped runs before storing or posting an error.
- The card contract is `docs/attention-store.md`; do not re-derive it.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code.
- No test may touch the real launchd domain, a real pushgateway, a real attention store, or the operator's `~/.config`; use `httptest` and stubs on `PATH`.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT modify `internal/config`, `internal/watch`, `internal/sync`, `internal/git`, `internal/cli`, `internal/lock`, or the concurrency/restart logic in `internal/supervise/supervise.go`; extend the runner closure and add the three new files.
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

Before finishing, walk the assigned criteria against the tests:
- after two successful cycles the stub pushgateway has received only `POST` requests carrying both `git_ai_sync_heartbeat_timestamp` and `git_ai_sync_last_success_timestamp`, and zero `PUT` (Acceptance Criterion 5);
- with `stall_threshold: 2` and an unreachable remote, exactly one card is posted on the second failed cycle with the required flags and payload, and one `POST …/close` is sent on the next success (Acceptance Criterion 6);
- a non-existent script logs exactly one startup warning naming the path, makes zero invocations, and the state file records `consecutive_failures: 3` (Acceptance Criterion 7);
- a 10 s stub is abandoned at 5 s with one WARNING naming the timeout, one invocation, and the next cycle still runs (Acceptance Criterion 8).

The state-file JSON shape (`last_metrics_push`, per-vault `consecutive_failures` / `last_success` / `last_error`) is the contract `status` reads in the lifecycle prompt; keep the field names and tags exactly as specified.

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
