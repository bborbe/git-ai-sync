---
status: approved
approved: "2026-11-10T21:01:06Z"
branch: dark-factory/pin-launchd-plist-to-launch-path
---

## Summary

- `git-ai-sync install` records the path the binary was **launched from** in the launchd plist, not the path that path happens to resolve to today
- A package-manager install puts a stable link on `PATH` and replaces its target on every upgrade; recording the target means the next upgrade deletes the file the plist names, and the agent dies silently on its next restart
- Recording the launch path means the plist survives an upgrade untouched — the link still points at whatever the upgrade installed
- A plain, non-symlinked install is recorded verbatim, so nothing changes for an install that is not a package-manager link
- The regression is locked by a test that fails on today's code and passes after the change

## Problem

`ResolveOptions` fills `Options.BinaryPath` by calling `os.Executable()` and then `filepath.EvalSymlinks` on the result. The resolved path is what `install` writes into `ProgramArguments[0]`, and nothing can override it — `newInstallCommand` passes only the config and the logger, so there is no flag. Launched through a symlink, the plist therefore names the link's target rather than the link. A Homebrew cask installs `/opt/homebrew/bin/git-ai-sync` as a link into `/opt/homebrew/Caskroom/git-ai-sync/<version>/git-ai-sync`; an upgrade repoints the link and removes the old versioned directory. The plist still names the deleted path, and the agent fails on its next restart — silently, until a reboot or a manual reinstall.

The same defect makes the resolution untestable: `os.Executable()` cannot be faked, so no test can assert what `install` records without running a real install.

## Goal

`install` records the path the process was launched from. An install through a stable link keeps working after the link's target is replaced, and a plain install records its own path verbatim. The resolution is a pure function of an executable path, so a test can assert both cases without an install.

## Non-goals

- Converting the `git-ai-sync` cask from the hand-maintained Python-wheel cask to a GoReleaser binary cask. That is a separate repo/tap change, and the real `brew upgrade` end-to-end depends on it.
- The install paths of the other bborbe Go tools managed from the same tap (dark-factory, distill, vault-cli, vault-ui).
- The launchd label, the plist filename, and the `supervise --config` argument list.
- Linux/systemd unit path resolution.
- Any new command-line flag. The override seam that already exists on `Options` is enough.

## Assumptions

- macOS `os.Executable()` already returns an absolute path, so using it verbatim does not produce a relative path in the plist.
- A package-manager install presents the binary as a stable link whose target is a versioned directory, and an upgrade replaces that directory.
- `CHANGELOG.md` currently has no `## Unreleased` heading — its top section is `## v0.18.2` — so the change must create one.
- The operator's installed binary at `~/.local/bin/git-ai-sync` is a real file today, not a symlink. That is why the live readings in AC 9 are non-discriminating and the fixture swap is the real probe.

## Acceptance Criteria

