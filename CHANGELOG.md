# Changelog

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
