---
status: completed
spec: [009-port-conflict-resolution]
summary: Added the fail-closed internal/conflict resolver (marker parsing, no-marker short circuit, JSON Claude CLI invocation with all fail-closed checks, fence stripping, frontmatter guard, atomic write + staging, marker-flagged resolver) and the exported internal/git helpers, with tests and a CHANGELOG entry
execution_id: 078aba9b-094c-5f23-8b62-8bf286b1307c-exec-036-spec-009-conflict-core
dark-factory-version: dev
created: "2026-11-09T23:20:11Z"
queued: "2026-11-09T23:33:53Z"
started: "2026-11-09T23:33:55Z"
completed: "2026-11-09T23:36:48Z"
branch: feat/port-conflict-resolution
---

# Implement the fail-closed conflict resolver core

<summary>
- The binary gains a fail-closed conflict resolver that asks the Claude CLI for a merged file and writes it only after every check has passed.
- A non-zero CLI exit, an empty answer, an authentication failure, or an error envelope leaves the conflicted file byte-identical.
- A markdown note that began with frontmatter is never overwritten by an answer that lost it.
- A file with no conflict-marker blocks is staged unchanged and the model is never called for it.
- A fenced answer has its fence removed before it is compared or written.
- Files flagged for conflict markers can be resolved and re-staged.
- The git package exposes the conflict-state, conflicted-file, single-file staging, and continuation helpers the resolver needs.
- No test calls a real model, reads real credentials, or depends on HOME; a stub `claude` executable on PATH is the test input.
- The Python source is untouched and no `.github/workflows/` file is touched.
</summary>

<objective>
Implement `internal/conflict` — conflict-marker parsing, the no-marker short circuit, the `claude -p --output-format json` invocation with every fail-closed check, fence stripping, the markdown frontmatter guard, atomic single-file write plus staging, and the marker-flagged resolver — and add the exported `internal/git` helpers it needs (`IsInMerge`, `IsInRebase`, `ConflictedFiles`, `StageFile`, `ContinueMerge`, `ContinueRebase`). **Why:** a wrong file here corrupts a vault, not a log line, so every check in the Python reference must survive the port.
</objective>

<context>
Read the approved spec at `specs/in-progress/009-port-conflict-resolution.md` — Problem, Goal, Non-goals, Assumptions, Acceptance Criteria 2-10, Desired Behavior 3-9, Constraints, Failure Modes, and Security are the contract for this prompt. The spec's Verification section is also binding: the Go gate is `make precommit-go`, not `make precommit`.

Read `src/git_ai_sync/conflict_resolver.py` for the exact reference behaviour:
- `parse_conflict_markers` — the regex and the empty-list short circuit at lines 139-141.
- `resolve_conflict_with_claude` — prompt text, `is_error`, `"Failed to authenticate"`, empty-response, fence-strip, and frontmatter checks, in that order.
- `resolve_all_conflicts` and `resolve_marker_flagged_files` — the per-file write-then-stage flow and the marker-flagged flow.

Read `internal/git/git.go` — `runGit`, `FindRepo`, `Push`, and the unexported `inConflictState` whose stat probes the new exported helpers must mirror. Read `internal/sync/sync_test.go` for the local-repository fixture pattern (`newFixture`, `configure`, `runGit`, `writeFile`) and copy it, including pinning `core.hooksPath` to the fixture's own hooks directory.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors` (`Wrapf`, `Wrap`, `Errorf`); sentinel errors use stdlib `errors` aliased as `stderrors`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — use stdlib `testing` (not Ginkgo/Gomega), as this repo does.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every external command takes the caller's `ctx`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the `## Unreleased` section goes after the preamble block and before the newest version heading.

The module path is `github.com/bborbe/git-ai-sync`. The dependency direction is one-way: `internal/conflict` may import `internal/git`; `internal/git` must never import `internal/conflict`.

<!-- OPEN QUESTION for the reviewer: the spec's "streamed text" half of the auth check (conflict_resolver.py:198-200) has no analogue in a one-shot `claude -p --output-format json` invocation, which returns only the final envelope. This port checks `envelope.result`; a streamed auth error is expected to surface in `result` or as `is_error`. Confirm this is the intended reading. -->
<!-- OPEN QUESTION for the reviewer: Python's `is_error` message appends the envelope's result text. This port names only the `subtype`, so a `Failed to authenticate` string that arrives inside an `is_error` envelope is never logged as content (Security: matched as an indicator, never logged as content). Acceptance Criterion 6 only requires the subtype. Confirm the divergence is acceptable. -->
</context>