- [ ] The resolution returns the path it was given, for both shapes — evidence: one test `TestResolveBinaryPath` in `internal/launchd/launchd_test.go` covers a symlink case and a plain-path case, and `go test ./internal/launchd -run TestResolveBinaryPath -v -count=1` prints `--- PASS: TestResolveBinaryPath` with both case names. The symlink case **must build its own link** under `t.TempDir()` (`dir/real/x` plus `dir/link → real`) and assert the result is `dir/link/x`, not `dir/real/x`: this discriminates on Linux and macOS alike, which matters because CI runs on `ubuntu-latest`, where `/tmp` is not a symlink. The plain-path case additionally resolves a file under `/tmp` and asserts the result still begins `/tmp/`; that case discriminates on macOS only, where `/tmp` links to `/private/tmp`, and the test states that in a comment rather than implying it holds everywhere. The test must actually create the link: `grep -n 'os.Symlink' internal/launchd/launchd_test.go` returns at least one line, so a case that fakes the shape with a real file — which passes on today's code too — is not accepted as the evidence.
- [ ] The rendered plist carries the supplied path verbatim — evidence: a `RenderPlist` test named `TestRenderPlistKeepsLaunchPath` passes a `BinaryPath` shaped like a symlink and asserts the document contains that exact path as the first `ProgramArguments` entry and does not contain the target. This is the boundary assertion: rendering must not re-resolve.
- [ ] The executable-path resolution no longer resolves symlinks in non-test code, and the stale comment describing it is gone — evidence: `! grep -q 'EvalSymlinks' internal/launchd/launchd.go` and `! grep -q 'symlink-resolved' internal/launchd/launchd.go` both succeed. The second clause is separate because the field comment at `internal/launchd/launchd.go:41` reads `defaults to the symlink-resolved os.Executable()` and the first grep cannot see it.
- [ ] The existing plist-shape test asserts the launch-from path, positively — evidence: `grep -n '"<string>"+executable+"</string>"' internal/cli/lifecycle_test.go` returns at least one line (grep the needle directly — the `requireOrdered` call is multi-line, so grepping the call site returns no needle); `! grep -q 'filepath.EvalSymlinks' internal/cli/lifecycle_test.go` succeeds; and `go test ./internal/cli -count=1` prints `ok`. The positive clause is what stops the assertion being **deleted** to make the package green — a deletion satisfies every negative check while removing the only guard on the recorded path.
- [ ] A caller-supplied `BinaryPath` is still used as given and the resolution is not consulted — evidence: a `ResolveOptions` test with `BinaryPath` set to a symlink path returns it unchanged; `go test ./internal/launchd -run TestResolveOptions -v -count=1` prints `--- PASS` for the `binarypath-override` case.
- [ ] The change ships without regressing the build — evidence: `make precommit-go` exits 0 and `go test ./internal/launchd ./internal/cli -count=1` prints `ok` for both packages.
- [ ] The service doc describes the launch path — evidence: `grep -n 'launch path' docs/launchd-service.md` returns at least one line, and `! grep -q 'symlink-resolved' docs/launchd-service.md` succeeds.
- [ ] The CHANGELOG bullet sits under `## Unreleased` — evidence: `awk '/^## /{s=$0} /launch path/{print s}' CHANGELOG.md` prints `## Unreleased`, and `grep -c '^## Unreleased$' CHANGELOG.md` prints `1`.
- [ ] **Post-Deploy (Rung-2):** an upgrade that replaces a link's target leaves the agent startable and running the new target, and the plist names the launch path — evidence: the operator-executable fixture swap in `# Verification` produces every reading in steps 3-6. The live readings are **not** discriminating: the installed binary is a real file today, not a symlink, so the plist already records it and the process already runs from it.
  - `deploy_check:` `~/.local/bin/git-ai-sync --version`
  - `deploy_target:` `git-ai-sync $(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit-go` — must exit 0. **Not `make precommit`**: that target is Python-only and never compiles a Go file.
- `go test ./internal/launchd ./internal/cli -count=1` — the new resolution tests and the updated lifecycle test.
- `go test ./internal/launchd -run 'TestResolveBinaryPath|TestResolveOptions|TestRenderPlistKeepsLaunchPath' -v -count=1` — must print `--- PASS` with every case name.
- `! grep -q 'EvalSymlinks' internal/launchd/launchd.go` and `! grep -q 'symlink-resolved' internal/launchd/launchd.go` — the resolution and its comment no longer describe symlink resolution.
- `grep -n 'launch path' docs/launchd-service.md` — the doc describes the new behaviour.

### Operator-executable (runs on the host after PR merge)

The fixture swap below, with `TMP=/tmp/pin-probe`, and a binary built from the merge revision:

```bash
mkdir -p $TMP/build && go build \
  -ldflags "-X github.com/bborbe/git-ai-sync/internal/version.Version=$(git describe --tags --abbrev=0)" \
  -o $TMP/build/git-ai-sync ./cmd/git-ai-sync
```

1. Stage the first version: `mkdir -p $TMP/Caskroom/0.18.0 $TMP/bin && cp $TMP/build/git-ai-sync $TMP/Caskroom/0.18.0/git-ai-sync && ln -sfn $TMP/Caskroom/0.18.0/git-ai-sync $TMP/bin/git-ai-sync`. Do not copy the installed binary here — on a pre-change install that is the old build and step 3 fails on correct code.
2. `HOME=$TMP $TMP/bin/git-ai-sync install --config /Users/bborbe/.config/git-ai-sync/config.yaml` — exit 0.
3. `plutil -p $TMP/Library/LaunchAgents/com.github.bborbe.git-ai-sync.plist | grep -A2 ProgramArguments` — prints `0 => "$TMP/bin/git-ai-sync"`, **not** the Caskroom target.
4. Swap the target as a real upgrade does: `mkdir -p $TMP/Caskroom/0.18.1 && cp $TMP/build/git-ai-sync $TMP/Caskroom/0.18.1/git-ai-sync && ln -sfn $TMP/Caskroom/0.18.1/git-ai-sync $TMP/bin/git-ai-sync && rm -rf $TMP/Caskroom/0.18.0`, then `launchctl kickstart -k gui/$(id -u)/com.github.bborbe.git-ai-sync` — exit 0. Creating `0.18.1` **before** the `ln -sfn` is load-bearing: without it the link dangles and step 5 fails on correct code.
5. `PID=$(launchctl list | awk '$3=="com.github.bborbe.git-ai-sync"{print $1}')` is a number, not `-`; `ps -o command= -p "$PID"` prints `$TMP/bin/git-ai-sync supervise --config …`; and `lsof -a -p "$PID" -d txt -Fn | grep '^n/' | sed 's#^n/private/#n/#'` prints `n$TMP/Caskroom/0.18.1/git-ai-sync` — the binary actually mapped, resolved through the link to the **new** target.
6. Restore the live agent: `HOME=/Users/bborbe /Users/bborbe/.local/bin/git-ai-sync install --config /Users/bborbe/.config/git-ai-sync/config.yaml`, then repeat step 5 against the live plist — expect `0 => "/Users/bborbe/.local/bin/git-ai-sync"` and `n/Users/bborbe/.local/bin/git-ai-sync`. A denied or interrupted restore leaves the live agent down; it is reported as a blocker, never as a completed step.

