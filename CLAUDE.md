# CLAUDE.md

Automatic Git repository sync with AI-powered conflict resolution (Python/uv).

## Dark Factory Workflow

**Never code directly.** All code changes go through the dark-factory pipeline.

### Complete Flow

**Spec-based (multi-prompt features):**

1. Create spec → `/dark-factory:create-spec`
2. Audit spec → `/dark-factory:audit-spec`
3. User confirms → `dark-factory spec approve <name>`
4. dark-factory auto-generates prompts from spec
5. Audit prompts → `/dark-factory:audit-prompt`
6. User confirms → `dark-factory prompt approve <name>`
7. Start daemon → `dark-factory daemon` (use Bash `run_in_background: true`)
8. dark-factory executes prompts automatically

**Standalone prompts (simple changes):**

1. Create prompt → `/dark-factory:create-prompt`
2. Audit prompt → `/dark-factory:audit-prompt`
3. User confirms → `dark-factory prompt approve <name>`
4. Start daemon → `dark-factory daemon` (use Bash `run_in_background: true`)
5. dark-factory executes prompt automatically

### Assess the change size

| Change | Action |
|--------|--------|
| Simple fix, config change, 1-2 files | Write a prompt → `/dark-factory:create-prompt` |
| Multi-prompt feature, unclear edges, shared interfaces | Write a spec first → `/dark-factory:create-spec` |

### Read the relevant guide before starting — every time, not from memory

- Writing a spec → read [[Dark Factory - Write Spec]] and [[Dark Factory Guide#Specs What Makes a Good Spec]]
- Writing prompts → read [[Dark Factory - Write Prompts]] and [[Dark Factory Guide#Prompts What Makes a Good Prompt]]
- Running prompts → read [[Dark Factory - Run Prompt]]

### Claude Code Commands

| Command | Purpose |
|---------|---------|
| `/dark-factory:create-spec` | Create a spec file interactively |
| `/dark-factory:create-prompt` | Create a prompt file from spec or task description |
| `/dark-factory:audit-spec` | Audit spec against preflight checklist |
| `/dark-factory:audit-prompt` | Audit prompt against Definition of Done |

### CLI Commands

| Command | Purpose |
|---------|---------|
| `dark-factory spec approve <name>` | Approve spec (inbox → queue, triggers prompt generation) |
| `dark-factory prompt approve <name>` | Approve prompt (inbox → queue) |
| `dark-factory daemon` | Start daemon (watches queue, executes prompts) |
| `dark-factory run` | One-shot mode (process all queued, then exit) |
| `dark-factory status` | Show combined status of prompts and specs |
| `dark-factory prompt list` | List all prompts with status |
| `dark-factory spec list` | List all specs with status |
| `dark-factory prompt retry` | Re-queue failed prompts for retry |

### Key rules

- Prompts go to **`prompts/`** (inbox) — never to `prompts/in-progress/` or `prompts/completed/`
- Specs go to **`specs/`** (inbox) — never to `specs/in-progress/` or `specs/completed/`
- Never number filenames — dark-factory assigns numbers on approve
- Never manually edit frontmatter status — use CLI commands above
- Always audit before approving (`/dark-factory:audit-prompt`, `/dark-factory:audit-spec`)
- **BLOCKING: Never run `dark-factory prompt approve`, `dark-factory spec approve`, or `dark-factory daemon` without explicit user confirmation.** Write the prompt/spec, then STOP and ask the user to approve. Do not assume approval from prior context or task momentum.
- **Before starting daemon** — run `dark-factory status` first to check if one is already running. Only start if not running.
- **Start daemon in background** — use Bash tool with `run_in_background: true` (not foreground, not detached with `&`)

## Development Standards

### Build and test

- `make precommit` — format + lint + typecheck + test
- `make test` — tests only
- `make check` — lint + typecheck

### Test conventions

- pytest with pytest-asyncio
- `tests/` directory
- `asyncio_mode = "auto"`

### Dependencies

- `claude-code-sdk` — Claude Code CLI SDK for conflict resolution
- `pydantic` / `pydantic-settings` — configuration
- `watchdog` — filesystem watching
- `ruff` — linting and formatting
- `mypy` — type checking

### Toolchain

- Python 3.14, uv package manager
- `uv sync --all-extras` to install
- `uv run` prefix for all commands

## Architecture

- `src/git_ai_sync/__main__.py` — CLI entry point, subcommands: `watch`, `sync`, `resolve`, `status`, `doctor`, `version`
- `src/git_ai_sync/config.py` — Configuration via pydantic-settings (env vars)
- `src/git_ai_sync/conflict_resolver.py` — AI-powered conflict resolution via Claude Code SDK
- `src/git_ai_sync/file_watcher.py` — Filesystem change tracking with watchdog (debounce gate)
- `src/git_ai_sync/git_operations.py` — Git operations: status, stage, commit, pull --rebase, push, conflict detection
- `src/git_ai_sync/logging_setup.py` — Logging configuration

## Key Design Decisions

- **Watch mode = poll + debounce** — polls at interval, skips sync if files changed recently (active editing)
- **Sync flow:** stage → commit → pull --rebase → push (always rebase, never merge)
- **Conflict resolution via Claude Code SDK** — spawns Claude to resolve merge conflicts
- **All git operations are functions, not methods** — stateless, take `repo_path: Path` as first arg
- **No database** — all state is in git
- **Sequential sync** — no concurrent operations on same repo