<requirements>
1. Add exactly these exported helpers to `internal/git/git.go`. Keep the existing unexported `inConflictState` and its behaviour unchanged; the new helpers are accessors over the same probes, not a new detector.

   ```go
   // IsInMerge reports whether repo is in a merge state (.git/MERGE_HEAD exists).
   func IsInMerge(repo string) bool

   // IsInRebase reports whether repo is in a rebase state
   // (.git/rebase-merge or .git/rebase-apply exists).
   func IsInRebase(repo string) bool

   // ConflictedFiles returns the paths with unresolved conflicts
   // (git diff --name-only --diff-filter=U), relative to repo.
   func ConflictedFiles(ctx context.Context, repo string) ([]string, error)

   // StageFile stages one path (git add -- <path>).
   func StageFile(ctx context.Context, repo string, path string) error

   // ContinueMerge completes a merge after its conflicts are resolved
   // (git commit --no-edit).
   func ContinueMerge(ctx context.Context, repo string) error

   // ContinueRebase continues a rebase after its conflicts are resolved
   // (git rebase --continue).
   func ContinueRebase(ctx context.Context, repo string) error
   ```

   Implementation rules:
   - `IsInMerge` / `IsInRebase` take no `ctx` and return only `bool`: `os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))` and the two rebase directories; a stat error means false.
   - `ConflictedFiles` runs `runGit(ctx, repo, "diff", "--name-only", "--diff-filter=U")`, splits on `\n`, trims each line, and drops empty lines. Wrap a command error with `errors.Wrapf(ctx, err, "list conflicted files")`.
   - `StageFile` runs `runGit(ctx, repo, "add", "--", path)`; the `--` keeps a path that starts with `-` from being read as a flag. Wrap a command error with `errors.Wrapf(ctx, err, "stage %s", path)`.
   - `ContinueMerge` runs `runGit(ctx, repo, "commit", "--no-edit")`. `ContinueRebase` runs `runGit(ctx, repo, "rebase", "--continue")`. Wrap command errors with `errors.Wrapf` naming the operation. Do NOT use `git merge --continue`; the reference uses `git commit --no-edit`.
   - `ContinueMerge` / `ContinueRebase` are exercised end-to-end by prompt 2's merge and rebase tests. Do not build a separate conflict fixture for them here.

