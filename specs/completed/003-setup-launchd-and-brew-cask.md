---
status: completed
approved: "2026-09-10T15:31:34Z"
generating: "2026-09-10T15:37:26Z"
prompted: "2026-09-10T15:37:26Z"
verifying: "2026-09-10T17:06:32Z"
completed: "2026-09-10T22:22:37Z"
branch: dark-factory/setup-launchd-and-brew-cask
---

## Summary

- New idempotent CLI subcommands `git-ai-sync setup-launchd <vault-dir>` and `git-ai-sync remove-launchd <vault-dir>` generate the per-vault launchd agent (plist + `launchctl` registration) that is today written by hand on every Mac
- A tag-triggered `release-wheel` workflow attaches a `git_ai_sync-<ver>-py3-none-any.whl` wheel to every git-ai-sync GitHub release, so a Homebrew cask has a stable artifact to install
- A `git-ai-sync` cask in `bborbe/homebrew-tap` (with a scheduled auto-bump workflow) installs the tool via `uv tool install --force` and registers the default vault agent (Personal) — `brew install` / `brew upgrade` replaces the per-machine `uv tool install git+...` + hand-written plist ritual
- Docs updated: repo usage docs and the two KB pages (Git AI Sync, Python Service Brew Cask Pattern as consumer #2)
- Scope excludes migrating the 9 existing launchd agents on this Mac — except the acknowledged Personal-plist replacement that the cask verification performs

## Problem

git-ai-sync is installed per machine via `uv tool install git+https://github.com/bborbe/git-ai-sync`, and each vault's watcher is a hand-written launchd plist: this Mac runs 9 of them, each individually authored, with the binary path and pushgateway credentials baked in by hand. The repo ships no wheel on its releases — `ci.yml` runs tests only, and only v0.3.3 has a GitHub release while the installed version is 0.10.1 — so a Homebrew cask has nothing to point at. Onboarding a new machine (or vault) means copying a plist, editing the path, and hoping the label/log paths stay consistent; there is no idempotent, tested, reproducible way to do it.

## Goal

After this work, on any Mac: `brew install bborbe/tap/git-ai-sync` installs the tool and registers the Personal vault agent in one command; `git-ai-sync setup-launchd <vault-dir>` and `git-ai-sync remove-launchd <vault-dir>` create and tear down per-vault agents idempotently, reproducing the exact plist shape the live agents use today; every git-ai-sync release carries a downloadable wheel; and the tap cask tracks releases automatically so `brew upgrade` stays meaningful.

## Non-goals

- Migrating the 9 existing launchd agents on this Mac (or any machine) to the new subcommand flow — the mechanism ships here; per-machine rollout is goal-level work. Acknowledged exception: the cask verification replaces the hand-written `personal` plist (same label) and kickstarts the replacement. The other 8 agents stay untouched.
- Packaging any other tool as a brew cask
- Linux systemd packaging — `docs/systemd-user-service.md` stays as-is
- Changing `watch`/`sync`/conflict-resolution behavior, the CLI's other subcommands, or the plist shape the live agents use
- Do NOT add a launchd-setup opt-out flag to the cask, a configurable interval/strategy for `setup-launchd`, or any tunable threshold — the frozen plist shape is fixed; a future consumer wanting variation gets its own spec

## Acceptance Criteria

- [ ] `make precommit` exits 0 — evidence: exit code (format + lint + typecheck + test)
- [ ] Unit test asserts plist generation: for a fixture vault dir, the generated plist decodes to exactly — Label `com.github.bborbe.git-ai-sync-<label>`, ProgramArguments `[<resolved binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`, `KeepAlive` true, `RunAtLoad` true, `StandardOutPath` == `StandardErrorPath` == `/tmp/git-ai-sync-<label>.log`, `SoftResourceLimits` `{NumberOfFiles: 1024, ResidentSetSize: 536870912}`, `HardResourceLimits` `{NumberOfFiles: 2048, ResidentSetSize: 1073741824}`, and `EnvironmentVariables` carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `GIT_AI_SYNC_PUSHGATEWAY_USERNAME` / `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD` verbatim from the environment (each key present only when set in the environment) — evidence: the generated plist parsed (`plutil -p` on the written file, or the unit test's decoded dict) shows exactly the listed fields (mirrors live plist at `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-obsidian-brogrammers.plist`)
- [ ] Unit test asserts label derivation and idempotence: a vault dir ending in `Personal` derives label `personal`; a dir ending in `Path With.Spaces` derives `path-with-spaces`; the last path component is used, lowercased, every run of non-alphanumeric characters collapsed to a single hyphen. Running setup twice produces a byte-identical plist, and a second bootstrap of an already-loaded job is reported as success (no error) — evidence: the unit test's decoded plist dict per input→label pair and the byte-identical re-run output; the `Path With.Spaces → path-with-spaces` case is ALSO locked at the e2e layer (AC 4 uses a spaced scratch-dir name)
- [ ] E2E test on scratch dirs: with `HOME` pointed at a scratch dir and `launchctl` resolved to a scripted fake (PATH shim), the real CLI run of `setup-launchd <scratch-vault>` writes the plist under `scratch/Library/LaunchAgents/`, invokes `launchctl bootstrap gui/<uid> <plist>` then `launchctl kickstart -k gui/<uid>/<label>`; one scratch vault named `Path With.Spaces` derives label `path-with-spaces` at this layer too; a re-run is identical and exits 0; `remove-launchd <scratch-vault>` invokes `launchctl bootout gui/<uid> <label>` and deletes the plist; `remove-launchd` on a never-set-up dir exits 0 — evidence: pytest e2e plus the fake launchctl invocation log
- [ ] `.github/workflows/release-wheel.yml` exists in git-ai-sync and triggers on `v*` tag pushes, builds the wheel with `uv build --wheel`, and attaches it to the release (create-or-upload) — evidence: `ls .github/workflows/release-wheel.yml` succeeds and `grep -n 'tags:' .github/workflows/release-wheel.yml` returns line ≥1
- [ ] **Operator:** after the next release cut (e.g. v0.11.0), `gh release view v0.11.0 --repo bborbe/git-ai-sync --json assets` lists one asset named `git_ai_sync-0.11.0-py3-none-any.whl` — evidence: gh JSON output
- [ ] **Operator:** `brew install bborbe/tap/git-ai-sync` on the host exits 0; the wheel is staged under `/opt/homebrew/Caskroom/git-ai-sync/<ver>/`; `git-ai-sync version` prints the installed version; `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-personal.plist` exists with ProgramArguments `[<resolved binary>, watch, /Users/bborbe/Documents/Obsidian/Personal, --interval, 30, --strategy, merge]` where `<resolved binary>` is the executable's real path after symlink resolution (per Desired Behavior #1 — `~/.local/bin/git-ai-sync` is a symlink into the uv tool install, so the plist carries the resolved target, e.g. `~/.local/share/uv/tools/git-ai-sync/bin/git-ai-sync`); and `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` reports the job loaded with `state = running`, with `/tmp/git-ai-sync-personal.log` containing a `Starting watch mode` line for the Personal path — evidence: exit code, file presence, launchctl print output, log grep; plus negative evidence: `shasum` of the 8 non-Personal git-ai-sync plists in `~/Library/LaunchAgents/` is byte-identical before and after the install
- [ ] Cask robustness + auto-bump: `Casks/git-ai-sync.rb` in `bborbe/homebrew-tap` contains `must_succeed: false` on every launchctl `system_command` step and its caveats block carries the manual start commands — `git-ai-sync setup-launchd <vault-dir>` (the tool's own idempotent registration, preferred) and `launchctl kickstart -k gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>` (restart an already-loaded agent; the cask installs via `uv tool install --force`, not `launchctl bootstrap`, so the caveats use the tool's own commands rather than a raw bootstrap invocation); `.github/workflows/update-git-ai-sync-cask.yml` exists with a daily schedule; and **operator:** after the next git-ai-sync release, `gh pr list --repo bborbe/homebrew-tap --state open` shows a bump PR titled `git-ai-sync 0.11.0` — evidence: `grep -n 'must_succeed: false' Casks/git-ai-sync.rb` returns ≥1, `grep -n 'schedule' .github/workflows/update-git-ai-sync-cask.yml` returns ≥1, gh PR list output
- [ ] Repo docs updated: `docs/launchd-service.md` or `README.md` documents the `setup-launchd` / `remove-launchd` subcommands and the plist shape they produce — evidence: `grep -n 'setup-launchd' docs/launchd-service.md README.md` returns line ≥1 across the two files; KB pages `Git AI Sync` and `Python Service Brew Cask Pattern` (Brogrammers vault) updated — evidence: the pages' files exist and contain the new install command / consumer reference

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format + lint + typecheck + test suite passes
- `make test` — unit + e2e (fake-launchctl scratch-dir) tests pass
- `ls .github/workflows/release-wheel.yml` — workflow file landed
- `grep -n 'setup-launchd' src/git_ai_sync/` — subcommand registered (any file)
- `grep -n 'remove-launchd' src/git_ai_sync/` — subcommand registered (any file)

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `brew install bborbe/tap/git-ai-sync` — exits 0, wheel staged in Caskroom, plist written for Personal, agent loaded/running (replaces the acknowledged hand-written personal plist); `brew upgrade --cask bborbe/tap/git-ai-sync` — bumps version; `brew uninstall bborbe/tap/git-ai-sync` — plist gone, uv tool removed. Run this FIRST — it installs the new binary (with `setup-launchd`) that the next step exercises
- `git-ai-sync setup-launchd ~/Documents/Obsidian/Personal` twice (idempotence — the agent is already registered by the brew postflight, so this also exercises the already-loaded tolerance), then `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` — job loaded, state running; `tail /tmp/git-ai-sync-personal.log` shows watch cycles; then `git-ai-sync remove-launchd ~/Documents/Obsidian/Personal` and `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` fails (job gone) and the plist file is gone; finally re-run `git-ai-sync setup-launchd ~/Documents/Obsidian/Personal` to restore the Personal agent
- `gh release view v0.11.0 --repo bborbe/git-ai-sync --json assets` — wheel asset listed
- `grep -n 'must_succeed: false' /opt/homebrew/Library/Taps/bborbe/homebrew-tap/Casks/git-ai-sync.rb` — launchd steps are best-effort
- After the next release: `gh pr list --repo bborbe/homebrew-tap --state open` — `git-ai-sync 0.11.0` bump PR open (auto-bump workflow ran)

## Desired Behavior

1. `git-ai-sync setup-launchd <vault-dir>` resolves the vault dir to an absolute path, derives the label from the last path component (lowercased, runs of non-alphanumeric characters collapsed to a single hyphen — `Personal` → `personal`), and resolves the git-ai-sync executable path (PATH lookup, falling back to `~/.local/bin/git-ai-sync`; when invoked via an absolute path not on PATH, that path is used), **following symlinks to the real binary** (e.g. `~/.local/bin/git-ai-sync` → `~/.local/share/uv/tools/git-ai-sync/bin/git-ai-sync` for a `uv tool install`) — so the plist always points at a real, non-symlink binary.
2. `setup-launchd` writes `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-<label>.plist` reproducing the frozen live-plist shape — `watch <vault-dir> --interval 30 --strategy merge`, KeepAlive + RunAtLoad, stdout/stderr to `/tmp/git-ai-sync-<label>.log`, Soft/Hard ResourceLimits matching the live template, and `EnvironmentVariables` passing through the three `GIT_AI_SYNC_PUSHGATEWAY_*` values that are set in the setup environment — then bootstraps the job into the `gui/<uid>` domain and kickstarts it. Idempotent: a re-run leaves the plist byte-identical and succeeds even when the job is already loaded.
3. `git-ai-sync remove-launchd <vault-dir>` bootouts the agent from the `gui/<uid>` domain (best-effort) and deletes the plist. Idempotent: running it when nothing is loaded or no plist exists exits 0.
4. Both subcommands exit non-zero with a message naming the failed step (dir validation, plist write, launchctl) when an action actually fails; the vault dir must exist and be a directory, else setup exits non-zero before writing anything.
5. The tag-triggered `release-wheel` workflow builds the wheel with `uv build --wheel` (full git history so hatch-vcs derives the version from the tag) and attaches `git_ai_sync-<ver>-py3-none-any.whl` to the release, creating the release object if the tag alone exists, refreshing the asset on a re-run of the same tag.
6. The `git-ai-sync` cask installs the wheel via `uv tool install --force <wheel-url>` using the absolute `HOMEBREW_PREFIX/bin/uv` (postflight inherits no shell PATH), then runs `git-ai-sync setup-launchd /Users/bborbe/Documents/Obsidian/Personal`; every launchctl step is best-effort (`must_succeed: false`) so the cask install succeeds even when launchd domain management is unavailable (SSH/sandboxed context); the caveats block carries the manual start commands — `git-ai-sync setup-launchd <vault-dir>` and `launchctl kickstart -k gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>`. Uninstall bootouts the agent and removes the uv tool, both best-effort.
7. The tap's scheduled `update-git-ai-sync-cask` workflow fetches the latest git-ai-sync release, verifies a wheel asset exists (fails loudly otherwise), computes the wheel sha256, and opens a bump PR (`git-ai-sync <ver>`) when the cask version is older — keeping `brew upgrade` meaningful.
8. Usage docs (repo `docs/launchd-service.md` or `README.md`) document the two subcommands and the plist shape they produce, replacing the hand-walked "create a plist by hand" section; the two KB pages are updated to reflect the brew install path and git-ai-sync as consumer #2 of the cask pattern.

## Constraints

- The plist shape is a frozen constraint and must match the live template (`~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-obsidian-brogrammers.plist`): Label format `com.github.bborbe.git-ai-sync-<label>`; ProgramArguments `[<binary>, watch, <vault-dir>, --interval, 30, --strategy, merge]`; KeepAlive true; RunAtLoad true; StandardOutPath == StandardErrorPath == `/tmp/git-ai-sync-<label>.log`; SoftResourceLimits 1024 / 536870912; HardResourceLimits 2048 / 1073741824; EnvironmentVariables carrying `GIT_AI_SYNC_PUSHGATEWAY_URL` / `_USERNAME` / `_PASSWORD` from the setup environment. `watch` and its defaults (`--interval 30 --strategy merge`) must not change.
- The label rule must reproduce `personal` for `/Users/bborbe/Documents/Obsidian/Personal` so the cask's default replaces the existing hand-written plist of the same label cleanly. The rule deliberately does NOT reproduce the other hand-written labels (e.g. `Brogrammers` derives `brogrammers`, not the live `obsidian-brogrammers`) — those agents are out of scope and their differing labels guarantee no accidental takeover.
- Existing CLI subcommands (`watch`, `sync`, `resolve`, `status`, `config`, `version`, `doctor`) and their behavior are unchanged.
- Project DoD: `make precommit` green (ruff + mypy strict, pytest, pytest-asyncio `asyncio_mode = "auto"`), type hints and docstrings on all public functions, functions-over-classes for stateless operations, `subprocess.run` with `check=False` and explicit error handling, no absolute paths in code (all paths via `Path`).
- Wheel artifact name is exactly `git_ai_sync-<ver>-py3-none-any.whl`; release tags are `v<semver>`; hatch-vcs derives the version from git tags, so the build needs full history (`fetch-depth: 0`).
- Cask mirrors `Casks/vault-ui.rb`: `depends_on formula: "uv"`; uv resolved as `File.join(HOMEBREW_PREFIX, "bin", "uv")`; wheel URL `https://github.com/bborbe/git-ai-sync/releases/download/v<ver>/git_ai_sync-<ver>-py3-none-any.whl`; `must_succeed: false` on every launchctl `system_command`; manual start command in caveats.
- Prompt-creator orientation: the two KB pages `Git AI Sync` and `Python Service Brew Cask Pattern` (Brogrammers vault, `50 Knowledge/`), the live plist `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-obsidian-brogrammers.plist`, and the tap's `Casks/vault-ui.rb` mirror are the canonical references; the repo docs `docs/launchd-service.md` is the hand-walked ritual this feature replaces.
- The 9 existing agents on this Mac stay untouched except the acknowledged Personal-plist replacement during cask verification.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---------|-------------------|----------|-----------|---------------|-------------|
| Plist already exists / agent already loaded (re-run setup) | Idempotent: plist rewritten byte-identical; bootstrap of an already-loaded job treated as success; kickstart restarts the job with the new config | None needed | None (success path) | Reversible | Two concurrent setups write identical plists (atomic replace); double bootstrap error tolerated; exactly one job per label (launchd invariant) |
| `launchctl bootstrap` fails (SSH / sandboxed context, no GUI session) | `setup-launchd` exits non-zero with a message naming the manual `launchctl bootstrap`/`kickstart` command; the cask postflight tolerates it (`must_succeed: false`) and install still exits 0 | Operator runs the manual command from the caveats / error message; `launchctl print gui/$(id -u)/<label>` confirms loaded | Non-zero exit from `setup-launchd`; cask installs with no running agent — caveats text names the check | Reversible | Irrelevant — no domain change happened |
| Wheel missing on a release (release-wheel workflow failed or tag pushed before merge) | Cask install fails at `uv tool install` (404 on the wheel URL); auto-bump workflow fails loudly on `exit 1` with a "no wheel asset" message | Re-run the workflow on the tag (`gh workflow run release-wheel.yml` or `gh release upload --clobber`), then re-run `brew install` | `gh release view <tag> --json assets` shows no wheel | Partial — release exists, wheel absent; fixable by upload | Two workflow runs on the same tag: `--clobber` refreshes the asset; last upload wins |
| Brew postflight fails (uv install network error, disk full, brew sees non-zero from setup-launchd) | `brew install` exits non-zero; wheel staged in Caskroom but no tool / no plist | Re-run `brew install` after the cause is fixed; or `uv tool install --force <wheel-url>` + `git-ai-sync setup-launchd ~/Documents/Obsidian/Personal` by hand | Brew error output names the failing step | Reversible (nothing partially registered that a re-run doesn't fix) | Homebrew's cask install lock serializes installs |
| Concurrent `setup-launchd` runs on the same dir | Both write identical plists; second bootstrap error tolerated; one job ends up loaded | None | None — success path | Reversible | See row 1 |
| Crash mid-setup (plist written, bootstrap not run; or bootout done, plist not deleted) | Plist on disk without a loaded job — launchd ignores it until bootstrap; re-running setup completes it. Loaded job without plist (remove crashed) — next `remove-launchd` bootouts (best-effort) and re-run converges | Re-run the same subcommand — both are idempotent | Job loaded with no plist: `launchctl print gui/$(id -u)/<label>` lists it | Partial — no corrupted half-state that a re-run cannot heal | Mid-crash leaves the plist or the job alone; no lock needed |
| Auto-bump workflow hits GitHub API rate limit / transient failure | No bump PR opens for the cycle | Re-run the workflow via `workflow_dispatch` | `gh pr list --repo bborbe/homebrew-tap` has no `git-ai-sync <ver>` PR a day after a release | Reversible | Daily cron — at most one missed cycle |
| Cask installed with an already-running old hand-written agent for the same label (Personal) | `setup-launchd` bootstraps over it (bootstrap of loaded job tolerated) and kickstarts; the new plist (same label, same watch config) takes over | None — intended replacement per DoD | `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-personal` shows the new ProgramArguments | Reversible (`remove-launchd` + reinstall) | Only during the acknowledged Personal migration |

## Security / Abuse Cases

- Vault-dir is user-supplied path input: it must exist and be a directory (else setup exits non-zero before any write); the label is derived from the last path component only, so a path cannot inject directory separators or plist syntax into the Label; non-alphanumeric runs collapse to hyphens, so a hostile dir name cannot smuggle characters into the label.
- Plist content is generated from resolved values (binary path, dir, env values), never from user-supplied XML — no XML injection surface.
- The pushgateway password is written into the generated plist (`EnvironmentVariables`) exactly as today's hand-written plists do; the plist lives in the user-owned `~/Library/LaunchAgents/`, matching current behavior. The tool never logs the password value.
- The cask installs a wheel from a release URL pinned by version + sha256; the auto-bump workflow re-computes the sha256 of the actual asset before updating the cask, so a tampered or mislabeled asset cannot silently land.
- The cask postflight invokes `setup-launchd` and launchctl with the operator's privileges — bounded to writing one plist in `~/Library/LaunchAgents/` and managing one label in the user's GUI domain; `must_succeed: false` prevents a broken agent from failing unrelated install work.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `setup-launchd`/`remove-launchd` subcommands + plist generation + label derivation + executable resolution + unit tests (git-ai-sync repo) | 1-4 | 1-3 | — |
| 2 | E2E tests on scratch dirs with a scripted fake `launchctl` (PATH shim) + scratch HOME | 2-4 | 4 | prompt 1 (exercises the real CLI) |
| 3 | `.github/workflows/release-wheel.yml` tag-triggered wheel attach | 5 | 5 | prompt 1 (tool must build); merge to master before the release cut |
| 4 | `Casks/git-ai-sync.rb` + `.github/workflows/update-git-ai-sync-cask.yml` in bborbe/homebrew-tap — **operator-applied** in a host checkout (see rationale) | 6, 7 | 6-8 | prompt 3 (cask points at the wheel); the release cut (v0.11.0) must have happened before verification |
| 5 | Repo usage docs (`docs/launchd-service.md` / `README.md`) | 8 | 9 (repo half) | prompt 1 (documents the subcommands) |

Rationale: the subcommand is the foundation — everything downstream calls it (cask postflight, e2e, docs). The wheel workflow must land and merge to master before the release cut, and the cask can only be verified once a release carries the wheel. **Cross-repo split:** prompt 4 targets `bborbe/homebrew-tap`, a repo this pipeline's container does not mount and holds no credentials for. It is therefore NOT a container prompt — the operator applies the cask + auto-bump workflow in a host checkout of `bborbe/homebrew-tap` (same hand-applied mechanism as the KB-page updates in prompt 5), and the operator rung verifies it. This must not be generated as a normal dark-factory prompt.

## Do-Nothing Option

Keep installing per machine via `uv tool install git+https://...` and hand-writing each vault's plist. Every new machine and vault repeats the copy-edit-hope cycle, release artifacts stay test-only, and there is no tested, idempotent, reproducible path to a running per-vault watcher. The cost is recurring manual setup with no regression lock — acceptable only if the tool stays single-machine.

## Verification Result

**Verified:** 2026-09-10T22:22:07Z (git-ai-sync HEAD e0dbac3)
**Binary:** /Users/bborbe/.local/bin/git-ai-sync v0.11.2 (uv tool install; resolved for plist to /Users/bborbe/.local/share/uv/tools/git-ai-sync/bin/git-ai-sync)
**Scenario:** Live remove→verify→restore cycle on the deployed Personal agent — setup-launchd re-run tolerated the already-loaded job and rewrote the plist byte-identical; remove-launchd booted out the job (launchctl print then fails: "Could not find service") and deleted the plist; setup-launchd restored it (state = running, fresh pid) and the log gained a new "Starting watch mode" line.
**Evidence:**
- `make precommit` PASS: 226 tests, ruff + mypy clean (test_launchd.py asserts full plist shape incl. GIT_AI_SYNC_PUSHGATEWAY_* pass-through present-only-when-set; test_launchd_e2e.py asserts bootstrap/kickstart/bootout invocation order + spaced-dir label + idempotence)
- plist shasum b58d990d74ae9bd975bc360444e3d5e9156b2652 identical across re-run, remove, and restore; launchctl print gui/501/com.github.bborbe.git-ai-sync-personal: state = running, program = resolved binary, args match `watch <Personal> --interval 30 --strategy merge`
- 8 non-Personal plists byte-identical before/after install and after the full cycle (shasums match)
- `gh release view v0.11.0 --repo bborbe/git-ai-sync --json assets`: one asset git_ai_sync-0.11.0-py3-none-any.whl
- homebrew-tap: Casks/git-ai-sync.rb (v0.11.2) with must_succeed: false x3, caveats carry setup-launchd + kickstart (no bootstrap literal); auto-bump proven by merged PRs `git-ai-sync 0.11.1` / `git-ai-sync 0.11.2` (cask first landed at 0.11.0, so no 0.11.0 bump PR existed)
**Verdict:** PASS
