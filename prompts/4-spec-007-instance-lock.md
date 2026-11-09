---
status: draft
spec: [007-go-sync-loop-prototype]
created: "2026-11-09T18:54:14Z"
branch: dark-factory/go-sync-loop-prototype
---

# Implement the instance lock

<summary>
- A second process on the same repository refuses to start and names the process that holds the lock.
- The lock is an exclusive, non-blocking file lock in the repository root.
- The lock file is created or corrected to owner-only permissions on every acquisition.
- The lock file holds the holder's process ID.
- A crashed process leaves no stale lock; the kernel releases it.
- The lock is released on a clean shutdown.
</summary>

<objective>
Implement `internal/lock` with an exclusive non-blocking `flock` on `<repo>/.git-ai-sync.lock`, mode 0600 with correction, PID contents, and a refusal error naming the holder; add tests for acquisition, contention, release, a stale file, and mode correction. **Why:** this is the single-instance contract the port must preserve, and the watch loop in prompt 5 acquires it.
</objective>

<context>
Read the approved spec at `specs/in-progress/007-go-sync-loop-prototype.md` — Desired Behavior 7, Acceptance Criteria 16 and 17, and the Security section are the contract for this prompt. Read `specs/in-progress/001-instance-locking.md` for the settled lock contract.

Read `src/git_ai_sync/instance_lock.py` for the exact semantics: `open(..., "a+b")`, `os.chmod(..., 0o600)`, `fcntl.flock(LOCK_EX | LOCK_NB)`, PID contents, and the refusal message.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — use `github.com/bborbe/errors`; sentinel errors use stdlib `errors` aliased as `stderrors`.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the test conventions, but use stdlib `testing` (not Ginkgo/Gomega).

The module path is `github.com/bborbe/git-ai-sync`. Use `syscall.Flock` (available on Linux and macOS) with `syscall.LOCK_EX|syscall.LOCK_NB` and `syscall.LOCK_UN`. Do not add a shell or a subprocess. Do not include the word `python` (any case) in any Go source file under `cmd/` or `internal/`.
</context>

<requirements>
1. Create `internal/lock/lock.go` with exactly this public surface (exported identifiers documented):

   ```go
   package lock

   import (
       "context"
       "os"

       stderrors "errors"
   )

   // FileName is the lock file name in the repository root.
   const FileName = ".git-ai-sync.lock"

   // ErrLocked reports that another instance already holds the repository lock.
   var ErrLocked = stderrors.New("another instance is already running")

   // Lock is a held repository lock.
   type Lock struct {
       // unexported fields
   }

   // Acquire takes an exclusive non-blocking lock on <repoRoot>/.git-ai-sync.lock,
   // corrects the file mode to 0600, and writes the current PID. A second acquisition
   // on the same repository returns an error wrapping ErrLocked and, when readable,
   // naming the holder's PID.
   func Acquire(ctx context.Context, repoRoot string) (*Lock, error)

   // Release unlocks and closes the lock file. It is safe to call more than once.
   func (l *Lock) Release(ctx context.Context) error
   ```

   Implementation rules:
   - `path := filepath.Join(repoRoot, FileName)`.
   - Open with `os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)`.
   - Call `os.Chmod(path, 0o600)` on every acquisition, even when the file already existed, so a pre-existing looser mode is tightened. Wrap a chmod failure and close the file.
   - Call `syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)`. On `syscall.EWOULDBLOCK` or `syscall.EAGAIN` (check with `stderrors.Is`; on Linux the two constants are equal), read the file contents (seek to 0, read all, trim space), close the file, and return `errors.Wrapf(ctx, ErrLocked, "another instance is already running (pid %s)", pid)` when a PID was read, or `errors.Wrapf(ctx, ErrLocked, "another instance is already running")` when it was empty. The returned error must satisfy `stderrors.Is(err, ErrLocked)` and contain the holder's PID when readable.
   - On any other flock error, close the file and return a wrapped error.
   - On success, truncate the file to zero, seek to 0, write `fmt.Sprintf("%d", os.Getpid())`, and sync. Wrap each failure and close the file.
   - `Release`: if the receiver or its file is nil, return nil. Call `syscall.Flock(fd, syscall.LOCK_UN)`, then close the file, set the file to nil, and return the first error encountered (wrapped with `errors.Wrap(ctx, ...)`). Calling `Release` twice is safe.
   - Do not use a lock-presence check; the exclusion is `flock`. Do not add a timeout, a takeover, or a retry.

2. Create `internal/lock/lock_test.go` covering:
   - `TestAcquireWritesPID`: in `t.TempDir()`, `Acquire`, read the lock file, assert its contents equal `strconv.Itoa(os.Getpid())`; `Release`.
   - `TestAcquireCorrectsModeTo0600`: pre-create `<dir>/.git-ai-sync.lock` with mode `0644`, `Acquire`, assert `os.Stat(...).Mode().Perm() == 0o600`; `Release`.
   - `TestSecondAcquireRefused`: `Acquire` once, then `Acquire` again on the same directory; assert the second returns an error, `stderrors.Is(err, ErrLocked)` is true, and `err.Error()` contains the current PID. Release the first lock and assert a third `Acquire` succeeds.
   - `TestStaleFileDoesNotBlock`: pre-create the lock file with arbitrary contents but hold no flock; `Acquire` succeeds and overwrites the contents with the current PID; `Release`.
   - `TestReleaseAllowsReacquire`: `Acquire`, `Release`, `Acquire` again; success.
   - `TestReleaseIsIdempotent`: call `Release` twice; no panic and no error.

   Use `t.TempDir()`; each test uses its own directory. Do not run lock tests in parallel on the same directory.
</requirements>

<constraints>
- Do NOT modify `internal/config`, `internal/logging`, `internal/clock`, `internal/clock/clocktest`, `internal/version`, `internal/cli`, `cmd/`, `internal/git`, `internal/sync`, `internal/quietwindow`, `internal/changetracker`, `Makefile`, `CLAUDE.md`, or any Python file. This prompt adds `internal/lock` and its tests.
- Do NOT write to `.git/`; the lock file is at the repository root.
- Do NOT use a lock-presence check, a timeout, an auto-expiry, or a takeover mechanism; the exclusion is `flock`.
- Do NOT invoke a shell, an interpreter, or any subprocess.
- Do NOT use `fmt.Errorf`; wrap with `github.com/bborbe/errors`. Sentinel errors use stdlib `errors` aliased as `stderrors`.
- Do NOT include the word `python` (any case) in any Go source file under `cmd/` or `internal/`.
- `gofmt`, `go vet ./...` and `go test ./... -count=1` must pass. Run `make precommit-go`, not `make precommit`.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run these from the repository root:

```
go build ./...
go vet ./...
go test ./internal/lock/... -count=1 -v
go test ./... -count=1
make test-go
make precommit-go
grep -n 'LOCK_EX\|LOCK_NB\|LOCK_UN' internal/lock/lock.go
grep -n '0o600\|0600' internal/lock/lock.go
grep -n 'ErrLocked' internal/lock/lock.go
grep -rn 'exec.Command' internal/lock/ | wc -l
grep -rn 'python' --include='*.go' cmd/ internal/ | wc -l
```

Expected: the build, vet, tests and both Make targets exit 0; the flock constants, the mode and `ErrLocked` appear; the lock package runs no subprocess (`0`); the Go-source `python` grep prints `0`.

Before finishing, walk Acceptance Criteria 16 and 17 against the tests: a second acquisition is refused non-blockingly with the holder's PID; the lock file is corrected to 0600 even when it pre-existed at a looser mode; and a stale file does not block a new acquisition.
</verification>
