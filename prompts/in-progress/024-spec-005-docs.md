---
status: approved
spec: [005-consecutive-failure-attention-alert]
created: "2026-10-11T16:26:06Z"
queued: "2026-10-11T18:27:07Z"
---

# Document the stall alert settings and the attention-store contract

<summary>
- The README's environment-variable table lists all three new settings with their defaults
- The README explains what the stall alert does, that a single isolated failure stays silent, and that the notifier is disabled when the script is absent
- The launchd guide's plist environment list now names all six passed-through keys, not just the pushgateway three
- A new page captures the external attention-store protocol the notifier depends on: the close route, the item-id stdout shape, the posting flags the card uses, and the liveness/expiry model
- A reader can now tell from the docs alone which settings exist, what their defaults are, and what the notifier does when the plugin is missing
</summary>

<objective>
Update `README.md` and `docs/launchd-service.md` for the three new settings, and add `docs/attention-store.md` documenting the external attention-store protocol the notifier depends on. Documentation only — no source or test changes.
</objective>

<context>
Read CLAUDE.md for project conventions.

Read `README.md` — the `## Configuration` section's environment-variable table (columns `Variable | Description | Default`; today it lists `GIT_AI_SYNC_INTERVAL`, `GIT_AI_SYNC_MODEL`, `GIT_AI_SYNC_COMMIT_PREFIX`, `GIT_AI_SYNC_STRATEGY`, the three pushgateway variables, and `GIT_AI_SYNC_LOG_FILE`) and the `## Metrics` section (which documents the pushgateway behaviour, including the "without `GIT_AI_SYNC_PUSHGATEWAY_URL` the feature is disabled and makes no network calls" sentence and the one-watcher-per-vault caveat). Add the new table rows and a new `## Stall Alert` section after `## Metrics`.

Read `docs/launchd-service.md` — the `### The plist shape setup-launchd produces` section's final bullet, which today reads: `**EnvironmentVariables**: GIT_AI_SYNC_PUSHGATEWAY_URL, GIT_AI_SYNC_PUSHGATEWAY_USERNAME, GIT_AI_SYNC_PUSHGATEWAY_PASSWORD — each is passed through only if it was set when setup-launchd ran`. Extend that bullet to name all six keys.

