# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## v0.13.0

- feat: Post one operator attention card when a vault's watch loop has failed a configurable number of consecutive cycles, and close it on the next success — a best-effort side channel that never breaks a sync cycle
- feat: Add `GIT_AI_SYNC_STALL_THRESHOLD`, `GIT_AI_SYNC_ATTENTION_SCRIPT`, and `ATTENTION_STORE_URL` settings and pass them through `setup-launchd` to the installed agent, so a launchd-managed vault can configure the consecutive-failure alert
- feat: Count consecutive failed watch cycles and post one operator attention card at the threshold, closing it automatically on the next success — a single isolated failure stays silent, a debounce skip is neutral, and a cycle that exits the process is not counted
- docs: Document the stall-alert settings in `README.md`, extend the launchd plist environment list, and add `docs/attention-store.md` describing the attention-store protocol the alert depends on
- fix: Treat a failing push in the watch loop's ahead-of-remote branch as a cycle failure instead of an unhandled error that ends the process and zeroes the consecutive-failure counter before it can reach the threshold

## v0.12.2

- fix: Classify a pre-commit-hook refusal over a staged path that is not safe on Windows — add `UnsafePathRefusalError`, `is_unsafe_path_refusal()`, `get_blocked_paths()`, and `sanitize_path()` so the offending paths can be renamed to Windows-safe names (recovery wiring lands in a later prompt)
- fix: Auto-recover when the pre-commit hook refuses a commit over a path that is not safe on Windows — rename each offending path to a Windows-safe name, re-stage, and retry up to 3 attempts; on exhaustion unstage the offending paths and raise with every still-offending path named
- fix: Never skip the pull when a commit is refused — in watch and sync, a commit failure (unsafe path or conflict marker) no longer suppresses the pull, so the local checkout can no longer silently fall behind `origin`; sync pulls then exits non-zero only when the refusal was unrecoverable, and watch continues to the next interval
- test: Lock the conflict-marker recovery path against the unsafe-path recovery — `test_commit_with_marker_recovery_marker_path_unchanged` drives a marker refusal through `commit_with_marker_recovery` and asserts the AI resolver runs while `_recover_unsafe_paths` and `get_blocked_paths` are never touched, proving the two refusal classes stay disjoint

## v0.12.1

- docs: Remove the dark-factory approval gate from this repo's `CLAUDE.md`. It contradicted the global `execution-phase-no-reask` carve-out and would have stopped every spec and prompt approval.

## v0.12.0

- feat: Ship a `Dockerfile` so `git-ai-sync` runs as a container — a `node:22-slim` base that already carries the Claude Code CLI the conflict resolver drives, with the Python 3.14 tool installed through `uv` into fixed prefixes outside `HOME` so a pod whose `HOME` is an emptyDir still starts with no download. `entrypoint.sh` runs `watch` and, because `watch` exits(1) on a merge conflict instead of resolving it, runs `resolve` and resumes; it also keeps `.git-ai-sync.lock` out of the repository's history via `.git/info/exclude`, since the lock is written to the repo root and `stage_all` is `git add .`

## v0.11.2

- fix: `remove-launchd` bootouts the agent with the single service-target form `launchctl bootout gui/<uid>/com.github.bborbe.git-ai-sync-<label>` instead of the separate-domain two-arg form (`bootout gui/<uid> <label>`), which real launchd rejects with `Boot-out failed: 5: Input/output error` and left the agent running after a successful-looking remove

## v0.11.1

- fix: `setup-launchd`/`remove-launchd` address launchd services by their full label (`com.github.bborbe.git-ai-sync-<label>`) in the already-loaded print probe, kickstart, bootout, and the printed manual fallback commands — the short label form fails on real launchd (`Could not find service`), which made fresh setup exit 1 and the cask postflight's registration fail silently

## v0.11.0

