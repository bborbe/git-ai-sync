---
status: completed
spec: [013-pin-launchd-plist-to-launch-path]
summary: Recorded the launch path (identity helper) instead of the symlink-resolved target in launchd ResolveOptions, added launch-path/render/override tests, updated the lifecycle assertion, and added the Unreleased changelog bullet.
execution_id: 5f16a005-33ee-5c79-be8c-829b3e4dc84e-exec-048-spec-013-launch-path-code
dark-factory-version: dev
created: "2026-11-10T21:20:00Z"
queued: "2026-11-10T21:22:57Z"
started: "2026-11-10T21:22:58Z"
completed: "2026-11-10T21:29:39Z"
branch: dark-factory/pin-launchd-plist-to-launch-path
---

# Record the launch path, not the symlink target, in the launchd plist

<summary>
- `git-ai-sync install` records the path the binary was launched from, not the path that path happens to resolve to today.
- An install through a stable package-manager link now survives an upgrade that replaces the link's target.
- A plain, non-linked install records its own absolute path unchanged.
- The path resolution becomes a small pure helper, so both shapes are covered by a unit test without running an install.
- A test builds a real link under a temp directory and proves the link is returned, not its target.
- The plist renderer is proven to write the supplied path verbatim and never to re-resolve it.
- The caller-supplied binary-path override still wins and the resolution is not consulted.
- The install-path test now asserts the launch-from path positively, so the assertion cannot be deleted to make the package green.
- No new command-line flag and no new configuration key.
</summary>

<objective>
Make `launchd.ResolveOptions` record the executable path exactly as `os.Executable()` returns it — the launch path — instead of the path after `filepath.EvalSymlinks`, and lock both shapes (link and plain path) plus the renderer's pass-through with unit tests. **Why:** a package-manager install puts a stable link on `PATH` and repoints it on every upgrade; recording the resolved target means the next upgrade deletes the file the plist names and the agent dies silently on its next restart.
</objective>

<context>
Read the approved spec at `specs/in-progress/013-pin-launchd-plist-to-launch-path.md` — Summary, Problem, Goal, Non-goals, Assumptions, Acceptance Criteria 1-6, Desired Behavior 1-5, Constraints, Security, and every row of the Failure Modes table. The spec's Suggested Decomposition row 1 is this prompt.

Read these files before editing:
- `internal/launchd/launchd.go` — `Options` (the `BinaryPath` field comment reads `defaults to the symlink-resolved os.Executable()`), `ResolveOptions` (the `os.Executable()` block ends with a `filepath.EvalSymlinks` call that must go), and `RenderPlist` (the loop that writes `opts.BinaryPath`, `supervise`, `--config`, `opts.Config.ConfigPath` into `ProgramArguments`). `RenderPlist` must not change.
- `internal/launchd/launchd_test.go` — `testOptions(home)` sets `BinaryPath: "/tmp/bin/git-ai-sync"`; the existing helpers `assertContains`, `assertInOrder`, `assertWellFormedXML` are the style to reuse. The package is `launchd`, so tests call the unexported helper directly.
- `internal/cli/lifecycle_test.go` — `TestInstallWritesPlistAndBootstraps` currently calls `os.Executable()`, then `filepath.EvalSymlinks`, and asserts the resolved value with `requireContains` and `requireOrdered`. That block is the one that must change.
- `internal/cli/cli.go` — `newInstallCommand` passes only `launchd.Options{Config: cfg, Logger: logger}`; it has no binary-path flag and must stay that way.

Read these guides (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`

The module path is `github.com/bborbe/git-ai-sync`. The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`) — **not** `make precommit`, which is Python-only and never compiles a Go file. `go test ./...` also compiles `internal/supervise`, so that package must keep passing unchanged.

<!-- OPEN QUESTION for the reviewer: the spec fixes the test name `TestResolveBinaryPath` but does not name the pure helper it exercises. This prompt names it `resolveBinaryPath(executable string) string`. Confirm the name at audit time. -->
</context>