Read `src/git_ai_sync/config.py` — the three settings and their exact defaults (the field names are `stall_threshold` with default `10`, `attention_script` with the supervisor plugin's `attention-ask.py` path resolved from the home directory, and `attention_store_url` with default `http://localhost:18080` and NO `GIT_AI_SYNC_` prefix).

Read `src/git_ai_sync/notifier.py` — the card contract this documentation must describe: `dedup_key(vault)` → `git-ai-sync-<vault>-stalled`, `producer_id(vault)` → `git-ai-sync-<vault>`, `PRODUCER_KIND = "cron"`, the `ITEM_ID: <32-hex>` stdout parse, the `POST {store}/api/1.0/attention/{item_id}/close` close route, and the two `--option` labels with `--recommend` naming the actionable one.

Read `docs/launchd-service.md` and `docs/systemd-user-service.md` for the existing documentation tone (short, imperative, no marketing). Do NOT modify `docs/systemd-user-service.md` — it enumerates no environment variables today, and the spec scopes this change to `README.md`, `docs/launchd-service.md`, and the new page.

Read `CHANGELOG.md` — a `## Unreleased` section exists; append a bullet to it, do NOT create a second `## Unreleased`.
</context>

<requirements>
1. In `README.md`, add three rows to the `## Configuration` environment-variable table (keep the existing rows and the table's column shape):

   | Variable | Description | Default |
   |---|---|---|
   | `GIT_AI_SYNC_STALL_THRESHOLD` | Consecutive failed watch cycles before one attention card is posted (must be >= 2) | `10` |
   | `GIT_AI_SYNC_ATTENTION_SCRIPT` | Path to the supervisor's `attention-ask.py` posting script | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/attention-ask.py` |
   | `ATTENTION_STORE_URL` | Attention store base URL (no `GIT_AI_SYNC_` prefix — the same variable the posting script reads) | `http://localhost:18080` |

2. In `README.md`, add a `## Stall Alert` section immediately after the `## Metrics` section. It must cover:
   - what the alert is for: a watch loop that keeps failing while the process stays alive (an unreachable remote, a refused commit) is invisible to liveness checks, and the alert posts one operator card on the attention board once `GIT_AI_SYNC_STALL_THRESHOLD` consecutive cycles have failed;
   - exactly one card is posted per stall, and it is closed automatically on the next successful cycle — a later stall posts a fresh card;
   - a single isolated failure stays silent, and a debounce skip neither counts nor resets;
   - the notifier is best-effort: a missing script, a non-zero exit, a timeout, or an unreachable store is logged as a warning and never breaks or delays a sync cycle;
   - the notifier is **disabled when the script is absent** — one startup warning names the missing path and the sync loop behaves exactly as it does without the feature (there is no enable/disable flag; a missing script path is the disable path);
   - the alert is `watch`-mode only: `git-ai-sync sync`, `status`, and `doctor` are unaffected by the threshold;
   - the store protocol the alert depends on is documented in `docs/attention-store.md` (link it).

3. In `docs/launchd-service.md`, extend the `EnvironmentVariables` bullet in `### The plist shape setup-launchd produces` so it names all six passed-through keys — `GIT_AI_SYNC_PUSHGATEWAY_URL`, `GIT_AI_SYNC_PUSHGATEWAY_USERNAME`, `GIT_AI_SYNC_PUSHGATEWAY_PASSWORD`, `GIT_AI_SYNC_STALL_THRESHOLD`, `GIT_AI_SYNC_ATTENTION_SCRIPT`, `ATTENTION_STORE_URL` — keeping the existing "each is passed through only if it was set when `setup-launchd` ran" semantics and noting that `ATTENTION_STORE_URL` deliberately has no `GIT_AI_SYNC_` prefix because the posting script reads that same name.

4. Create `docs/attention-store.md` — the external protocol `git-ai-sync`'s stall alert depends on, so a future reader can tell what a store must support. Use the tone of the other pages under `docs/` (short, factual, no marketing), a single `# ` title, and cover, at minimum:
   - **Posting.** The card is posted by invoking the supervisor's `attention-ask.py` `post` subcommand as an argument vector (never through a shell). Show the flags the card uses, with the value shape for each: `--dedup-key git-ai-sync-<vault>-stalled`, `--producer-id git-ai-sync-<vault>`, `--producer-kind cron`, `--payload`, `--context`, `--option` (exactly two labels), and `--recommend` (naming one of the two). State that `--liveness-ref` and `--expires-at` are deliberately NOT overridden, so the script's own defaults apply.
   - **Stdout contract.** The script's exit code is the post verdict: `0` means a card was created and its id is on stdout as `ITEM_ID: <id>`; any non-zero exit means no card exists. The id is a 32-character lowercase-hex store id; an id that does not match that shape is treated as unreadable and no close is attempted.
   - **Closing.** The posting script exposes no close verb, so the producer closes its own card with a direct `POST {store}/api/1.0/attention/{item_id}/close` against the same store the post used — no body and no credential against the local default store. The close is bounded and is never retried; the store rejects an illegal transition (a repeat close of an already-closed card) with HTTP 409 rather than treating it as a no-op, and a 409 leaves the card on the board while the producer clears its own guard regardless.
   - **Liveness and expiry.** The default liveness subject is `owner:<producer-id>` — the operator, not the posting session — so the card outlives the process that posted it; and the default `expires_at` (now + the store's TTL, `ATTENTION_ASK_TTL_HOURS`, default 24 h) bounds it, so a store that does not enforce `expires_at` would leak cards. State that an older store that predates the `owner:` liveness model rejects the push (the script exits non-zero and the alert degrades to a warning).
   - **Dedup.** The `--dedup-key` is the store's re-post suppression contract: while a card with that key is open the store suppresses a duplicate, and closing the card releases the key so a later stall posts a new card.
   - **Where the store URL comes from.** `ATTENTION_STORE_URL` (default `http://localhost:18080`) is read by both the posting script and git-ai-sync itself, so a post and its close can never disagree about which store they are talking to. The local default store is unauthenticated; a token-protected store is out of scope.

5. Append this bullet to the existing `## Unreleased` section of `CHANGELOG.md` (do not create a second `## Unreleased`):

   ```
   - docs: Document the stall-alert settings in `README.md`, extend the launchd plist environment list, and add `docs/attention-store.md` describing the attention-store protocol the alert depends on
   ```
</requirements>

<constraints>
- Documentation only: do NOT change any file under `src/` or `tests/`. If a documented behaviour turns out to disagree with the code, document the code's actual behaviour and say so in your final message rather than changing the code.
- Never document a secret: no credential, token, or example remote URL that could carry one appears in any doc you touch.
- Document the exact setting names, defaults, and the disabled-when-absent behaviour — a doc that names a setting the code does not read is a bug.
- Keep the existing table columns and section ordering in `README.md`; add rows and a section, do not restructure the file.
- Do NOT modify `docs/systemd-user-service.md`, `CLAUDE.md`, or the spec file.
- Do NOT invent new settings, flags, or protocol details: every flag, route, and default you write must exist in `src/git_ai_sync/notifier.py`, `src/git_ai_sync/config.py`, or the supervisor's `attention-ask.py` (its `post --help` is the reference for the flag names).
- Do NOT add a CHANGELOG bullet under a new `## Unreleased` heading; append to the existing one.
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + mypy strict + test).
Confirm the three settings are documented in both existing docs:
`grep -n 'GIT_AI_SYNC_STALL_THRESHOLD' README.md docs/launchd-service.md`
`grep -n 'GIT_AI_SYNC_ATTENTION_SCRIPT' README.md docs/launchd-service.md`
`grep -n 'ATTENTION_STORE_URL' README.md docs/launchd-service.md`
Confirm the store contract page covers every required term:
`grep -nE 'ITEM_ID:|/close|--dedup-key|owner:|expires_at' docs/attention-store.md`
Confirm the store contract page also names the remaining card flags:
`grep -nE -- '--producer-id|--producer-kind|--option|--recommend' docs/attention-store.md`
Confirm the README gained the stall-alert section:
`grep -n 'Stall Alert' README.md`
Confirm the launchd guide lists all six passed-through keys:
`grep -n 'GIT_AI_SYNC_PUSHGATEWAY_PASSWORD' docs/launchd-service.md`
Before finishing, re-run `<verification>` and confirm it passes; walk each acceptance criterion in this prompt against the change.
</verification>
