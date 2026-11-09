---
status: prompted
approved: "2026-11-09T23:14:51Z"
generating: "2026-11-09T23:16:40Z"
prompted: "2026-11-09T23:29:27Z"
branch: feat/port-conflict-resolution
---

## Summary

- `git-ai-sync` resolves a merge or rebase conflict by asking the Claude CLI for the merged file, then writing it back and staging it
- The Python tool is deliberately fail-closed: since v0.13.1 a failed resolution raises instead of writing the CLI's error text into the file
- On 2026-10-19 the earlier behaviour replaced a 52 KB task file with a 72-byte `Failed to authenticate` string
- The Go binary must carry the same contract, not a simplified one: a wrong file here corrupts the vault, not a log line
- `git-ai-sync resolve [repo-path]` resolves **every** conflicted file, continues the merge or rebase, and pushes — the contract `entrypoint.sh:72` already calls as `git-ai-sync resolve "$VAULT"`
- Both merge and rebase states are supported, as they are today

## Problem

`internal/git` already detects the conflict state — `Pull` returns an error wrapping `ErrConflict` when `MERGE_HEAD` or `rebase-merge`/`rebase-apply` exists — but nothing in the Go binary can resolve a conflict. `git-ai-sync watch` therefore stops at a conflicted pull and stays stopped; the Python tool would have resolved it and continued.

The Python contract is stricter than "ask Claude and write the answer", and each check exists because of a real incident:

- the CLI reports `is_error` in its result envelope → fail
- `"Failed to authenticate"` appears in **either** the streamed text or the result → fail
- the response is empty → fail
- a `.md` file whose original content began with `---`, resolved to something that no longer begins with `---` → fail, because that is a dropped frontmatter and Obsidian loses the note's metadata
- staging fails after a successful write → the file counts as failed, not resolved
- the answer arrives inside a markdown code fence → the fence is stripped before the content is compared or written

A Go port that "simplifies" any of these reintroduces silent vault corruption across all twelve vaults at once. The empty-response check is the one most likely to be dropped, because a CLI that exits 0 and prints nothing looks like success.

## Goal

The Go binary resolves merge and rebase conflicts through the Claude CLI with the same fail-closed contract, exposes it as `git-ai-sync resolve [repo-path]`, resolves every conflicted file, stages what it writes, continues the merge or rebase, and pushes. A failed, empty, or unauthenticated resolution leaves the file byte-identical and the repository still conflicted.

## Non-goals

- Changing the conflict **detection heuristics**. The existing `MERGE_HEAD` / `rebase-merge` / `rebase-apply` probe stays as it is; this spec adds exported accessors over it, not a new detector.
- Improving the resolution prompt beyond parity with the Python one.
- Wiring automatic resolution into `watch`. The watch loop may continue to stop on `ErrConflict`; resolving unattended is a separate decision.
- Porting `commit_with_marker_recovery`'s **trigger**. Go's `Commit` has no refusal classes, so there is no hook-refusal signal to hang it on. The marker-flagged *resolution* is in scope (it is `resolve_marker_flagged_files`); detecting that the hook refused is not.
- Any change to the Python source. It is the reference implementation and stays untouched.
- Touching `.github/workflows/`.

## Assumptions

- The container and the operator's machine both have a `claude` CLI on `PATH`. The Python tool used `claude_code_sdk`; there is no Go equivalent, so the Go port shells out.
- **The prompt is passed on stdin, and the answer is requested as JSON** — `claude -p --output-format json`. File content can be large, so argv is the wrong carrier; and the `is_error` / `subtype` fields exist only in the JSON envelope, so plain text cannot express the contract. This pins the shape the stub must imitate.
- A CLI that exits non-zero, prints nothing, or reports an error in its envelope is a failed resolution. The port names which failure occurred; it does not attempt to retry.
- `git ls-files --error-unmatch <path>` is a valid "is this path in the index" probe, and `git diff --cached --name-only` lists staged paths.

## Acceptance Criteria

⚠️ **The fail-closed probes drive the JSON envelope, never bare stdout.** Under `--output-format json` the answer is the envelope's `result` field. A probe that prints nothing to stdout is rejected by any JSON parse, so it proves only that the port parses JSON — not that it checks for an empty answer. Each stub below emits a **well-formed** envelope unless the AC says otherwise.