<requirements>
1. In `internal/launchd/launchd.go`, add the pure helper next to `ResolveOptions`. It is deliberately the identity function: the launch path is the value `os.Executable()` returned, and resolving it is exactly the defect being removed. The comment records why, so a later edit does not reintroduce `EvalSymlinks`:
   ```go
   // resolveBinaryPath returns the launch path: the path the process was
   // launched from. It does not resolve symlinks: a package-manager install
   // presents a stable link whose target a later upgrade replaces, so the
   // recorded path must stay the link.
   func resolveBinaryPath(executable string) string {
       return executable
   }
   ```

2. In `internal/launchd/launchd.go`, replace the defaulting block in `ResolveOptions`:
   ```go
   if opts.BinaryPath == "" {
       executable, err := os.Executable()
       if err != nil {
           return Options{}, errors.Wrapf(ctx, err, "resolve executable path")
       }
       resolved, err := filepath.EvalSymlinks(executable)
       if err != nil {
           return Options{}, errors.Wrapf(ctx, err, "resolve executable symlinks")
       }
       opts.BinaryPath = resolved
   }
   ```
   with:
   ```go
   if opts.BinaryPath == "" {
       executable, err := os.Executable()
       if err != nil {
           return Options{}, errors.Wrapf(ctx, err, "resolve executable path")
       }
       opts.BinaryPath = resolveBinaryPath(executable)
   }
   ```
   The `os.Executable()` failure keeps its existing wrapped error, `resolve executable path`, unchanged (spec Failure Modes row "os.Executable() fails"). The `filepath` import stays — `PlistPath`, `LogDir` and `LogPath` still use it.

3. In `internal/launchd/launchd.go`, update the `Options.BinaryPath` field comment from `// defaults to the symlink-resolved os.Executable()` to:
   ```go
   BinaryPath      string        // defaults to the path os.Executable() was launched from
   ```
   The words `symlink-resolved` must no longer appear anywhere in the file.

4. In `internal/launchd/launchd_test.go`, add `TestResolveBinaryPath` with two subtests. The symlink case must build its own link under `t.TempDir()` — a real `dir/real/x` and a directory link `dir/link -> real` — and assert the result is `dir/link/x`, not `dir/real/x`. The plain-path case creates a real file under `/tmp` and asserts the result still begins `/tmp/`, with a comment stating that this only discriminates on macOS:
   ```go
   func TestResolveBinaryPath(t *testing.T) {
       t.Run("symlink", func(t *testing.T) {
           dir := t.TempDir()
           realDir := filepath.Join(dir, "real")
           if err := os.MkdirAll(realDir, 0o755); err != nil {
               t.Fatalf("create real directory: %v", err)
           }
           realFile := filepath.Join(realDir, "x")
           if err := os.WriteFile(realFile, nil, 0o755); err != nil {
               t.Fatalf("create real file: %v", err)
           }
           link := filepath.Join(dir, "link")
           if err := os.Symlink(realDir, link); err != nil {
               t.Fatalf("create symlink: %v", err)
           }

           launchPath := filepath.Join(link, "x")
           if got := resolveBinaryPath(launchPath); got != launchPath {
               t.Errorf("resolveBinaryPath(%q) = %q, want the launch path %q", launchPath, got, launchPath)
           }
           if got := resolveBinaryPath(launchPath); got == realFile {
               t.Errorf("resolveBinaryPath(%q) = %q, want the link, not its target", launchPath, got)
           }
       })

       t.Run("plain-path", func(t *testing.T) {
           // /tmp is a symlink to /private/tmp on macOS, so this case
           // discriminates there; on Linux (CI runs on ubuntu-latest, where
           // /tmp is a real directory) it only proves the value is returned
           // unchanged.
           file, err := os.CreateTemp("/tmp", "git-ai-sync-launch-path-*")
           if err != nil {
               t.Fatalf("create temp file under /tmp: %v", err)
           }
           name := file.Name()
           if err := file.Close(); err != nil {
               t.Fatalf("close temp file: %v", err)
           }
           defer func() { _ = os.Remove(name) }()

           if got := resolveBinaryPath(name); !strings.HasPrefix(got, "/tmp/") {
               t.Errorf("resolveBinaryPath(%q) = %q, want it to still begin /tmp/", name, got)
           }
       })
   }
   ```
   The link must be created with `os.Symlink`; a case that fakes the shape with a real file passes on today's code and is not accepted. `os`, `path/filepath` and `strings` are already imported in this file.

