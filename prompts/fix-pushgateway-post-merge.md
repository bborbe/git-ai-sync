---
status: draft
created: "2026-09-02"
---

<summary>
- Metric pushes to the pushgateway use HTTP `POST` instead of `PUT`
- `PUT` replaces the entire grouping-key group, so the heartbeat push erased the previously-pushed `last_success` metric every cycle
- Result: `git_ai_sync_last_success_timestamp` survived at most ~30s on the gateway, so the "last success stale" alert could never fire — the exact silent-wedge blindness the feature exists to prevent
- `POST` merges into the group, so heartbeat and last-success coexist on the gateway
- Regression test pins `POST` as the request method
</summary>

<objective>
Fix the metric-push HTTP method from PUT to POST so both git-ai-sync metrics coexist on the pushgateway and the age-of-last-success alert can actually fire when a vault wedges.
</objective>

<context>
Read CLAUDE.md for project conventions.

Read `src/git_ai_sync/metrics.py` — `_push_metric` builds a `urllib.request.Request` with `method="PUT"` at line 79. This is the bug: the Prometheus pushgateway treats `PUT /metrics/job/<job>/instance/<instance>` as *replace the whole group* (delete all other series under that grouping key), while `POST` merges (updates the named series, keeps the others). Verified live against the prod gateway 2026-09-02: after a heartbeat PUT, the previously-POSTed last_success series for the same instance was gone; with POST both coexist.

Read `tests/test_metrics.py` — `test_push_heartbeat_url_method_and_auth` asserts `req.method == "PUT"` at line 38 and must be updated to `POST`. Add a companion regression test that pins the method on `push_last_success` too.

Read `CHANGELOG.md` — add a `fix:` bullet under `## Unreleased` (create the section directly above the highest `## vX.Y.Z` heading, which is currently `## v0.9.0`, if it does not exist).
</context>

<requirements>
1. In `src/git_ai_sync/metrics.py`, change the request method from `"PUT"` to `"POST"`:
   - Old: `request = Request(url, data=body.encode("utf-8"), headers=headers, method="PUT")`
   - New: `request = Request(url, data=body.encode("utf-8"), headers=headers, method="POST")`
   - Add a one-line comment above it explaining why POST and not PUT: PUT replaces the whole grouping-key group and would erase the sibling metric; POST merges so heartbeat and last-success coexist.
2. In `tests/test_metrics.py`:
   - Update `test_push_heartbeat_url_method_and_auth`: change `assert req.method == "PUT"` to `assert req.method == "POST"`.
   - Add `test_push_last_success_uses_post` — same shape as `test_push_heartbeat_url_method_and_auth` but calling `push_last_success` (patch `git_ai_sync.metrics.urlopen` and `git_ai_sync.metrics.time.time`), asserting `req.method == "POST"`, `full_url` contains `instance/` and the last-success metric name appears in `req.data`.
3. In `CHANGELOG.md`, under `## Unreleased` (create if absent, directly above `## v0.9.0`), add exactly one bullet:
   - `- fix: git-ai-sync pushgateway metrics use POST instead of PUT so heartbeat and last-success timestamps coexist on the gateway (PUT replaced the whole group and erased last-success every cycle, which would have kept the sync-stall alert from ever firing)`
4. Self-check: re-run the `<verification>` block and walk each requirement against the change before finishing.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Type hints on all function signatures, docstrings on public functions, no `print()`
- Do not change anything else about `metrics.py` (URL shape, grouping key, body format, timeout, auth, error handling all stay as-is)
- Do not alter the existing `test_push_failure_returns_false_and_logs_warning`, timeout, unreachable, or arbitrary-error tests
</constraints>

<verification>
Run `make precommit` — must exit 0 (format + lint + typecheck + full test suite).

Run `uv run pytest tests/test_metrics.py -v` — all metric tests pass, including the updated PUT→POST assertion and the new last-success POST regression test.

Run `grep -n 'method="POST"' src/git_ai_sync/metrics.py` — returns line ≥1; `grep -n 'method="PUT"' src/git_ai_sync/metrics.py` — returns 0 lines.
</verification>