2. Create `internal/conflict/conflict.go` with exactly this public surface and the unexported helpers shown:

   ```go
   // Package conflict resolves git merge and rebase conflicts through the Claude CLI.
   package conflict

   // ResolveFile reads one conflicted file, resolves it through the Claude CLI when
   // its content contains conflict-marker blocks, writes the resolved content
   // atomically, and stages it. A failed resolution leaves the file byte-identical.
   // A file whose content parses to zero marker blocks is staged unchanged and the
   // model is not invoked.
   func ResolveFile(ctx context.Context, repo string, path string) error

   // ResolveMarkerFlaggedFiles resolves each path and re-stages it. It stops at the
   // first failure and returns an error naming the path.
   func ResolveMarkerFlaggedFiles(ctx context.Context, repo string, paths []string) error
   ```

   Implementation rules:
   - `ResolveFile`:
     1. `full := filepath.Join(repo, path)`; read the file with `os.ReadFile`. Wrap a read error with `errors.Wrapf(ctx, err, "read conflicted file %s", path)`.
     2. If `len(parseConflictMarkers(string(original))) == 0`, call `git.StageFile(ctx, repo, path)` and return; do not write and do not invoke the model. This matches `conflict_resolver.py:139-141`.
     3. Otherwise call `resolveContent(ctx, path, original)`. If it returns an error, return `errors.Wrapf(ctx, err, "resolve %s", path)` and do not touch the file.
     4. Determine the file mode with `os.Stat(full)` (default `0o644` when the stat fails), then write with `writeFileAtomic(ctx, full, resolved, mode)` and wrap a write error with `errors.Wrapf(ctx, err, "write resolved file %s", path)`.
     5. Stage with `git.StageFile(ctx, repo, path)` and wrap a staging error with `errors.Wrapf(ctx, err, "stage resolved file %s", path)`. A staging failure after a successful write is a failed file (the worktree keeps the resolved content).
   - `resolveContent(ctx, path string, original []byte) ([]byte, error)`:
     1. Build the prompt (requirement 3) and run `exec.CommandContext(ctx, "claude", "-p", "--output-format", "json")` with the prompt on **stdin** (`cmd.Stdin = strings.NewReader(prompt)`). Capture stderr into a `bytes.Buffer` (`cmd.Stderr = &stderr`). Call `cmd.Output()` synchronously — no goroutine may outlive the call.
     2. If the command fails (non-zero exit), return `errors.Wrapf(ctx, err, "claude CLI failed for %s: %s", path, strings.TrimSpace(stderr.String()))`.
     3. If `len(bytes.TrimSpace(out)) == 0`, return `errors.Errorf(ctx, "claude returned empty response for %s", path)`. This is the missing-envelope case; it must not be treated as an empty answer that gets written.
     4. `json.Unmarshal` stdout into:
        ```go
        type envelope struct {
            Type    string `json:"type"`
            IsError bool   `json:"is_error"`
            Subtype string `json:"subtype"`
            Result  string `json:"result"`
        }
        ```
        A parse failure returns `errors.Wrapf(ctx, err, "parse claude response for %s", path)` — a body that is not valid JSON is a failed resolution, not a text answer.
     5. If `env.IsError`, return `errors.Errorf(ctx, "claude failed to resolve %s (subtype: %s)", path, env.Subtype)`. The subtype must appear in the message (Acceptance Criterion 6 expects `error_during_execution`). Do not append `env.Result`.
     6. If `strings.Contains(env.Result, "Failed to authenticate")`, return `errors.Errorf(ctx, "claude failed to authenticate while resolving %s", path)`. Never log the matched string as content.
     7. If `strings.TrimSpace(env.Result) == ""`, return `errors.Errorf(ctx, "claude returned empty response for %s", path)`. This is the incident class: a valid envelope with an empty `result` must not become a zero-byte file.
     8. `resolved := stripCodeFence(strings.TrimSpace(env.Result))`.
     9. Frontmatter guard, after fence stripping: if `strings.HasSuffix(path, ".md")` and `bytes.HasPrefix(original, []byte("---"))` and `!strings.HasPrefix(resolved, "---")`, return `errors.Errorf(ctx, "resolution of %s dropped its markdown frontmatter; refusing to write", path)`.
     10. Return `[]byte(resolved), nil`.
   - `stripCodeFence(s string) string`: if `s` does not start with three backticks, return it unchanged. Otherwise split on `\n`, drop the first line when it starts with three backticks, drop the last line when it starts with three backticks, and join with `\n`. This mirrors `conflict_resolver.py:207-215`; the guard runs on the stripped result, so a fenced answer with intact frontmatter is not mistaken for a frontmatter loss.
   - `parseConflictMarkers(content string) []conflictBlock` compiles this pattern with `regexp.MustCompile` and calls `FindAllStringSubmatch`:

     ```go
     regexp.MustCompile(`(?s)<{7} .*?\n(.*?)\n={7}\n(.*?)\n>{7} .*?\n`)
     ```

     Define `type conflictBlock struct { ours, theirs string }`; populate it from the two capture groups. This mirrors `parse_conflict_markers`.
   - `writeFileAtomic(ctx context.Context, path string, content []byte, mode os.FileMode) error`: create a temp file in `filepath.Dir(path)` with `os.CreateTemp(dir, ".conflict-resolve-*")`; write the content; `Chmod(mode)`; `Sync()`; `Close()`; `os.Rename(tmp, path)`. Defer `os.Remove(tmpName)` so a failure leaves no temp file. Wrap each failure with `errors.Wrapf(ctx, ...)`. Never write the destination before every check has passed and never write a partial result.
   - `ResolveMarkerFlaggedFiles` loops the paths, calls `ResolveFile`, and on the first error returns `errors.Wrapf(ctx, err, "failed to resolve marker-flagged file %s", path)`. It ships with **no production caller** in this change: do not wire it into `internal/git.Commit`, `internal/sync`, or the CLI. It exists because the task's success criterion requires it and is exercised only by its own test.

3. Build the resolution prompt with **parity** to `conflict_resolver.py:147-170` — no improvements. Because the text contains a three-backtick fence around the file content, build it with `fmt.Sprintf` using a double-quoted string and `\n` escapes (or a `strings.Builder`), never a backtick raw literal. The rendered prompt is:

   ````text
   You are resolving a git merge conflict in a file.

   File: <path>
   Type: <filepath.Ext(path), or "unknown" when empty>

   **Resolution Strategy:**
   - Preserve ALL meaningful changes from both sides when possible
   - For timestamps/dates: prefer the more recent one
   - For additions: include both additions
   - For contradictory edits: use judgment based on context
   - Preserve file structure (frontmatter, formatting, headers)
   - Never remove content unless it's clearly a deletion
   - For Markdown: preserve frontmatter and heading structure

   **File Content with Conflicts:**

   ```
   <original file content>
   ```

   **Task:**
   Return the COMPLETE resolved file content with NO conflict markers.
   Return ONLY the file content, no explanations, no markdown code blocks.
   ````

   The prompt carries only the path, the type, and the conflicted content. It must not include environment variables, credentials, or any other file.

