---
status: completed
summary: Added a tolerant message-parser wrapper to git-ai-sync's conflict resolver so the SDK's informational rate_limit_event (and any unrecognized stream message type) is skipped instead of aborting resolution, with regression tests, doctor wiring, and a CHANGELOG entry
execution_id: git-ai-sync-rate-limit-exec-010-fix-rate-limit-event-parse
dark-factory-version: dev
created: "2026-09-03T14:05:56Z"
queued: "2026-09-03T14:05:56Z"
started: "2026-09-03T14:06:15Z"
completed: "2026-09-03T14:09:44Z"
---

<summary>
- `resolve_conflict_with_claude` aborts resolution when the Claude Code CLI emits an informational `rate_limit_event` message at session start
- `claude-code-sdk==0.0.25` `parse_message` raises `MessageParseError: Unknown message type: rate_limit_event` for any streamed `type` it does not recognize
- `MessageParseError` is a `ClaudeSDKError`, so the resolver's `except ClaudeSDKError` converts it to `ConflictError` and the whole file never resolves or stages — the vault stays wedged on conflicted files forever
- Fix: tolerate unrecognized / non-assistant stream messages in the resolver instead of letting the SDK abort
- Regression test pins a `rate_limit_event`-then-assistant stream resolving successfully
</summary>

<objective>
Make `resolve_conflict_with_claude` skip a `rate_limit_event` (or any unrecognized) stream message instead of aborting with `ConflictError`, so real conflict resolution completes and stages the file.
</objective>

<context>
Read CLAUDE.md for project conventions.