5. In `internal/launchd/launchd_test.go`, add `TestRenderPlistKeepsLaunchPath`. It passes a `BinaryPath` shaped like a package-manager link and asserts the document contains that exact path as the first `ProgramArguments` entry and does not contain the link's target — the renderer must not re-resolve:
   ```go
   func TestRenderPlistKeepsLaunchPath(t *testing.T) {
       opts := testOptions(t.TempDir())
       launchPath := "/opt/homebrew/bin/git-ai-sync"
       target := "/opt/homebrew/Caskroom/git-ai-sync/0.18.0/git-ai-sync"
       opts.BinaryPath = launchPath

       data, err := RenderPlist(opts)
       if err != nil {
           t.Fatalf("RenderPlist: %v", err)
       }
       document := string(data)

       assertContains(t, document, "<string>"+launchPath+"</string>")
       if strings.Contains(document, target) {
           t.Errorf("plist contains the link target %q instead of only the launch path:\n%s", target, document)
       }
       assertInOrder(t, document,
           "<key>ProgramArguments</key>",
           "<string>"+launchPath+"</string>",
           "<string>supervise</string>",
           "<string>--config</string>",
           "<string>"+opts.Config.ConfigPath+"</string>",
       )
   }
   ```
   Do not change `RenderPlist` itself.

6. In `internal/launchd/launchd_test.go`, add `TestResolveOptions` with the exact subtest name `binarypath-override`. A caller-supplied `BinaryPath` is returned unchanged and the defaulting is not consulted:
   ```go
   func TestResolveOptions(t *testing.T) {
       t.Run("binarypath-override", func(t *testing.T) {
           linkPath := "/opt/homebrew/bin/git-ai-sync"
           opts, err := ResolveOptions(context.Background(), Options{
               HomeDir:    t.TempDir(),
               UID:        501,
               BinaryPath: linkPath,
           })
           if err != nil {
               t.Fatalf("ResolveOptions: %v", err)
           }
           if opts.BinaryPath != linkPath {
               t.Errorf("BinaryPath = %q, want the caller-supplied %q", opts.BinaryPath, linkPath)
           }
       })
   }
   ```
   `HomeDir` and `UID` are set so the test does not depend on the environment; only `BinaryPath` is left for `ResolveOptions` to default, and because it is non-empty it must stay as given.

7. In `internal/cli/lifecycle_test.go`, in `TestInstallWritesPlistAndBootstraps`, replace the executable-resolution block:
   ```go
   executable, err := os.Executable()
   if err != nil {
       t.Fatalf("resolve executable: %v", err)
   }
   resolved, err := filepath.EvalSymlinks(executable)
   if err != nil {
       t.Fatalf("resolve executable symlinks: %v", err)
   }
   requireContains(t, document, "<string>"+resolved+"</string>")
   requireOrdered(t, document,
       "<string>"+resolved+"</string>",
       "<string>supervise</string>",
       "<string>--config</string>",
       "<string>"+configPath+"</string>",
   )
   ```
   with:
   ```go
   executable, err := os.Executable()
   if err != nil {
       t.Fatalf("resolve executable: %v", err)
   }
   requireContains(t, document, "<string>"+executable+"</string>")
   requireOrdered(t, document,
       "<string>"+executable+"</string>",
       "<string>supervise</string>",
       "<string>--config</string>",
       "<string>"+configPath+"</string>",
   )
   ```
   The variable must stay named `executable` and the assertion must stay positive — do not delete it, and do not weaken it to a substring that would also pass on the resolved value. `filepath` is still used elsewhere in the file (`writeConfig`), so its import stays.

