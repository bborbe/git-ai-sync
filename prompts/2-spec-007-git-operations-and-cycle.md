---
status: draft
spec: [007-go-sync-loop-prototype]
created: "2026-11-09T18:54:14Z"
branch: dark-factory/go-sync-loop-prototype
---

# Implement the git operations and one sync cycle

<summary>
- The binary gains the git operations a sync cycle needs: finding the repository root, checking for changes, staging, committing, pulling with merge or rebase, pushing, and detecting whether the local branch is ahead.
- One sync cycle stages, commits, pulls, then pushes; a dirty worktree is committed before the pull.
- A clean worktree is pulled, and pushed only when the local branch is ahead; the one-shot command keeps its own "no changes" behaviour.
- A commit refused by a pre-commit hook is reported but does not stop the pull.
- A pull conflict is reported as a distinct failure so the caller can stop.
- Every operation is tested against a locally created repository, with no network.
- Both merge and rebase strategies land a remote commit and push the local edit.
- No interpreter is invoked; only `git`.
</summary>

<objective>
Implement `internal/git` (repo discovery, status, stage, commit, pull, push, ahead-of-remote, commit message) and `internal/sync` (the stage → commit → pull → push cycle, with watch and one-shot modes), with tests against local scratch repositories. **Why:** this is the sync semantics the port must reproduce exactly, and it is the piece the watch loop in prompt 5 drives.
</objective>

<context>
Read the approved spec at `specs/in-progress/007-go-sync-loop-prototype.md` — Desired Behavior 5, the Failure Modes table, and Acceptance Criteria 7, 8 and 9 are the contract for this prompt.

Read `src/git_ai_sync/git_operations.py` for the exact git commands and error semantics. Read `src/git_ai_sync/__main__.py` (`cmd_watch` and `cmd_sync`) for the cycle order and the one-shot "no changes to sync" behaviour. Do not port the marker/unsafe-path recovery classes: the spec says every commit refusal is the continuing case, and marker recovery is out of scope.