4. Create `internal/conflict/conflict_test.go` (package `conflict`) with a local git fixture and a stub `claude` on PATH. Suggested helper pattern (adapt names as needed):

   ```go
   // writeStub writes an executable `claude` script into a fresh temp dir,
   // prepends that dir to PATH, and returns the dir. The script drains stdin first
   // so the CLI never blocks on a closed pipe, then runs body.
   func writeStub(t *testing.T, body string) string {
       t.Helper()
       dir := t.TempDir()
       script := "#!/bin/sh\ncat >/dev/null\n" + body
       path := filepath.Join(dir, "claude")
       if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
           t.Fatalf("write claude stub: %v", err)
       }
       t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
       return dir
   }
   ```

   Write the response envelope to a file and have the stub `cat "$CLAUDE_STUB_RESPONSE"`; build the envelope with `encoding/json` in the test so no shell quoting is needed. Set `t.Setenv("CLAUDE_STUB_RESPONSE", path)`. A recording stub additionally runs `printf invoked >> "$CLAUDE_STUB_LOG"` with `CLAUDE_STUB_LOG` set to a temp path.

   The fixture creates a repository with `git init`, local `user.name` / `user.email`, `commit.gpgsign=false`, `core.hooksPath` pinned to `$(git rev-parse --absolute-git-dir)/hooks`, one initial commit, and helpers to write files and run git. Cover these cases:
   - `TestResolveFileWritesBothSidesAndStripsFence`: a non-`.md` file whose content is a complete conflict block (`<<<<<<< HEAD`, `from-ours`, `=======`, `from-theirs`, `>>>>>>> other`, each line newline-terminated); the stub returns a fenced answer containing both lines. After `ResolveFile`: the file contains `from-ours` and `from-theirs`, contains no `<<<<<<<`, contains no three-backtick fence, and `git diff --cached --name-only` lists the path.
   - `TestResolveFileNoMarkersStagesWithoutModel`: a file whose content parses to zero marker blocks; a recording stub. After `ResolveFile`: the bytes are unchanged, `git diff --cached --name-only` lists the path, and the invocation log does not exist or is empty. This is Acceptance Criterion 9.
   - `TestResolveFileCLIExitsNonZeroLeavesFileUntouched`: a marker file; the stub exits 1. `ResolveFile` returns an error, and the bytes are unchanged. Acceptance Criterion 2.
   - `TestResolveFileEmptyEnvelopeLeavesFileUntouched`: the stub emits `{"type":"result","is_error":false,"subtype":"success","result":""}`. `ResolveFile` returns an error whose message contains `empty`, and the bytes are unchanged. Acceptance Criterion 3.
   - `TestResolveFileMissingEnvelopeLeavesFileUntouched`: the stub exits 0 and prints nothing. `ResolveFile` returns an error and the bytes are unchanged. Acceptance Criterion 4.
   - `TestResolveFileAuthenticationFailureLeavesFileUntouched`: the stub emits a valid envelope whose `result` is `Failed to authenticate`. `ResolveFile` returns an error whose message contains `authenticat`, and the bytes are unchanged. Acceptance Criterion 5.
   - `TestResolveFileIsErrorEnvelopeLeavesFileUntouched`: the stub emits `{"type":"result","is_error":true,"subtype":"error_during_execution","result":"boom"}`. `ResolveFile` returns an error whose message contains `error_during_execution`, and the bytes are unchanged. Acceptance Criterion 6.
   - `TestResolveFileRefusesFrontmatterLoss`: the conflicted file is `note.md` and its content begins with `---` and contains a complete conflict block; the stub returns merged markdown with no frontmatter. `ResolveFile` returns an error whose message contains `frontmatter`, and the bytes are unchanged. This must exercise `ResolveFile` (the path that writes), not only the guard helper. Acceptance Criterion 7.
   - `TestResolveFileKeepsFrontmatterFromFencedAnswer`: same `note.md` input; the stub returns the merged content wrapped in a three-backtick fence with the `---` block intact. After `ResolveFile`: the first line is `---`, both `from-ours` and `from-theirs` appear, and the file contains no three-backtick fence. Acceptance Criterion 8.
   - `TestResolveMarkerFlaggedFilesResolvesAndRestages`: stage a file whose content is a complete conflict block, then call `ResolveMarkerFlaggedFiles(ctx, repo, []string{"note.txt"})` with a stub returning both lines. Afterward: both lines appear, `<<<<<<<` does not, `git ls-files --error-unmatch note.txt` exits 0, and `git diff --cached --name-only` lists `note.txt`. Acceptance Criterion 10.
   - Optionally `TestParseConflictMarkers`: one complete block parses to one `conflictBlock` with the expected `ours` / `theirs`; content without markers parses to an empty slice.

   Each test uses its own `t.TempDir()` and is not parallel. Set `t.Setenv("GIT_EDITOR", "true")` in the fixture even though this prompt does not continue a merge or rebase, so the pattern is ready for prompt 2. The suite must pass with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.