- [ ] A forced conflicting pull resolves and pushes — probe: in a throwaway repo with a real conflict on one file and a bare remote as upstream, `git-ai-sync resolve .` exits 0; the resolved file carries the unique line planted on each side (`grep -c 'from-ours'` = 1 and `grep -c 'from-theirs'` = 1), carries no conflict marker (`grep -c '<<<<<<<'` = 0); the command's output names that path (`2>&1 | grep -q '<path>'`); and the push landed (`git -C <remote> rev-parse master` equals `git rev-parse HEAD`). A truncated file, a single-side file, or an unpushed commit fails this.
- [ ] The CLI exiting non-zero fails closed — probe: a stub that exits 1; the conflicted file's `shasum -a 256` is unchanged, `git status --porcelain` still shows `UU`, and the command exits non-zero.
- [ ] An **empty `result` in a valid envelope** fails closed — probe: a stub that exits 0 and prints `{"type":"result","is_error":false,"subtype":"success","result":""}`; the file is byte-identical (`shasum -a 256` equal), the command exits non-zero, and the message names an empty response (`2>&1 | grep -qi 'empty'`). This is the incident class: writing `env.Result` unconditionally turns this into a zero-byte file.
- [ ] A **missing envelope** (empty stdout) fails closed — probe: a stub that exits 0 and prints nothing; the file is byte-identical and the command exits non-zero. Kept as its own case because a port can handle one of these two empty shapes and miss the other.
- [ ] An authentication failure **inside a valid envelope** fails closed — probe: a stub that exits 0 and prints `{"type":"result","is_error":false,"subtype":"success","result":"Failed to authenticate"}`; the file is byte-identical and the command exits non-zero naming authentication (`2>&1 | grep -qi 'authenticat'`). The envelope is valid JSON on purpose: a bare auth string would be rejected by the parse, so a port with no auth check would pass that probe by accident.
- [ ] An `is_error` envelope fails closed — probe: a stub printing `{"type":"result","is_error":true,"subtype":"error_during_execution","result":"boom"}`; the file is byte-identical, the command exits non-zero, and the message contains `error_during_execution`.
- [ ] A `.md` file whose content started with `---` is never accepted in a resolved form that lost it — probe: drive the **resolution path** with a stub returning markdown without frontmatter; the file on disk stays byte-identical (`shasum -a 256` equal) and the output carries the refusal (`2>&1 | grep -qi 'frontmatter'`). A guard function that exists but which the resolve path never calls fails this criterion — the probe must exercise the path that writes the file.
- [ ] A **`.md` file with frontmatter resolves successfully and keeps it** — probe: the conflicted file is `note.md` whose content begins with `---`; a stub returns the correct merged content, wrapped in a ```` ``` ```` fence, **with the `---` block intact**; after the run `head -1 <file>` is `---`, `grep -c 'from-ours'` = 1, `grep -c 'from-theirs'` = 1, and `grep -c '```' <file>` = 0. This is the positive half of the frontmatter guard: without it, an implementation that refuses **every** `.md` file containing `---` passes the guard criterion outright while making the resolver useless on markdown — which is most of a vault. It also proves the fence is stripped before the guard runs, so a fenced answer with intact frontmatter is not mistaken for a frontmatter loss.
- [ ] A file with **no conflict markers** is passed through without calling the model — probe: a Go test resolves a conflicted path whose content parses to zero marker blocks, with a stub that records its invocations; the file is unchanged, staged, and the stub was **never invoked**. This matches `conflict_resolver.py:139-141`. Without it a binary conflicted file (`uv.lock`, an image) is sent to the model and the answer is written over it.
- [ ] A file flagged for conflict markers is resolved **and re-staged** — probe: the marker-flagged resolver runs on a repository whose index already holds a file containing `<<<<<<<`; afterwards the file carries both sides (`grep -c 'from-ours'` = 1, `grep -c 'from-theirs'` = 1), `grep -c '<<<<<<<'` = 0, `git ls-files --error-unmatch <file>` exits 0, and `git diff --cached --name-only` lists it. Both-sides assertions are required: an empty file otherwise passes the marker and staging checks.
- [ ] The `resolve` subcommand works in both merge and rebase states — probe: one run against an in-merge repo and one against an in-rebase repo, each with a bare remote; both exit 0, both files carry both sides with `grep -c '<<<<<<<'` = 0, `git diff --cached --name-only` lists them, and each push landed.
- [ ] `resolve` refuses outside a conflict state — probe: against a clean repository it exits non-zero and writes nothing.
- [ ] `resolve` refuses when the conflict state holds no conflicted files — probe: a repository with `MERGE_HEAD` present and an empty `git diff --diff-filter=U`; the command exits non-zero and its output names the condition (`2>&1 | grep -qi 'no conflicted'`), rather than reporting success for doing nothing.
- [ ] `resolve` takes the same instance lock as `sync` — probe: the test acquires the lock directly through `internal/lock` (deterministic, no sleep race), then runs `resolve`; it exits non-zero naming the lock (`2>&1 | grep -qi 'lock'`). Without the lock a `resolve` and a `watch` can write the same file concurrently.
- [ ] The port changes no Python behaviour — probe: after `git fetch origin master`, `git diff --name-only origin/master -- src tests pyproject.toml uv.lock` is empty.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit-go` — must exit 0. **Not `make precommit`**: in this repo that target is Python-only (`precommit: sync format test check` → `uv sync`, `ruff`, `pytest`, `mypy`) and never compiles or runs a single Go file, so a syntax error in a new Go package would pass it. `precommit-go` is the Go gate: `gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`.
- Every acceptance criterion above is driven by a Go test that builds its own throwaway repository and puts a **stub `claude` executable on `PATH`**, so the suite never calls a real model and never depends on network or credentials. The stub's behaviour is the test's input: it returns a good merge, a non-zero exit, an empty body, an authentication string, an `is_error` envelope, frontmatter-stripped markdown, or a fenced answer. This is how the fail-closed branches become testable at all.
- **Fixture isolation**, following the pattern `internal/sync/sync_test.go` already uses: each fixture sets a local `user.name` / `user.email`, pins `core.hooksPath` to its own `.git/hooks`, and sets `GIT_EDITOR=true` so `git rebase --continue` and `git commit --no-edit` cannot block on an editor. The suite must pass both with the developer's normal global git config and with `GIT_CONFIG_GLOBAL=/dev/null`.
- The tests must not read or write the operator's real `CLAUDE_CONFIG_DIR`, `ANTHROPIC_*` environment, or the macOS Keychain. A test that depends on the machine's auth state is not a test.