Read `internal/config/config.go` and `internal/clock/clock.go` from prompt 1. The module path is `github.com/bborbe/git-ai-sync`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors` (`Wrapf`, `Wrap`, `Errorf`); sentinel errors use stdlib `errors` aliased as `stderrors`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the test conventions, but use stdlib `testing` (not Ginkgo/Gomega).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — every external command takes `ctx`.

Do not include the word `python` (any case) in any Go source file under `cmd/` or `internal/` — Acceptance Criterion 15 greps for it. The only subprocess allowed is `git`; never invoke a shell or an interpreter.
</context>

<requirements>
1. Create `internal/git/git.go` with exactly this public surface (exported identifiers documented):

   ```go
   package git

   import (
       "context"
       "time"

       stderrors "errors"
   )

   // Strategy selects how a pull integrates remote commits.
   type Strategy string

   const (
       // StrategyMerge integrates with a merge commit (git pull --no-rebase).
       StrategyMerge Strategy = "merge"
       // StrategyRebase replays local commits on top of the remote (git pull --rebase).
       StrategyRebase Strategy = "rebase"
   )

   // ErrConflict reports that a pull stopped in a merge or rebase conflict state.
   var ErrConflict = stderrors.New("pull conflict")

   // FindRepo resolves path and walks up until it finds a directory containing .git.
   func FindRepo(ctx context.Context, path string) (string, error)

   // HasChanges reports whether the worktree or index has uncommitted changes.
   func HasChanges(ctx context.Context, repo string) (bool, error)

   // StageAll stages every change in the worktree.
   func StageAll(ctx context.Context, repo string) error

   // Commit creates a commit with the given message. Any non-zero exit is returned
   // as an error; there are no special refusal classes.
   func Commit(ctx context.Context, repo string, message string) error

   // Pull integrates remote commits with the given strategy. A pull that stops in a
   // merge or rebase conflict state returns an error wrapping ErrConflict.
   func Pull(ctx context.Context, repo string, strategy Strategy) error

   // Push pushes the current branch to its upstream.
   func Push(ctx context.Context, repo string) error

   // IsAheadOfRemote reports whether HEAD has commits not on its upstream. A missing
   // upstream or a failed command is reported as (false, nil), matching the reference
   // tool's "not ahead" behaviour.
   func IsAheadOfRemote(ctx context.Context, repo string) (bool, error)

   // GenerateCommitMessage formats "<prefix>: <RFC3339 timestamp>".
   func GenerateCommitMessage(prefix string, now time.Time) string
   ```

   Implementation rules:

   - Add one unexported helper `runGit(ctx context.Context, repo string, args ...string) (string, error)` that runs `exec.CommandContext(ctx, "git", args...)` with `cmd.Dir = repo`, captures combined output, and on a non-zero exit returns `errors.Wrapf(ctx, err, "git %s: %s", strings.Join(args, " "), string(out))`. Every operation uses this helper. Never use a shell.
   - `FindRepo`: resolve with `filepath.Abs` (do not require the path to exist), then walk up with `filepath.Dir` until the parent stops changing; return the first directory where `os.Stat(filepath.Join(dir, ".git"))` succeeds. Return `errors.Errorf(ctx, "not a git repository: %s", path)` when none is found.
   - `HasChanges`: run `git status --porcelain`; return `len(strings.TrimSpace(out)) > 0`.
   - `StageAll`: run `git add .`.
   - `Commit`: run `git commit -m <message>`. Return the error unchanged in kind (wrapped by the helper); do not classify it.
   - `Pull`: `merge` runs `git pull --no-rebase`; `rebase` runs `git pull --rebase`. On a non-zero exit, check the conflict state:
     - merge conflict: `os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))` succeeds;
     - rebase conflict: `os.Stat(filepath.Join(repo, ".git", "rebase-merge"))` or `os.Stat(filepath.Join(repo, ".git", "rebase-apply"))` succeeds.
     If in a conflict state, return `errors.Wrapf(ctx, ErrConflict, "pull conflict: %s", out)`. Otherwise return the wrapped pull error.
   - `Push`: run `git push`.
   - `IsAheadOfRemote`: run `git rev-list --count @{upstream}..HEAD`; on any error return `(false, nil)`; parse the integer and return `(n > 0, nil)`.
   - `GenerateCommitMessage`: `fmt.Sprintf("%s: %s", prefix, now.Format(time.RFC3339))`. Do not read a clock inside this function.

2. Create `internal/sync/sync.go`:

   ```go
   package sync

   import (
       "context"

       "github.com/bborbe/git-ai-sync/internal/clock"
       "github.com/bborbe/git-ai-sync/internal/git"
   )

   // Options controls one sync cycle.
   type Options struct {
       Strategy     git.Strategy
       CommitPrefix string
       PullWhenClean bool
       Clock        clock.Clock
   }

   // Result reports what a cycle did. CommitErr is set when the commit was refused;
   // the cycle still ran the pull, and the caller logs CommitErr.
   type Result struct {
       HadChanges bool
       Committed  bool
       CommitErr  error
       Pulled     bool
       Pushed     bool
       NoChanges  bool
   }

   // Run performs one sync cycle.
   func Run(ctx context.Context, repo string, opts Options) (Result, error)
   ```

   Behaviour:

   a. `dirty, err := git.HasChanges(ctx, repo)`; on error return `Result{}, err`.

   b. Clean worktree (`!dirty`):
      - If `!opts.PullWhenClean`, return `Result{NoChanges: true}, nil` without pulling. This is the one-shot `sync` behaviour.
      - Otherwise run `git.Pull(ctx, repo, opts.Strategy)`; on error return `Result{}, err`.
      - Run `git.IsAheadOfRemote`; if ahead, run `git.Push`; on error return `Result{Pulled: true}, err`.
      - Return `Result{Pulled: true, Pushed: <whether pushed>}, nil`.

   c. Dirty worktree:
      - `git.StageAll`; on error return `Result{HadChanges: true}, err`.
      - `message := git.GenerateCommitMessage(opts.CommitPrefix, opts.Clock.Now())`.
      - `commitErr := git.Commit(ctx, repo, message)`. Record it; do NOT return on failure — continue to the pull.
      - `git.Pull(ctx, repo, opts.Strategy)`; on error return `Result{HadChanges: true, Committed: commitErr == nil, CommitErr: commitErr}, err`.
      - `git.Push(ctx, repo)`; on error return `Result{HadChanges: true, Committed: commitErr == nil, CommitErr: commitErr, Pulled: true}, err`.
      - Return `Result{HadChanges: true, Committed: commitErr == nil, CommitErr: commitErr, Pulled: true, Pushed: true}, nil`.

   Invariants: stage before commit; commit before pull; pull before push; the pull runs even when the commit failed; the push is not skipped after a successful commit; a commit failure is reported in `Result.CommitErr` and does not abort the pull. On a pull conflict the returned error wraps `git.ErrConflict`. `Run` does not log; the caller owns logging.

3. Create the tests. Add a shared fixture helper in `internal/sync/sync_test.go` (and, if useful, `internal/git/git_test.go`) that builds a local scratch repository. It must:

   - create a bare remote with `git init --bare <dir>/remote.git` and point its HEAD at `refs/heads/master` with `git symbolic-ref HEAD refs/heads/master`;
   - clone it to `<dir>/clone`, set `user.email`, `user.name` and `commit.gpgsign false`;
   - write `.gitignore` containing `/.git-ai-sync.lock` and commit it, plus an initial `note.md`, and push `-u origin master`;
   - provide a `runGit(t, dir, args...) string` helper that fails the test on a non-zero exit.

   Use `t.TempDir()` and `exec.Command("git", ...)` only. No network. Every assertion reads git state with the helper, not with a shell pipeline.

4. `internal/git/git_test.go` must cover:
   - `FindRepo` from a subdirectory and from a directory that is not a repository (error).
   - `HasChanges` true with an uncommitted edit and false after a commit.
   - `StageAll` followed by `Commit` creates a commit whose subject is the message.
   - `Pull` with merge and with rebase each bring a remote-only commit into the clone.
   - `Pull` returns an error wrapping `ErrConflict` when both sides edit the same line.
   - `Push` lands a local commit on the bare remote.
   - `IsAheadOfRemote` true after a local commit and false after a push; `(false, nil)` when there is no upstream.
   - `GenerateCommitMessage("auto", fixedTime)` equals `"auto: <fixedTime in RFC3339>"`.

5. `internal/sync/sync_test.go` must cover the acceptance criteria:

   a. **Dirty + merge (AC7 clauses a–f).** Build a clone with an uncommitted edit to `note.md`, and create a remote-only commit from a second clone (a new file pushed to the bare remote). Run `Run` with `PullWhenClean: true`, `Strategy: git.StrategyMerge`, `CommitPrefix: "auto"`, and a fake clock. Assert:
      - (a) `git status --porcelain` is empty;
      - (b) `git rev-list --count origin/master..HEAD` is `0`;
      - (c) `git rev-parse HEAD` in the clone equals `git rev-parse master` in the bare remote;
      - (d) `git show master:note.md` in the bare remote contains the edit;
      - (e) the clone's `git log --format=%s` has exactly one subject starting with `auto:`;
      - (f) `git merge-base --is-ancestor <remote-only-commit-sha> HEAD` in the clone exits 0.

   b. **Dirty + rebase (AC9).** Repeat the same fixture with `Strategy: git.StrategyRebase`; assert clauses (a)–(f) unchanged. Do not assert commit counts: a merge cycle adds three commits and a rebase cycle adds two.

   c. **Pull-only cannot satisfy (AC8).** On a fresh dirty clone with the same remote-only commit, run `git pull --no-rebase` without committing (tolerate a non-zero exit; the assertion is about the resulting remote state), then assert clause (d) fails (the edit is not on the remote) and clause (e) returns `0`. This documents that the assertions discriminate a commit-then-pull cycle from a bare pull.

   d. **Clean + watch (AC14).** On a clean clone with a new remote-only commit, run `Run` with `PullWhenClean: true`; assert `git rev-list --count HEAD..origin/master` is `0` and the remote-only commit is an ancestor of HEAD.

   e. **Clean + one-shot (AC14).** On a clean clone with a new remote-only commit, run `Run` with `PullWhenClean: false`; assert `Result.NoChanges` is true and `git rev-list --count HEAD..origin/master` is greater than `0` (nothing was pulled).

   f. **Commit refusal (Failure Modes).** Write an executable `.git/hooks/pre-commit` that exits 1. With a dirty worktree and a remote-only commit, run `Run`; assert the returned error is nil, `Result.CommitErr` is non-nil, the remote-only commit is an ancestor of HEAD (the pull ran), and `git status --porcelain` still shows the uncommitted edit.

   g. **Push rejected (Failure Modes).** Install an executable `hooks/pre-receive` in the bare remote that exits 1. Run `Run` on a dirty clone; assert the returned error is non-nil, the remote HEAD is unchanged, and the local commit survives in the clone's history (the error is reported; the caller continues the loop in prompt 5). No force-push.

   h. **Remote unreachable (Failure Modes).** Point the clone's origin at a nonexistent local path, make a local edit, and run `Run`; assert the returned error is non-nil, `Result.CommitErr` is nil, and the local commit survives (`git log -1 --format=%s` starts with `auto:`).

6. Do not add a logger to `internal/git` or `internal/sync`; the cycle reports failures through return values and the caller logs them.
</requirements>

<constraints>
- Do NOT modify `internal/config`, `internal/logging`, `internal/clock`, `internal/clock/clocktest`, `internal/version`, `internal/cli`, `cmd/`, `Makefile`, `CLAUDE.md`, or any Python file. This prompt adds `internal/git` and `internal/sync` and their tests.
- Do NOT add conflict resolution, marker recovery, path sanitisation, a notifier, metrics, or any Python-side change.
- Do NOT use a shell, `sh -c`, or any interpreter; the only subprocess is `git`, run with `exec.CommandContext`.
- Do NOT use `fmt.Errorf`; wrap with `github.com/bborbe/errors`. Sentinel errors use stdlib `errors` aliased as `stderrors`.
- Do NOT include the word `python` (any case) in any Go source file under `cmd/` or `internal/`.
- Do NOT make tests depend on the network; every repository is created under `t.TempDir()`.
- The scratch fixture must include `/.git-ai-sync.lock` in its `.gitignore` — the lock file is created in the worktree root and would otherwise be staged by a cycle.
- Do NOT add a configurable retry, backoff, force-push, or partial-fetch option.
- `gofmt`, `go vet ./...` and `go test ./... -count=1` must pass. Run `make precommit-go`, not `make precommit`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
go test ./... -count=1
go test ./internal/git/... ./internal/sync/... -count=1
make test-go
make precommit-go
grep -rn 'python' --include='*.go' cmd/ internal/ | wc -l
grep -rn 'exec.Command' internal/ | grep -v '"git"' | wc -l
grep -n 'pull --no-rebase' internal/git/git.go
grep -n 'pull --rebase' internal/git/git.go
grep -n 'PullWhenClean' internal/sync/sync.go
```

Expected: the build, vet, tests and both Make targets exit 0; the Go-source `python` grep prints `0`; the non-`git` exec grep prints `0`; both pull flags appear in `internal/git/git.go`; `PullWhenClean` appears in `internal/sync/sync.go`.

The first `go test`/`make precommit-go` may fetch Go modules over the network; that is expected. The tests themselves use only local repositories.

Before finishing, walk Acceptance Criteria 7, 8 and 9 against the tests: the dirty merge and rebase cycles assert clauses (a)–(f); the pull-only test asserts clauses (d) and (e) fail without a commit; the rebase run reuses the same assertions without commit counts.
</verification>
