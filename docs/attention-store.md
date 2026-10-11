# The attention-store protocol

`git-ai-sync`'s stall alert posts and closes operator cards on an external attention store through the supervisor's `attention-ask.py` script. This page records the protocol the alert depends on, so a future reader can tell what a store must support. It is the contract behind the [stall alert](../README.md#stall-alert) — nothing here is configurable beyond the store URL.

## Posting

A card is posted by invoking `attention-ask.py`'s `post` subcommand as an argument vector — never through a shell. The card sets these flags:

- `--dedup-key git-ai-sync-<vault>-stalled` — the re-post suppression key for one vault's stall.
- `--producer-id git-ai-sync-<vault>` — the explicit producer id for the card.
- `--producer-kind cron` — the producer kind.
- `--payload` — a one-line summary naming the vault and the number of consecutive failed cycles.
- `--context` — the repository path, the time of the first failure in the run, and the last error (URL-shaped runs are redacted before truncation, so no credential reaches a card).
- `--option` — exactly two labels: one actionable, one deferring.
- `--recommend` — names the actionable label.

`--liveness-ref` and `--expires-at` are deliberately NOT overridden, so the script's own defaults apply.

## Stdout contract

The script's exit code is the post verdict: `0` means a card was created and its id is on stdout as `ITEM_ID: <id>`; any non-zero exit means no card exists. The id is a 32-character lowercase-hex store id. An id that does not match that shape is treated as unreadable and no close is attempted.

## Closing

The posting script exposes no close verb, so the producer closes its own card with a direct `POST {store}/api/1.0/attention/{item_id}/close` against the same store the post used — no body and no credential against the local default store. The close is bounded and is never retried.

The store rejects an illegal transition — a repeat close of an already-closed card — with HTTP 409 rather than treating it as a no-op. A 409 leaves the card on the board while the producer clears its own guard regardless.

## Liveness and expiry

The default liveness subject is `owner:<producer-id>` — the operator, not the posting session — so the card outlives the process that posted it. The default `expires_at` (now + the store's TTL, `ATTENTION_ASK_TTL_HOURS`, default 24 h) bounds it, so a store that does not enforce `expires_at` would leak cards.

An older store that predates the `owner:` liveness model rejects the push: the script exits non-zero and the alert degrades to a warning.

## Dedup

The `--dedup-key` is the store's re-post suppression contract: while a card with that key is open the store suppresses a duplicate, and closing the card releases the key so a later stall posts a new card.

## Where the store URL comes from

`ATTENTION_STORE_URL` (default `http://localhost:18080`) is read by both the posting script and `git-ai-sync` itself, so a post and its close can never disagree about which store they are talking to. The local default store is unauthenticated; a token-protected store is out of scope.