## Desired Behavior

1. `install` writes the path the process was launched from into `ProgramArguments[0]`.
2. The resolution is a pure function of an executable path, callable from a test without a real executable.
3. A path that is not a symlink is returned unchanged — `/tmp/x` stays `/tmp/x`, not `/private/tmp/x`.
4. `Options.BinaryPath` remains the override seam; a non-empty value is used as given and the resolution is not consulted.
5. Rendering never re-resolves: `RenderPlist` writes the supplied path byte-for-byte, and the plist is otherwise unchanged — same keys, same order, same values, same `supervise --config` arguments.

## Constraints

- Change only `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`, `internal/cli/lifecycle_test.go`, `docs/launchd-service.md` and `CHANGELOG.md`. Do not create a new test file; `internal/launchd/launchd_test.go` is where the new tests go.
- No new command-line flag. `Options.BinaryPath` is the only override, and `newInstallCommand` keeps passing config and logger only.
- Do not weaken the existing lifecycle assertion to a substring that would also pass on the resolved value, and do not delete it — it must assert the launch-from path.
- `make precommit-go` is the build gate; `make precommit` is Python-only and proves nothing about Go code.
- Add a `CHANGELOG.md` bullet under `## Unreleased`; create that heading above `## v0.18.2` if it is absent.
- Nothing may touch `.github/workflows/`.

## Security / Abuse Cases

- **Path provenance.** The recorded path comes from `os.Executable()` or from a caller-supplied `Options.BinaryPath`, never from CLI input or a config file. No new flag, no new config key, so no new user-controlled surface reaches the plist.
- **XML escaping.** `RenderPlist` already escapes every value it writes (`escape`), so a path containing `<`, `>`, `&` or a quote cannot break out of the plist's `<string>` element. This change adds no new value to the document — it changes which absolute path fills an existing slot.
- **No privilege change.** `install` writes to `~/Library/LaunchAgents` as the invoking user and requires no `sudo`; recording the launch path does not alter the file's ownership or permissions.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| A caller relied on the resolved path being absolute-real | The launch path is used as given; on macOS `os.Executable()` already returns an absolute path | Reinstall from the real binary path | The plist's `ProgramArguments[0]` | Reversible |
| The launch path itself is deleted by an upgrade | The plist names a path that no longer exists; the agent fails to start | Re-run `install` from the new path, then confirm `launchctl list \| grep com.github.bborbe.git-ai-sync` shows a numeric PID | `launchctl list` shows no PID while the plist exists | Reversible |
| A package manager installs into a directory that is later removed | Same as above | Same as above | Same as above | Reversible |
| `os.Executable()` fails | `install` returns the existing wrapped error, unchanged by this spec | None needed | The command's exit code | Reversible |
| A symlink chain of more than one hop | The path recorded is the one launched from, not the final target | None needed — this is the intent | The plist's `ProgramArguments[0]` | Reversible |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `internal/launchd` resolution: extract the pure helper, drop the symlink resolution and its stale comment, add the two-case and render-passes-through tests plus the override test, and update the `internal/cli` lifecycle assertion | 1, 2, 3, 4, 5 | 1-6 | — |
| 2 | `docs/launchd-service.md` launch-path description and the `## Unreleased` CHANGELOG bullet | — | 7, 8 | 1 |
| 3 | Operator verification: the fixture-swap upgrade check | — | 9 | 1, 2 |

Prompts 1 and 2 must not be split further: dropping the resolution breaks the `internal/cli` lifecycle assertion, so `make precommit-go` cannot pass on a tree where only one of the two packages was updated. That is why prompt 1 spans both packages.

## Do-Nothing Option

The plist keeps naming a versioned directory that the next upgrade deletes. Nothing fails at install time, so the defect is invisible until the agent restarts — a reboot, a logout, or a manual `launchctl` cycle — at which point the sync loop is silently down across every configured vault, with no error anywhere except a launchd log line that nobody reads. It also blocks the operator's stated goal of managing all five Go tools through `brew upgrade`: a tool that dies on upgrade is worse under a package manager than one installed by hand.