5. Add tests for the new `internal/git` helpers in `internal/git/git_test.go`, reusing the existing fixture helpers:
   - `TestIsInMerge`: false on a fresh clone; true after writing `<repo>/.git/MERGE_HEAD`.
   - `TestIsInRebase`: false on a fresh clone; true after creating `<repo>/.git/rebase-merge`; remove it and true again after creating `<repo>/.git/rebase-apply`.
   - `TestConflictedFiles`: build the same conflicting pull the existing `TestPullConflictWrapsErrConflict` builds, then assert `ConflictedFiles` returns the conflicted path.
   - `TestStageFile`: write or modify a file, call `StageFile`, and assert `git diff --cached --name-only` lists it.
   `ContinueMerge` / `ContinueRebase` are covered by prompt 2's merge and rebase tests; do not duplicate that fixture here.

6. Add a `## Unreleased` section to `CHANGELOG.md` directly after the SemVer preamble block and before `## v0.16.1`, containing one bullet:

   ```markdown
   - feat: add `internal/conflict`, a fail-closed Claude CLI conflict resolver (JSON envelope with `is_error` / empty / authentication checks, markdown frontmatter guard, code-fence stripping, atomic single-file write and staging) plus the exported `internal/git` conflict helpers it needs
   ```

   Do not move or edit the `# Changelog` title or the SemVer preamble bullets.
</requirements>

<constraints>
- New package `internal/conflict`. It may import `internal/git`; `internal/git` must not import it, so the dependency direction stays one-way.
- `internal/git` gains exactly the exported helpers `IsInMerge`, `IsInRebase`, `ConflictedFiles`, `StageFile`, `ContinueMerge`, `ContinueRebase`. Do not change the conflict detection heuristics; the existing `MERGE_HEAD` / `rebase-merge` / `rebase-apply` probe stays as it is.
- The Claude invocation must be an `exec.CommandContext` with the caller's context. No goroutine may outlive the call.
- The CLI is invoked as `claude -p --output-format json` with the prompt on **stdin**. A body that is not valid JSON is a failed resolution, not a text answer.
- Write the resolved content only after every check has passed. Never write first and validate second, and never write a partial result. The file is replaced atomically or not at all.
- The frontmatter guard compares the original file's first three bytes against the resolved content's. It applies only to `.md` files, and only when the original began with `---`.
- A fenced answer has its fence removed before the frontmatter guard runs.
- The marker-flagged resolver ships without a production caller in this change. Do not present it as wired into the commit path, because it is not.
- The model is the CLI's default: pin no `--model` and add no model configuration.
- No test may call a real model, read real credentials, or depend on `HOME`; stub the `claude` executable on `PATH` in a temporary directory and never install it.
- New code uses `github.com/bborbe/errors` for wrapping. No `fmt.Errorf`, and no bare `return err` in new code.
- Do NOT modify any Python file under `src/` or `tests/`, and do NOT modify `pyproject.toml` or `uv.lock`.
- Do NOT touch `.github/workflows/` or anything under `.github/`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
gofmt -l cmd internal
go test ./internal/conflict/... -count=1 -v
go test ./internal/git/... -count=1 -v
make precommit-go
```

Expected: the build, vet, both package test runs, and `make precommit-go` exit 0; `gofmt -l cmd internal` prints nothing.

Before finishing, walk Acceptance Criteria 2-10 against the tests:
- non-zero CLI exit leaves the file byte-identical (2);
- an empty `result` in a valid envelope leaves it byte-identical and names `empty` (3);
- empty stdout leaves it byte-identical (4);
- a valid envelope containing `Failed to authenticate` leaves it byte-identical and names authentication (5);
- an `is_error` envelope leaves it byte-identical and names the subtype (6);
- the resolution path refuses a frontmatter loss (7) and accepts a fenced answer that keeps its frontmatter (8);
- a file with no marker blocks is staged without invoking the stub (9);
- the marker-flagged resolver resolves and re-stages with both sides present (10).

Do not run `make precommit`; it is Python-only in this repo and never compiles a Go file.
</verification>