### Operator-executable (runs on the host after merge)

- One real end-to-end run against a throwaway clone with a genuine conflict and a bare remote, using the real Claude CLI: `git-ai-sync resolve .` exits 0, the file carries both sides and no markers, `git status --porcelain` no longer shows `UU`, and the push lands. Record the output in `# Progress`.
- A second run with the real CLI unauthenticated (an empty `CLAUDE_CONFIG_DIR`) confirming the file is untouched — the check the container cannot make honestly, because the container's auth state is not the operator's.

## Desired Behavior

1. `git-ai-sync resolve [repo-path]` defaults to `.`, matching the Python CLI and `entrypoint.sh:72`.
2. It acquires the instance lock, refuses when the repository is not in a merge or rebase conflict state, and lists the conflicted paths.
3. For each conflicted file it reads the content and, **if that content parses to zero conflict-marker blocks, writes it back unchanged and stages it without invoking the model** — matching `conflict_resolver.py:139-141`. Otherwise it sends the content to the CLI on stdin and receives a JSON envelope.
4. It writes the resolved content **only after** every check has passed, then stages the file.
5. When every file resolved, it continues the merge or rebase — `git rebase --continue` for a rebase, and `git commit --no-edit` for a merge, matching `git_operations.py:600-618`. `merge --continue` is a different command and is not a substitute.
6. It then pushes, as the Python tool does.
7. If any file failed, the command exits non-zero naming those paths; files that did resolve stay resolved and staged.
8. If continuation leaves conflicts, it exits non-zero naming the still-conflicted paths.
9. A failed, empty, unauthenticated, frontmatter-stripped or `is_error` resolution leaves that file byte-identical.

## Constraints

