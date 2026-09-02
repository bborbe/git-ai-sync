# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

- feat: Classify pre-commit-hook refusals in `git_operations` — add `MarkerRefusalError`, `is_marker_refusal()`, and `get_marker_flagged_files()` so commits refused over conflict-marker content are distinguishable from ordinary commit failures (recovery wiring lands in a later prompt)
- feat: Add marker-aware commit recovery to `conflict_resolver` — `resolve_marker_flagged_files()` resolves and re-stages files flagged by the pre-commit hook (stopping early on failure), and `commit_with_marker_recovery()` retries a refused commit with bounded recovery (max 3 attempts; non-marker failures propagate unchanged) so auto-sync recovers from marker refusals instead of wedging
- chore: Add `fallback-version` to `[tool.hatch.version]` so the package builds without a git repository (needed in the hideGit YOLO container)
- fix: Auto-recover when the global pre-commit hook refuses a commit over conflict-marker content — detect the refusal signature, resolve flagged files via the existing AI conflict resolver, re-stage, and retry the commit up to 3 attempts; on final failure sync and watch both exit non-zero with the still-flagged files and a `git-ai-sync resolve` pointer, so watch mode no longer loops silently forever on this failure

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