Read `src/git_ai_sync/conflict_resolver.py` — `resolve_conflict_with_claude` reads the response stream at the `async for message in client.receive_response():` loop and only accumulates `TextBlock` blocks from `AssistantMessage` messages. The failure is upstream of that loop: `claude_code_sdk` v0.0.25's `_internal/message_parser.py` `parse_message` raises `MessageParseError: Unknown message type: rate_limit_event` when a streamed message's top-level `type` is not one of the handled set (`user` / `assistant` / `system` / `result` / `stream_event`). The Claude Code CLI emits `{"type": "rate_limit_event", "rate_limit_info": {"status": "allowed", "isUsingOverage": false}, ...}` at session start under Max/OAuth — informational, not only when actually rate-limited (upstream anthropics/claude-agent-sdk-python#601, dup #689; PyPI latest is still 0.0.25, no released SDK fix).

`MessageParseError` subclasses `ClaudeSDKError`, so the resolver's `except ClaudeSDKError` (line ~129) turns it into `ConflictError(f"Claude API call failed: {e}")` and aborts. The fix belongs in git-ai-sync: make the resolver's parse boundary tolerant of unrecognized message types.

Approach (resolver-side tolerance — do NOT pin a fork or patch the SDK's released package): install a thin wrapper over `claude_code_sdk._internal.message_parser.parse_message` that returns `None` for unknown message types instead of raising, so `receive_messages` yields `None` for those events and the resolver loop (which only acts on `AssistantMessage`) naturally skips them. Apply the wrapper in `conflict_resolver.py` (module-level, guarded so it is idempotent) before `resolve_conflict_with_claude` runs. Note: `cmd_doctor` in `src/git_ai_sync/__main__.py` does not import `conflict_resolver`, so a standalone `git-ai-sync doctor` never installs the wrapper — either add `from git_ai_sync import conflict_resolver` to `cmd_doctor` (or install the wrapper in `test_session`) so the doctor session test is covered too, or drop the doctor claim from scope.

Read `tests/test_conflict_resolver.py` — existing mock pattern (`_mock_claude_client`, `_AsyncIter`, `TestResolveConflictWithClaude`) for how the resolver's stream is tested.

Read `CHANGELOG.md` — add a `fix:` bullet under `## Unreleased` (create the section directly above the highest `## vX.Y.Z` heading, currently `## v0.10.0`, if absent).
</context>

<requirements>
1. **Reproduce first (failing test):** add a regression test to `tests/test_conflict_resolver.py` that asserts the raw `rate_limit_event` payload no longer aborts the parse boundary:
   - `test_parse_message_tolerates_rate_limit_event` — call the wrapped parse function (exposed from `git_ai_sync.conflict_resolver`) with `{"type": "rate_limit_event", "uuid": "...", "rate_limit_info": {"status": "allowed", "isUsingOverage": false}}`; assert it returns `None` (skipped) rather than raising. Before the implementation this test fails (the SDK's `parse_message` raises `MessageParseError`).
2. **Implement the tolerance in `src/git_ai_sync/conflict_resolver.py`:**
   - Import `MessageParseError` from `claude_code_sdk._internal.message_parser` (it is not exported from the public `claude_code_sdk` package) and import `claude_code_sdk._internal.message_parser` as `_message_parser`. In the wrapper, return `None` only when `data.get("type")` is not in the SDK's handled set (`user` / `assistant` / `system` / `result` / `stream_event`); otherwise delegate to `parse_message` and let genuine parse errors (missing fields, malformed payloads) propagate to the resolver's existing `except ClaudeSDKError` → `ConflictError`, preserving today's abort behavior for genuine errors. Do NOT catch the broader `ClaudeSDKError`.
   - Install a module-level wrapper over `_message_parser.parse_message`: on `MessageParseError` (unknown message type), return `None` instead of raising; otherwise delegate to the original. Make installation idempotent (guard against double-wrap). Export the wrapper (e.g. `_parse_message_tolerant`) so tests can call it directly.
   - Ensure `resolve_conflict_with_claude` needs no change to its receive loop — a `None` yielded for a skipped message is ignored by the existing `isinstance(message, AssistantMessage)` check. Verify the loop continues past the skipped event and returns the assistant text.
3. **Add a resolution-path regression test:** `test_resolve_conflict_with_claude_skips_rate_limit_event` — using the existing `_mock_claude_client` pattern, yield a non-assistant `rate_limit_event`-shaped mock message first, then an `AssistantMessage`; assert `resolve_conflict_with_claude` returns the assistant text (resolution completes, no `ConflictError`). Because the mock replaces the SDK client, it cannot prove the fix — add `test_wrapper_installed_on_sdk_module`: after `git_ai_sync.conflict_resolver` is imported, assert `claude_code_sdk._internal.message_parser.parse_message is git_ai_sync.conflict_resolver._parse_message_tolerant` (this is the attribute `ClaudeSDKClient.receive_messages` resolves at each call). Prefer a real-`ClaudeSDKClient` integration test with a scripted transport emitting a `rate_limit_event` line if one is cheap to add.
4. **In `CHANGELOG.md`, under `## Unreleased`** (create if absent, directly above `## v0.10.0`), add exactly one bullet:
   - `- fix: git-ai-sync Claude conflict resolver skips the SDK's informational rate_limit_event stream message (and any unrecognized message type) instead of aborting resolution with a parse error, so conflict resolution completes and stages the file (claude-code-sdk 0.0.25 has no released fix)`
5. Self-check: re-run the `<verification>` block and walk each requirement against the change before finishing.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Do NOT pin a fork or patch the installed `claude-code-sdk` package itself — tolerance lives in git-ai-sync's resolver only
- Python 3.14, `make precommit` must stay green (ruff strict, mypy strict, pytest)
- Type hints on all new functions, docstrings on public functions, no `print()`
- Do not change anything else about `conflict_resolver.py` (prompt text, model default, read/write/stage flow, `ConflictError` semantics for genuine SDK errors all stay as-is)
- Do not alter existing tests in `tests/test_conflict_resolver.py` — only add
</constraints>

<verification>
Run `make precommit` — must exit 0 (format + lint + typecheck + full test suite).

Run `uv run pytest tests/test_conflict_resolver.py -v` — all conflict-resolver tests pass, including `test_parse_message_tolerates_rate_limit_event` and `test_resolve_conflict_with_claude_skips_rate_limit_event`.

Run `grep -n 'rate_limit_event\|MessageParseError' src/git_ai_sync/conflict_resolver.py` — returns ≥1 line (the tolerance wrapper references it); `grep -n 'fix: git-ai-sync Claude conflict resolver skips' CHANGELOG.md` — returns ≥1 line under `## Unreleased`.
</verification>