- New package `internal/conflict`. It may import `internal/git`; `internal/git` must not import it, so the dependency direction stays one-way.
- `internal/git` gains the exported helpers the resolver needs, because the existing ones are unexported or missing: `IsInMerge`, `IsInRebase`, `ConflictedFiles`, `StageFile`, `ContinueMerge`, `ContinueRebase`. `inConflictState` is unexported today and `StageAll` is the only staging helper — the resolver must stage **one** path.
- The Claude invocation must be an `exec.CommandContext` with the caller's context, so a cancelled sync cancels the model call. No goroutine may outlive the call.
- The CLI is invoked as `claude -p --output-format json` with the prompt on **stdin**. The envelope is parsed for `is_error`, `subtype` and `result`; a body that is not valid JSON is a failed resolution, not a text answer.
- **Write the resolved content only after every check has passed.** Never write first and validate second, and never write a partial result. The file is replaced atomically or not at all.
- The frontmatter guard compares the **original** file's first three bytes against the resolved content's. It applies only to `.md` files, and only when the original began with `---`.
- A resolved answer beginning with a markdown code fence has the fence removed before the frontmatter guard runs, so a fenced answer with intact frontmatter is not rejected as a frontmatter loss.
- The marker-flagged resolver ships **without a production caller** in this change. In the reference its only caller is `commit_with_marker_recovery`'s marker arm (`conflict_resolver.py:420`), and Go's `Commit` has no refusal classes to hang that trigger on — porting the classifier is a separate spec. The function is included because this task's success criterion requires it and its contract is identical; it is exercised directly by its own test. Do not present it as wired into the commit path, because it is not.
- The model is the CLI's default. The Python tool passes `config.model` (default `claude-sonnet-4-5-20250929`, settable via `git-ai-sync config --model`); the Go port pins no `--model` and gains no model config. This is a declared divergence: adding a model knob is not part of this task.
- `resolve` acquires the same instance lock as `sync` and releases it on every exit path.
- No test may call a real model, read real credentials, or depend on `HOME`. Stub the CLI on `PATH`.
- New code uses `github.com/bborbe/errors` for wrapping, per the repo's style. No `fmt.Errorf`, no bare `return err` **in new code** — the existing `internal/cli` returns bare errors and is not being rewritten.
- Add a `CHANGELOG.md` bullet under `## Unreleased`.
- Nothing may touch `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit under `.github/` is rejected at push.
- The Python source under `src/`, the Python tests under `tests/`, `pyproject.toml` and `uv.lock` must not change.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| CLI exits non-zero | File byte-identical, command non-zero, path named | Operator fixes the CLI/network and re-runs `git-ai-sync resolve .`; `git status --porcelain` still shows `UU` until then |
| CLI exits 0, prints nothing | File byte-identical, command non-zero, "empty response" named | Same as above — nothing was written, so a re-run is safe |
| `Failed to authenticate` in the answer text | File byte-identical, command non-zero | Operator repairs the CLI session, then re-runs; no partial file to undo |
| `is_error` in the result envelope | File byte-identical, command non-zero, subtype named | Operator reads the subtype; a rate-limit subtype is retried after a wait |
| A rate limit is hit | Reported as `is_error: true` with a rate-limit subtype inside the envelope; file byte-identical | Wait, then re-run — nothing was written, so a retry is safe |
| Push rejected after a successful resolution | Files resolved, staged and committed; the command exits non-zero naming the push failure | Operator pulls/rebases and pushes; the resolution is already committed and is not lost |
| `.md` answer lost its frontmatter | File byte-identical, refusal logged | Operator resolves that file by hand; the guard will not let a broken note through |
| Stage fails after a successful write | File counts as failed, command non-zero | `git add <path>` then re-run; the worktree already holds the resolved content |
| Continuation leaves conflicts | Command non-zero naming the still-conflicted paths | Operator resolves the remainder and continues; nothing is silently finished |
| A second `resolve` while one runs | Refused by the instance lock | Wait for the first to finish — never run two over one vault |
| Model unavailable or slow | `exec.CommandContext` honours cancellation; no goroutine outlives the call | Cancel the sync; re-run later |

## Security / Abuse Cases

The resolver sends file contents to a model. That is already true of the Python tool and is the product's stated behaviour, so the port adds no new exposure. It must not broaden it: no file outside the conflicted set is read, no credential is logged, and the prompt carries only the path and the conflicted content. The `Failed to authenticate` string is matched as an indicator, never logged as content. The stub used by tests lives in a temporary directory on `PATH` and is never installed.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `internal/conflict` core: marker parsing, the no-marker short-circuit, the JSON CLI invocation with every fail-closed check, the frontmatter guard, fence stripping, single-file resolve + stage, the marker-flagged resolver, and the exported `internal/git` helpers it needs | 3, 4, 9 | 2, 3, 4, 5, 6, 7, 8, 9, 10 | — |
| 2 | The `resolve` subcommand: repo-path argument, conflict-state and empty-conflict guards, all-files loop, instance lock, continuation, push | 1, 2, 5, 6, 7, 8 | 1, 11, 12, 13, 14 | 1 |

## Alternatives Considered

- **Call the model over HTTP directly instead of shelling out to the CLI.** Rejected: it would need its own credential handling, and the repo already routes models through the local router and the `claude` CLI. A second auth path is a second thing to leak.
- **Read the answer as plain stdout instead of a JSON envelope.** Rejected: `is_error` and `subtype` exist only in the envelope, so a plain-text shell-out cannot express the contract the Python tool enforces. It also makes the `rate_limit_event` stream message writable into a vault file.
- **Write the file first and restore it on failure.** Rejected: the restore is the failure mode. A crash between write and restore leaves the corrupted file, which is precisely the incident.
- **Skip the frontmatter guard for simplicity.** Rejected: it is a data-loss guard, not a formatting preference, and the vault it protects is autocommitted.
- **Resolve automatically inside `watch`.** Rejected for this task: it changes when a model is called during an unattended loop, which deserves its own decision.

## Do-Nothing Option

The Go binary keeps stopping at any conflict. The twelve vaults run the Go watcher, so a single conflicted pull halts sync for that vault until a human notices — which is the failure the Python tool was built to remove. The port's own success criterion for conflict handling stays unmet, and the cut-over task cannot start.