8. Do not add any other test, helper, flag, config key or struct field. `RenderPlist` and `newInstallCommand` are unchanged; `Options.BinaryPath` is the only override seam.
</requirements>

<constraints>
- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go` and `internal/cli/lifecycle_test.go`. `docs/launchd-service.md` and `CHANGELOG.md` are handled by a separate prompt — do not edit them here.
- Do not create a new test file; the new tests go in `internal/launchd/launchd_test.go`.
- The executable-path resolution must no longer call `filepath.EvalSymlinks`, and the `symlink-resolved` field comment must be gone. Both `internal/launchd/launchd.go` and `internal/cli/lifecycle_test.go` must be free of `EvalSymlinks`.
- No new command-line flag and no new config key. `newInstallCommand` keeps passing config and logger only.
- `Options.BinaryPath` remains the override seam: a non-empty value is used as given and the resolution is not consulted.
- `RenderPlist` writes the supplied path byte-for-byte; the plist is otherwise unchanged — same keys, same order, same values, same `supervise --config` arguments.
- Do not weaken or delete the existing lifecycle assertion; it must assert the launch-from path positively.
- Use `github.com/bborbe/errors` for wrapping in the remaining `os.Executable()` error path; no `fmt.Errorf` and no bare `return err`.
- Do NOT touch `.github/` or anything under `.github/workflows/` — the CI GitHub App holds no `workflows` permission and any commit there is rejected at push.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run these from the repository root:

```
gofmt -l cmd internal
go vet ./...
go test ./internal/launchd -run 'TestResolveBinaryPath|TestResolveOptions|TestRenderPlistKeepsLaunchPath' -v -count=1
go test ./internal/cli -run TestInstallWritesPlistAndBootstraps -count=1
go test ./internal/launchd ./internal/cli -count=1
make precommit-go
```

Expected: `gofmt -l cmd internal` prints nothing; the verbose test run prints `--- PASS: TestResolveBinaryPath` with both `symlink` and `plain-path`, `--- PASS: TestResolveOptions` with `binarypath-override`, and `--- PASS: TestRenderPlistKeepsLaunchPath`; every `go test` command exits 0; `make precommit-go` exits 0.

Confirm the shape of the change:

```
! grep -q 'EvalSymlinks' internal/launchd/launchd.go
! grep -q 'symlink-resolved' internal/launchd/launchd.go
! grep -q 'filepath.EvalSymlinks' internal/cli/lifecycle_test.go
grep -n 'os.Symlink' internal/launchd/launchd_test.go
grep -n '"<string>"+executable+"</string>"' internal/cli/lifecycle_test.go
grep -n 'launch path' internal/launchd/launchd.go
```

Expected: the three absence checks print nothing; `os.Symlink` appears at least once in the test file; the `"<string>"+executable+"</string>"` needle appears at least once in the lifecycle test; `launch path` appears in the new helper comment.

Before finishing, walk the assigned criteria against the tests:
- `TestResolveBinaryPath/symlink` creates a real link and asserts the link path is returned, not its target; `TestResolveBinaryPath/plain-path` asserts a `/tmp` path is still under `/tmp` (Acceptance Criterion 1);
- `TestRenderPlistKeepsLaunchPath` asserts the launch path is the first `ProgramArguments` entry and the target is absent (Acceptance Criterion 2);
- `EvalSymlinks` and `symlink-resolved` are gone from `internal/launchd/launchd.go` (Acceptance Criterion 3);
- `TestInstallWritesPlistAndBootstraps` asserts `"<string>"+executable+"</string>"` and has no `filepath.EvalSymlinks` (Acceptance Criterion 4);
- `TestResolveOptions/binarypath-override` returns the supplied link path unchanged (Acceptance Criterion 5);
- `make precommit-go` exits 0 and both packages pass (Acceptance Criterion 6).
</verification>
