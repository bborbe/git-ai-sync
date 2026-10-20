---
status: committing
summary: Made conflict resolution fail closed by refusing error/auth-failed Claude results and resolutions that drop markdown frontmatter, with tests covering each refusal and the real-repo seam
execution_id: git-ai-sync-auth-refusal-exec-025-resolver-refuses-failed-resolution
dark-factory-version: v0.198.7
created: "2026-10-20T06:29:17Z"
queued: "2026-10-20T06:29:17Z"
started: "2026-10-20T06:29:19Z"
completed: "2026-10-20T06:31:02Z"
---

# Refuse to write a conflicted file when the Claude call fails

<summary>
- The conflict resolver stops treating the Claude CLI's error output as a resolved file
- A failed authentication can no longer overwrite a conflicted file with an error string
- A markdown file that had frontmatter keeps it, or the resolution is refused
- The conflict stays unresolved, so the next sync retries it instead of committing the damage
- Every refusal names the file and the reason in the log
- Successful resolutions behave exactly as before
</summary>

<objective>
Make conflict resolution fail closed: `resolve_conflict_with_claude` raises `ConflictError` when the Claude call errors, fails to authenticate, or returns a resolution that drops a markdown file's frontmatter, so neither write site touches the conflicted file. Today the CLI's error text is written into the file and committed as a successful resolution.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `src/git_ai_sync/conflict_resolver.py` — `resolve_conflict_with_claude`, `resolve_all_conflicts`, `resolve_marker_flagged_files`.
Read `tests/test_conflict_resolver.py` — the `_mock_claude_client` helper, `TestResolveConflictWithClaude`, `TestResolveAllConflicts`.
Read `docs/dod.md` — the Definition of Done that dark-factory's validation prompt checks; `make precommit` is the command that satisfies its Build section.
Read `CHANGELOG.md` — the frozen preamble ends at `* PATCH version when you make backwards-compatible bug fixes.`; the highest released section is `## v0.13.0` and there is currently NO `## Unreleased` section.
</context>

<requirements>
1. In `src/git_ai_sync/conflict_resolver.py`, add `ResultMessage` to the existing `claude_code_sdk` import.

2. In `resolve_conflict_with_claude`, capture the final result message inside the existing `async for message in client.receive_response():` loop. Keep the current `AssistantMessage` / `TextBlock` accumulation and the `_parse_message_tolerant` wrapper untouched; add a branch that assigns the `ResultMessage` to a local.

3. After the loop, before the existing empty-response guard, add these refusals. Each raises `ConflictError` with a message naming the file and the reason:
   - the captured `ResultMessage` has `is_error` true — always name its `subtype` (for example `error_during_execution`) in the message, and include its `result` text when present. The container's Claude CLI error result omits `result` and carries the diagnostic in an `errors` array that `claude_code_sdk` 0.0.25's `ResultMessage` does not expose, so `subtype` is the only reason detail available on this path;
   - `"Failed to authenticate"` appears in the accumulated response text, or in the result message's own `result` text when that attribute is present. This branch is load-bearing, not defensive: reproducing the production defect against the real CLI leaves `Failed to authenticate. API Error: 401 Invalid bearer token` in the accumulated assistant text, which is exactly what gets written to the file today.
   Keep the existing `if not response_text: raise ConflictError("Claude returned empty response")` as the fallback.

4. After the existing code-fence stripping, add a frontmatter refusal: when `file_path` ends with `.md`, the incoming `content` starts with `---`, and the cleaned `resolved` does not start with `---`, raise `ConflictError` naming the file. Vault task and goal files always carry frontmatter, so a resolution that drops it is a failed resolution. Scope note: this covers body conflicts only — when the conflict itself sits in the frontmatter the incoming content starts with `<<<<<<<`, so the guard does not apply; do not widen it to strip conflict markers.

5. Leave `resolve_all_conflicts` and `resolve_marker_flagged_files` otherwise unchanged. `resolve_all_conflicts` already catches `ConflictError` and logs `Failed to resolve <file>: <reason>`; `resolve_marker_flagged_files` already re-raises. Do not add a second write path, a fallback write, or a retry.

6. Tests in `tests/test_conflict_resolver.py`, split by unit:
   - `TestResolveConflictWithClaude` — one case per refusal condition. Add a sibling helper `_mock_claude_client_yielding(messages: list[object])` rather than changing `_mock_claude_client` (three existing tests use it). Build a real `claude_code_sdk.ResultMessage(...)` with every required field (`subtype`, `duration_ms`, `duration_api_ms`, `is_error`, `num_turns`, `session_id`) instead of a `MagicMock(spec=ResultMessage)`, so `isinstance(message, ResultMessage)` is exercised against the SDK's actual dataclass and a future field rename breaks the test. Cases: an `is_error` result message with `subtype="error_during_execution"` (also assert the message carries the `result` text when the message sets one), an auth-failure response body as assistant text, and a markdown file whose content starts with `---` and then contains conflict markers (without markers `parse_conflict_markers` returns empty and the function returns early, never reaching the guard) resolved to text without `---`, plus the negative case — the same `---` file resolved to text that still starts with `---` returns normally, proving the guard does not over-trigger. Each refusal case asserts `pytest.raises(ConflictError)` with a message matching the reason. Also add one test feeding a raw `{"type": "result", "subtype": "error_during_execution", "is_error": true, ...}` payload through `_parse_message_tolerant` and asserting it returns a real `ResultMessage` with those fields — the production loop consumes parser output, so this closes the parser boundary.
   - `TestResolveAllConflicts` — one case against a real conflicted git repository built in `tmp_path` (`git init` plus the repo-local config used by `test_commit_with_marker_recovery_marker_path_unchanged` — `user.email`, `user.name`, `commit.gpgsign false`, `core.hooksPath` — then a commit on two branches and a `git merge` leaving one path unmerged; the container has no global git identity, so a bare `git commit` fails). Patch `git_ai_sync.conflict_resolver.ClaudeSDKClient` with the new error-result helper instead of patching `resolve_conflict_with_claude`, so the real resolver drives the refusal and the whole seam is exercised. Assert: the file's bytes are unchanged, `git diff --name-only --diff-filter=U` still lists the path, and `caplog` holds the `Failed to resolve <file>:` ERROR record.

7. In `CHANGELOG.md`, create `## Unreleased` directly above `## v0.13.0` and add exactly one bullet:
   `- fix: Refuse a conflict resolution whose Claude call errored or failed to authenticate, or that would drop a markdown file's frontmatter, so a failed resolution can no longer overwrite the conflicted file and get committed as resolved`

8. Before finishing, re-run the verification below and walk each requirement against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Do not modify existing tests in `tests/test_conflict_resolver.py` — add new tests and a new helper; the existing `TestResolveConflictWithClaude` and `TestResolveAllConflicts` tests must pass unchanged
- No new mypy errors (strict mode); type hints on all signatures, docstrings on public functions
- No `# type: ignore` or `noqa` without an explanation
- Follow the existing patterns in `conflict_resolver.py` — module-level `logger`, functions over classes
- Do not change the model default or the prompt text sent to Claude
- Leave the `ClaudeSDKClient` call's existing timeout behaviour unchanged — adding a timeout or cancellation is out of scope for this fix
</constraints>

<verification>
`make precommit` — must pass.
`uv run pytest tests/test_conflict_resolver.py -v` — the new refusal cases are collected and pass (a green `make precommit` alone does not prove they ran).
`awk '/^## /{sec=$0} /fix: Refuse a conflict resolution/{print sec}' CHANGELOG.md` — prints `## Unreleased` (a bare `grep` finds the bullet in a released section too).
</verification>