- feat: Add idempotent `git-ai-sync setup-launchd <vault-dir>` and `git-ai-sync remove-launchd <vault-dir>` subcommands that generate and tear down per-vault launchd agents reproducing the frozen live-plist shape (label derived from the last path component, pushgateway env vars passed through only when set, byte-identical re-runs, tolerant of already-loaded jobs)
- test: Add end-to-end tests for `setup-launchd`/`remove-launchd` — a scripted fake `launchctl` shimmed onto PATH and a scratch HOME drive the real CLI through plist write, bootstrap/print/kickstart/bootout argv, idempotence, already-loaded tolerance, and hard-failure exit codes
- feat: Add a tag-triggered release-wheel workflow that builds the wheel with `uv build --wheel` (full git history for hatch-vcs version derivation) and attaches `git_ai_sync-<ver>-py3-none-any.whl` to the GitHub release, creating the release object when the tag alone exists and refreshing the asset with `--clobber` on a re-run
- docs: Document the `setup-launchd` / `remove-launchd` subcommands and the plist shape they produce in docs/launchd-service.md, replacing the hand-written plist ritual, and point README's background section at the new flow

## v0.10.1

- fix: git-ai-sync Claude conflict resolver skips the SDK's informational rate_limit_event stream message (and any unrecognized message type) instead of aborting resolution with a parse error, so conflict resolution completes and stages the file (claude-code-sdk 0.0.25 has no released fix)

## v0.10.0

- feat: Classify pre-commit-hook refusals in `git_operations` — add `MarkerRefusalError`, `is_marker_refusal()`, and `get_marker_flagged_files()` so commits refused over conflict-marker content are distinguishable from ordinary commit failures (recovery wiring lands in a later prompt)
- feat: Add marker-aware commit recovery to `conflict_resolver` — `resolve_marker_flagged_files()` resolves and re-stages files flagged by the pre-commit hook (stopping early on failure), and `commit_with_marker_recovery()` retries a refused commit with bounded recovery (max 3 attempts; non-marker failures propagate unchanged) so auto-sync recovers from marker refusals instead of wedging
- fix: Auto-recover when the global pre-commit hook refuses a commit over conflict-marker content — detect the refusal signature, resolve flagged files via the existing AI conflict resolver, re-stage, and retry the commit up to 3 attempts; on final failure sync and watch both exit non-zero with the still-flagged files and a `git-ai-sync resolve` pointer, so watch mode no longer loops silently forever on this failure

## v0.9.2

- fix: git-ai-sync pushes the last-success timestamp on every watch cycle that completes without error, including idle no-change cycles, so the sync-stall alert measures "last healthy cycle" (matching the /watch probe) instead of "last push" and does not false-fire on quiet-but-healthy vaults

## v0.9.1

- fix: git-ai-sync pushgateway metrics use POST instead of PUT so heartbeat and last-success timestamps coexist on the gateway (PUT replaced the whole group and erased last-success every cycle, which would have kept the sync-stall alert from ever firing)

## v0.9.0

- feat: `git-ai-sync watch` pushes per-vault heartbeat and last-success timestamps to the Prometheus pushgateway (env `GIT_AI_SYNC_PUSHGATEWAY_URL`/`_USERNAME`/`_PASSWORD`; disabled when unconfigured; failed pushes are logged and never break the sync loop) so a wedged vault is alertable via age-of-last-success while laptop-asleep periods stay silent
- feat: Log output can be written to a rotating file (`RotatingFileHandler`, 5 MB x 5 backups) via `GIT_AI_SYNC_LOG_FILE` so long-running watch services cannot grow logs unbounded

## v0.8.0

- feat: Add `--strategy {merge,rebase}` flag to `watch` and `sync` commands (env `GIT_AI_SYNC_STRATEGY`, default `merge`) so the pull strategy is configurable per-vault; merge default avoids the `git pull --rebase` wedge on redundant-commit detection in autocommit + multi-client vaults

## v0.7.0

