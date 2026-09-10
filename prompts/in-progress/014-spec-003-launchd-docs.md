---
status: approved
spec: [003-setup-launchd-and-brew-cask]
created: "2026-09-10T15:33:07Z"
queued: "2026-09-10T16:55:56Z"
---

# Document setup-launchd and remove-launchd usage

<summary>
- The launchd setup guide no longer walks users through hand-writing a plist — it documents the `setup-launchd` / `remove-launchd` commands
- The plist shape the commands produce is documented: label derivation, watch arguments, log paths, resource limits, pushgateway env passthrough
- Idempotence (re-run safety) and the manual `launchctl bootstrap` / `kickstart` fallback for sandboxed contexts are documented
- README's background section points at the new commands and the guide
- The two Brogrammers KB pages (Git AI Sync, Python Service Brew Cask Pattern) are operator-applied in the vault and are out of scope for this container prompt
</summary>

<objective>
Replace the hand-walked "create a plist by hand" ritual in the repo docs with the tested `setup-launchd` / `remove-launchd` subcommand flow, so onboarding a vault (or a machine) is a documented, reproducible command sequence.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.
Read `docs/dod.md` — the Definition of Done.
Read `docs/launchd-service.md` — the current hand-walked guide this prompt rewrites (prerequisites, manual plist XML, `launchctl load`/`unload` ritual, troubleshooting, log inspection).
Read `README.md` — section `## Run in the Background` (~line 61) where the new commands and a pointer to `docs/launchd-service.md` belong.
Read `src/git_ai_sync/launchd.py` and `src/git_ai_sync/__main__.py` — the subcommands and plist shape shipped by prompt 1 of this spec; document what the code actually does (label derivation, `/tmp/git-ai-sync-<label>.log`, frozen resource limits, pushgateway env passthrough, idempotence).

<!-- OPEN QUESTION (reviewer decision point): the two KB pages (Git AI Sync, Python Service Brew Cask Pattern) in the Brogrammers vault are operator-applied in a host checkout, exactly like the Homebrew cask row of the spec — the container has no vault mount and no credentials for it. This prompt covers only the repo-docs half of spec AC 9; the KB half is verified on the operator rung. -->
</context>

<requirements>
1. Rewrite `docs/launchd-service.md` so the primary install path is the subcommand flow. Required content:
   - a "Per-vault launch agent" section: `git-ai-sync setup-launchd <vault-dir>` creates the agent, `git-ai-sync remove-launchd <vault-dir>` tears it down; both are idempotent (re-running setup is safe and rewrites a byte-identical plist; remove on a never-set-up dir exits 0);
   - the label derivation rule (last path component, lowercased, runs of non-alphanumeric characters collapsed to a single hyphen — `Personal` → `personal`), so users can predict the plist filename `~/Library/LaunchAgents/com.github.bborbe.git-ai-sync-<label>.plist`;
   - the plist shape the command produces (the frozen contract): ProgramArguments `watch <vault-dir> --interval 30 --strategy merge`, KeepAlive + RunAtLoad, stdout/stderr at `/tmp/git-ai-sync-<label>.log`, the frozen soft/hard resource limits (SoftResourceLimits NumberOfFiles 1024 / ResidentSetSize 536870912, HardResourceLimits NumberOfFiles 2048 / ResidentSetSize 1073741824), and `GIT_AI_SYNC_PUSHGATEWAY_*` environment variables passed through only when set;
   - verification: `launchctl print gui/$(id -u)/com.github.bborbe.git-ai-sync-<label>` and `tail -f /tmp/git-ai-sync-<label>.log`;
   - a troubleshooting note for sandboxed/SSH contexts where launchd domain management is unavailable: the command exits non-zero and prints the exact manual `launchctl bootstrap gui/$(id -u) <plist>` and `launchctl kickstart -k gui/$(id -u)/<label>` commands to run;
   - keep the still-valid sections (verify watcher is running, one-shot sync vs service, "service keeps restarting" / wrong-binary-path cause). Remove the hand-written plist XML block and the `launchctl load`/`unload` ritual — they are replaced by the subcommands. The guide must mention `setup-launchd` and `remove-launchd` each at least once.

2. Update `README.md`: in `## Run in the Background`, document the one-command flow — `git-ai-sync setup-launchd <vault-dir>` / `git-ai-sync remove-launchd <vault-dir>` — and point to `docs/launchd-service.md` for the full guide and the plist shape. The README must mention `setup-launchd` at least once (spec AC 9 evidence greps `setup-launchd` across `docs/launchd-service.md` and `README.md`).

3. Update `CHANGELOG.md`: add a flat `docs:` bullet to the `## Unreleased` section (create it between the frozen preamble and `## v0.10.1` if needed):
   ```
   - docs: Document the `setup-launchd` / `remove-launchd` subcommands and the plist shape they produce in docs/launchd-service.md, replacing the hand-written plist ritual, and point README's background section at the new flow
   ```
   Do NOT modify anything above the `# Changelog` title, the preamble, or the `## v0.10.1` section.

4. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk spec AC 9 (repo half) against the change.
</requirements>

<constraints>
- This prompt covers ONLY the repo docs (spec AC 9, repo half). The two Brogrammers KB pages (`Git AI Sync`, `Python Service Brew Cask Pattern`) are operator-applied in a host checkout of the vault — do NOT attempt to locate, edit, or reference local vault paths; leave them to the operator rung of the spec's Verification ladder.
- The documented plist shape must match the frozen live template exactly and what `src/git_ai_sync/launchd.py` actually generates — do not document any tunable interval/strategy knobs for `setup-launchd` (there are none by design).
- Do NOT modify `docs/systemd-user-service.md` (Linux systemd packaging is a spec non-goal) or the tool's Python code.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass (format + lint + typecheck + test; the docs change must not break the gate).
Confirm the docs reference the subcommands (spec AC 9 evidence):
`grep -n 'setup-launchd' docs/launchd-service.md` — line ≥ 1
`grep -n 'remove-launchd' docs/launchd-service.md` — line ≥ 1
`grep -n 'setup-launchd' README.md` — line ≥ 1
Confirm the CHANGELOG entry: `grep -n 'docs:' CHANGELOG.md` — line ≥ 1 under `## Unreleased`.
</verification>
