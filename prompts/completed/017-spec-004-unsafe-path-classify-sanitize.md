---
status: completed
spec: [004-bug-unsafe-path-refusal-skips-pull]
summary: Added unsafe-path refusal primitives to git_operations.py — UnsafePathRefusalError, is_unsafe_path_refusal(), get_blocked_paths(), and sanitize_path() — with module-level AC tests and an Unreleased changelog entry
execution_id: git-ai-sync-exec-017-spec-004-unsafe-path-classify-sanitize
dark-factory-version: v0.196.0
created: "2026-10-11T14:30:00Z"
queued: "2026-10-11T12:54:21Z"
started: "2026-10-11T12:54:54Z"
completed: "2026-10-11T12:56:58Z"
---

<summary>
- A commit refused because a staged path is not safe on Windows is now classified as its own error type instead of a generic git failure
- The offending paths the hook named are read back out of the hook's own refusal message, so recovery knows exactly which files to fix
- A fixed, tested rule set turns any unsafe filename into a Windows-safe one — forbidden characters become dashes, edge dashes and trailing dots or spaces are stripped, reserved device names are suffixed, and an empty result becomes `untitled`
- When a sanitized name would collide with a file that already exists, a numeric suffix is added instead of overwriting it
- The new error type stays a subclass of the existing git error, so every current error handler keeps working unchanged
- Marker refusals (the sibling bug fixed earlier) are never misread as unsafe-path refusals — the two classifications stay separate
- Nothing about pull, push, or the pre-commit hook itself changes in this step
</summary>

<objective>
Add the unsafe-path refusal primitives to `src/git_ai_sync/git_operations.py`: an `UnsafePathRefusalError` exception, an `is_unsafe_path_refusal` classifier, a `get_blocked_paths` extractor, and a `sanitize_path` renamer — so the recovery loop (prompt 2) and the never-skip-the-pull wiring (prompt 3) can build on them.
</objective>