- feat: Accept `ANTHROPIC_MODEL` env var on `Config.model` via `AliasChoices` so alt-provider routing (e.g. MiniMax) works without setting `GIT_AI_SYNC_MODEL` — avoids the recognized-Anthropic-model-name trap that would otherwise route to api.anthropic.com despite `ANTHROPIC_BASE_URL`
- feat: `doctor` now prints resolved Claude routing (model + base URL + auth-token presence) and asks the backend to self-identify, turning it into a live alt-provider round-trip test
- feat: `doctor` session test has a 30s timeout so invalid token / unreachable base URL fails fast instead of hanging
- chore: Switch to hatch-vcs for dynamic versioning from git tags
- chore: Mount uv cache for faster CI / local installs
- docs: Document local-checkout install (`uv tool install --force --reinstall .`) and MiniMax alt-provider workflow in README

## v0.6.0

- feat: Wire instance lock into cmd_watch, cmd_sync, and cmd_resolve so only one git-ai-sync instance can operate on a repository at a time; add .git-ai-sync.lock to .gitignore

## v0.5.0

- feat: Add instance_lock module with OS-level flock to prevent concurrent git-ai-sync processes

## v0.4.0

- feat: Push unpushed commits in watch mode when local branch is ahead of remote

## v0.3.4

- chore: verify all tests pass, linting succeeds, and project meets Definition of Done criteria

## v0.3.3

- Fix watch mode failing to pull when local changes exist (commit before pull)

## v0.3.2

### Added
- `doctor` command to verify Claude Code CLI setup and dependencies

## v0.3.1

- Remove unnecessary ANTHROPIC_API_KEY check (SDK uses Claude Code auth automatically)

## v0.3.0

- Add merge conflict support (both rebase and merge conflicts now handled)
- Add `is_in_merge()` function to detect merge state
- Add `is_in_conflict_state()` to check for any conflict type
- Add `continue_merge()` for merge continuation
- Update conflict resolver to dispatch to correct continuation method
- Add 8 new tests for merge state detection

## v0.2.7

- Add comprehensive test suites for config, file_watcher, conflict_resolver, and __main__ (55 new tests)
- Add sync dependency to make test target

## v0.2.6

- Catch specific ClaudeSDKError instead of bare Exception in conflict resolver
- Add How It Works, Prerequisites, and Troubleshooting sections to README
- Update model default in README configuration docs
- Link LICENSE file from README license section

## v0.2.5

- Fix bare except in conflict_resolver catching GitError as resolve failure
- Fix tracker thread leak on non-keyboard exit paths in watch mode
- Fix outdated "not implemented yet" message in pull_rebase error
- Update default Claude model to claude-sonnet-4-5-20250929
- Make continue_rebase sync (was needlessly async)
- Remove dead _last_event field from file watcher

## v0.2.4

- Extract subprocess calls from __main__.py and conflict_resolver into git_operations
- Add git_operations functions: get_head_commit, get_commit_count, get_commit_log, get_changed_files_short, stage_file, get_conflicted_files, continue_rebase
- Add comprehensive git_operations test suite (36 tests)
- Remove remaining emojis from conflict_resolver log messages

## v0.2.3

- Replace all print() with structured logging via logging module
- Extract logging_setup.py module following python-skeleton pattern
- Add AST-based test to prevent print() in production code
- Add logging configuration tests (5 tests)
- Remove emojis from log messages
- Remove duplicate print+logger pairs

## v0.2.2

- Enhance watch mode output with detailed status at each iteration
- Show pulled commits from remote with messages
- Show local file changes with git status
- Always pull from remote even when no local changes

## v0.2.1

- Update README with uvx usage examples from GitHub
- Add Features section explaining auto-sync and debounce-gating
- Add local development usage with --refresh flag

## v0.2.0

- Add full sync command implementation (stage, commit, pull --rebase, push)
- Add AI-powered conflict resolution using Claude SDK
- Add watch mode with filesystem monitoring and debounce-gating
- Add git operations module with all core git functions
- Add configuration management with environment variables
- Add watchdog dependency for filesystem watching
- Improve watch mode logging visibility

## v0.1.0

- Add minimal Python project structure with src/ layout
- Add CLI framework with argparse subcommands (watch, sync, resolve, status, config, version)
- Add pyproject.toml with setuptools build backend
- Add Makefile with sync, test, check, precommit targets
- Add basic test suite with pytest
- Add README, LICENSE (BSD-2-Clause), .gitignore

## v0.0.1

- Initial commit