<context>
Read CLAUDE.md for project conventions (Python 3.14, uv, mypy strict, ruff line-length 100, pytest + pytest-asyncio `asyncio_mode = "auto"`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/tdd-guide.md` for the red-green workflow (write the failing tests, then implement, then run the full suite).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention (insert after the frozen preamble, directly above the highest `## vX.Y.Z`; flat list; conventional prefixes).
Read `docs/dod.md` — the Definition of Done that `make precommit` validates.
Read `src/git_ai_sync/git_operations.py` — `GitError`, `MarkerRefusalError` (the sibling class to mirror), `commit()` (the function to extend), `is_marker_refusal()` (the classifier shape to mirror), `get_marker_flagged_files()` (the output-parsing shape to mirror), `get_changed_files_short()`, and `stage_file()`.
Read `tests/test_git_operations.py` — `_mock_result()` helper, the `REPO` constant, `TestCommit`, and the module-level `test_is_marker_refusal` / `test_not_marker_refusal` / `test_get_marker_flagged_files` / `test_commit_raises_marker_refusal` / `test_marker_refusal_error_is_git_error` (the exact style the new tests must follow).
Read `CHANGELOG.md` — the frozen preamble ends at the `* PATCH version when you make backwards-compatible bug fixes.` line (line 9); the highest released section is `## v0.12.1`.
</context>

<requirements>
1. In `src/git_ai_sync/git_operations.py`, add a new exception class directly after the existing `MarkerRefusalError` class:

   ```python
   class UnsafePathRefusalError(GitError):
       """Commit refused by the pre-commit hook because a staged path is not safe on Windows."""

       pass
   ```

2. Add `is_unsafe_path_refusal(message: str) -> bool` immediately after `is_marker_refusal`:

   ```python
   def is_unsafe_path_refusal(message: str) -> bool:
       """Check whether an error message matches the pre-commit hook's unsafe-path refusal signature.

       Args:
           message: Error message text (typically git stderr)

       Returns:
           True if the message contains "staged path is not safe on Windows" (case-insensitive)
       """
       return "staged path is not safe on windows" in message.lower()
   ```

   The two classes MUST stay disjoint: `is_marker_refusal("✋ Blocked: staged path is not safe on Windows")` is False, and `is_unsafe_path_refusal("✋ Refusing commit: staged changes contain git merge conflict markers")` is False.

3. Add `get_blocked_paths(message: str) -> list[str]`. The hook prints the offending paths as indented lines of the exact form `     <path>  —  <reason>` (five leading spaces, two spaces, an em dash `—` U+2014, two spaces, then the reason), between the `✋ Blocked: staged path is not safe on Windows` banner and the trailing explanatory paragraph that begins `   Windows rejects these characters in a path component:`. Extract the `<path>` field from each such line and return them in order; return `[]` for any message with no such block. Implementation:

   ```python
   _BLOCKED_PATH_RE = re.compile(r"^[ \t]+(?P<path>.+?)\s{2}—\s{2}(?P<reason>.+)$", re.MULTILINE)


   def get_blocked_paths(message: str) -> list[str]:
       """Extract the offending paths from the pre-commit hook's unsafe-path refusal block.

       Args:
           message: Error message text (typically git stderr)

       Returns:
           List of offending paths named in the hook's indented block, or an empty list
       """
       return [match.group("path") for match in _BLOCKED_PATH_RE.finditer(message)]
   ```

   Note the pattern is anchored with `re.MULTILINE` and requires the two-space-em-dash-two-space separator, so the trailing prose lines (`   Windows rejects these characters in a path component: : ? * " < > |` and the `notesmd-cli move` example) never match — they have no `  —  ` separator.

4. Add `sanitize_path(path: str, existing: set[str] | None = None) -> str` — the fixed rule set from the spec's Desired Behavior 3, applied per path component. Rules, in order, for each component:
   - replace each of `:`, `?`, `*`, `"`, `<`, `>`, `|` with `-`
   - collapse runs of `-` to a single `-`
   - trim leading and trailing `-` from the stem (the text before the last `.`, or the whole component when there is no `.`)
   - strip a trailing dot or trailing space
   - if the component's stem (text before the last `.`, or the whole component when there is no `.`) matches a reserved Windows device name case-insensitively — `CON`, `PRN`, `AUX`, `NUL`, `COM1`-`COM9`, `LPT1`-`LPT9` — append `_` to the stem
   - if the component is now empty, use `untitled`
   - split on `/` and sanitize each component, then rejoin with `/` (so `dir/a?.md` → `dir/a-.md` → `dir/a.md`)
   - when `existing` is provided and the joined result is in `existing`, append `-2`, `-3`, … to the final component's stem until the joined result is not in `existing` (so `sanitize_path("a?.md", {"a.md"})` → `a-2.md`). When `existing` is None or empty, skip this step.

   Sketch (keep it small; mypy strict — every local needs a type):

   ```python
   _UNSAFE_CHARS = ":?*\"<>|"
   _RESERVED_DEVICES = frozenset(
       {"CON", "PRN", "AUX", "NUL", *(f"COM{i}" for i in range(1, 10)), *(f"LPT{i}" for i in range(1, 10))}
   )


   def _sanitize_component(component: str) -> str:
       cleaned = "".join("-" if ch in _UNSAFE_CHARS else ch for ch in component)
       while "--" in cleaned:
           cleaned = cleaned.replace("--", "-")
       cleaned = cleaned.rstrip(". ")
       stem, dot, ext = cleaned.rpartition(".")
       if not dot:
           stem, dot, ext = cleaned, "", ""
       stem = stem.strip("-")
       if not stem:
           return f"untitled{dot}{ext}"
       if stem.upper() in _RESERVED_DEVICES:
           stem = f"{stem}_"
       return f"{stem}{dot}{ext}"


   def sanitize_path(path: str, existing: set[str] | None = None) -> str:
       """Rename an unsafe path to a Windows-safe one, disambiguating a collision."""
       sanitized = "/".join(_sanitize_component(part) for part in path.split("/"))
       if not existing or sanitized not in existing:
           return sanitized
       stem, dot, ext = sanitized.rpartition(".")
       stem, dot, ext = (sanitized, "", "") if not dot else (stem, dot, ext)
       counter = 2
       candidate = f"{stem}-{counter}{dot}{ext}"
       while candidate in existing:
           counter += 1
           candidate = f"{stem}-{counter}{dot}{ext}"
       return candidate
   ```

   If your implementation differs, it MUST still satisfy every row of the AC 3 table below.

5. Modify `commit()` so it classifies an unsafe-path refusal BEFORE the marker check (classification order is a correctness concern, not style). OLD:

   ```python
       if result.returncode != 0:
           if is_marker_refusal(result.stderr):
               raise MarkerRefusalError(f"Failed to commit: {result.stderr}")
           raise GitError(f"Failed to commit: {result.stderr}")
   ```

   NEW:

   ```python
       if result.returncode != 0:
           if is_unsafe_path_refusal(result.stderr):
               raise UnsafePathRefusalError(f"Failed to commit: {result.stderr}")
           if is_marker_refusal(result.stderr):
               raise MarkerRefusalError(f"Failed to commit: {result.stderr}")
           raise GitError(f"Failed to commit: {result.stderr}")
   ```

   Update `commit()`'s `Raises:` docstring to list `UnsafePathRefusalError` before `MarkerRefusalError`, then `GitError`.

6. Add the needed imports at the top of `git_operations.py` — `import re` (alongside the existing `import subprocess`). Keep the ruff import order valid.

7. Add module-level test functions to `tests/test_git_operations.py` (module-level, NOT inside a class — the node ids must match the spec's acceptance-criteria evidence paths exactly). Add the new names (`UnsafePathRefusalError`, `get_blocked_paths`, `is_unsafe_path_refusal`, `sanitize_path`) to the import block from `git_ai_sync.git_operations`.

   a. `test_unsafe_path_refusal` — classification. Use this verbatim hook block as the fixture (it is the hook's real stderr shape):

      ```python
      BLOCKED_STDERR = (
          "\n"
          "✋ Blocked: staged path is not safe on Windows\n"
          "\n"
          "     unsafe?.md  —  contains one of  : ? * \" < > |\n"
          "\n"
          "   Windows rejects these characters in a path component: : ? * \" < > |\n"
          "   It also rejects a component ending in a dot or a space, and a name\n"
          "   whose stem is a reserved device: CON PRN AUX NUL COM1-9 LPT1-9.\n"
      )
      ```

      Assert `is_unsafe_path_refusal(BLOCKED_STDERR) is True`; assert `is_unsafe_path_refusal("✋ Blocked: STAGED PATH IS NOT SAFE ON WINDOWS") is True` (case-insensitive); assert `is_unsafe_path_refusal("Failed to commit: index.lock exists") is False`; and assert the disjointness both ways — `is_unsafe_path_refusal("✋ Refusing commit: staged changes contain git merge conflict markers") is False` and `is_marker_refusal(BLOCKED_STDERR) is False`.

   b. `test_blocked_paths` — extraction. Assert `get_blocked_paths(BLOCKED_STDERR) == ["unsafe?.md"]`; assert `get_blocked_paths(TWO_PATH_STDERR) == ["unsafe?.md", "bad:name.md"]`, where `TWO_PATH_STDERR` is `BLOCKED_STDERR` with a second indented line `"     bad:name.md  —  contains one of  : ? * \" < > |\n"` inserted directly after the first; assert `get_blocked_paths("Failed to commit: index.lock exists") == []`; assert `get_blocked_paths("") == []`. Add a guard that the trailing prose is NOT captured: assert no element of the result contains `"Windows rejects"` or `"notesmd-cli"`.

   c. `test_sanitize_path` — one case per rule, exactly the spec's AC 3 table. Assert:
      - `sanitize_path("a?b") == "a-b"`
      - `sanitize_path("a???b") == "a-b"` (run collapsed)
      - `sanitize_path("-a-") == "a"` (edges trimmed)
      - `sanitize_path("a.") == "a"` and `sanitize_path("a ") == "a"` (trailing dot / space)
      - `sanitize_path("CON.md") == "CON_.md"` and `sanitize_path("con.md") == "con_.md"` (reserved stem, case-insensitive)
      - `sanitize_path("???.md") == "untitled.md"` (component empty after sanitizing)
      - `sanitize_path("a?.md", {"a.md"}) == "a-2.md"` (collision)
      - one multi-component case: `sanitize_path("notes/a?b.md") == "notes/a-b.md"` (per-component application)

8. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk AC 1, AC 2, and AC 3 from the spec against the change.
</requirements>

<constraints>
- Do NOT weaken, bypass, or extend the global pre-commit hook — no `--no-verify`, no `core.hooksPath` override, no hook edit; the fix belongs in git-ai-sync's recovery
- `UnsafePathRefusalError` MUST subclass `GitError` (so every existing `except git_operations.GitError` handler keeps working) and MUST be disjoint from `MarkerRefusalError` — the unsafe-path check MUST come before the marker check in `commit()`
- Preserve the existing error-handling shape: `subprocess.run(..., check=False)` with a typed `GitError` subclass raised before any broad `except`
- `sanitize_path` operates on path components only — never touch file contents
- `continue_merge()` also invokes `git commit` and therefore hits the same pre-commit hook; its refusal classification is deliberately out of scope for this spec (it runs only inside an in-progress merge) — do not change it here
- Type hints on all new functions (mypy strict); pytest conventions only (`asyncio_mode = "auto"`); no `unittest`
- All new tests are module-level functions (node ids must match the spec's AC evidence paths: `test_unsafe_path_refusal`, `test_blocked_paths`, `test_sanitize_path`)
- Update `CHANGELOG.md`: insert a `## Unreleased` section between the frozen preamble (ends at line 9) and `## v0.12.1`, with a single `fix:` bullet:

  ```
  ## Unreleased

  - fix: Classify a pre-commit-hook refusal over a staged path that is not safe on Windows — add `UnsafePathRefusalError`, `is_unsafe_path_refusal()`, `get_blocked_paths()`, and `sanitize_path()` so the offending paths can be renamed to Windows-safe names (recovery wiring lands in a later prompt)
  ```

  Do NOT modify anything above the `# Changelog` title, the preamble lines, or the `## v0.12.1` section.
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the acceptance-criteria tests pass:
`uv run pytest tests/test_git_operations.py -k 'unsafe_path_refusal or blocked_paths or sanitize_path' -v`
Confirm the marker-refusal tests are unchanged and still pass:
`uv run pytest tests/test_git_operations.py -k 'marker' -v`
Confirm the CHANGELOG structure: `grep -n "^## " CHANGELOG.md` — must show `## Unreleased` directly above `## v0.12.1`, with nothing inserted above the `# Changelog` title or inside the preamble.
</verification>
