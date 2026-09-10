---
status: completed
spec: [003-setup-launchd-and-brew-cask]
summary: 'Added a tag-triggered release-wheel GitHub Actions workflow (uv build --wheel with fetch-depth: 0, gh release create-or-upload with --clobber) and a feat: CHANGELOG bullet under ## Unreleased'
execution_id: git-ai-sync-exec-013-spec-003-release-wheel-workflow
dark-factory-version: dev
created: "2026-09-10T15:33:07Z"
queued: "2026-09-10T16:55:56Z"
started: "2026-09-10T17:04:56Z"
completed: "2026-09-10T17:05:16Z"
---

# Tag-triggered release-wheel workflow

<summary>
- A new GitHub Actions workflow runs on `v*` tag pushes and builds the git-ai-sync wheel with `uv build --wheel`
- The checkout fetches full history so hatch-vcs derives the version from the tag (required — the version is dynamic)
- The wheel `git_ai_sync-<ver>-py3-none-any.whl` is attached to the GitHub release for that tag, creating the release object when only the tag exists
- Re-running the workflow on the same tag refreshes the asset (`--clobber`), so a failed or missing upload is recoverable by re-running the workflow
- If the built wheel does not match the expected `<tag-version>` name, the workflow fails loudly instead of uploading a misnamed artifact
</summary>

<objective>
Give every git-ai-sync release a downloadable wheel artifact — the stable URL the Homebrew cask (applied separately by the operator) points at — so `brew install` and `brew upgrade` have something to install.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the `## Unreleased` convention.
Read `.github/workflows/ci.yml` — the existing workflow style to mirror (name/on/jobs layout, `actions/checkout@v4`, `actions/setup-python@v5`, `pip install uv`).
Read `pyproject.toml` — build-system `hatchling` + `hatch-vcs`, `dynamic = ["version"]`, `[tool.hatch.version] source = "vcs"`, wheel package `src/git_ai_sync`. The wheel `uv build --wheel` produces is named `git_ai_sync-<ver>-py3-none-any.whl` with `<ver>` derived from the git tag (the `fallback-version` in pyproject.toml only applies when git is unavailable).
Read `CHANGELOG.md` — the frozen preamble ends at the `* PATCH version when you make backwards-compatible bug fixes.` line; the highest released section is `## v0.10.1`.
</context>

<requirements>
1. Create `.github/workflows/release-wheel.yml`. It must:
   - have `name: Release Wheel`;
   - trigger only on tag pushes matching `v*`: `on: push: tags: ["v*"]`;
   - set `permissions: contents: write` (required to create a release and upload an asset);
   - use `actions/checkout@v4` with `fetch-depth: 0` (full history — REQUIRED so hatch-vcs derives the version from the tag; a shallow checkout would fall back to the hardcoded version);
   - use `actions/setup-python@v5` (python-version `3.x`, mirroring ci.yml) and `pip install uv` (mirroring ci.yml — no new setup action);
   - run `uv build --wheel` (creates `dist/git_ai_sync-<ver>-py3-none-any.whl`); do NOT run `uv sync` first — the build backend is isolated and does not need the project deps installed;
   - attach the wheel to the release for the tag:
     1. `wheel_file="dist/git_ai_sync-${GITHUB_REF_NAME#v}-py3-none-any.whl"` (version from the tag, `v` stripped);
     2. fail loudly if the file does not exist: `test -f "$wheel_file"` (catches a build that produced a different name);
     3. create the release object only when the tag alone exists: `gh release view "$GITHUB_REF_NAME" >/dev/null 2>&1 || gh release create "$GITHUB_REF_NAME" --title "$GITHUB_REF_NAME" --generate-notes`;
     4. upload with `gh release upload "$GITHUB_REF_NAME" "$wheel_file" --clobber` — `--clobber` makes a re-run on the same tag refresh the asset (spec failure-mode row "Wheel missing on a release": recovery is re-running the workflow or `gh release upload --clobber`; last upload wins);
   - provide `GH_TOKEN: ${{ github.token }}` in the upload step's `env`.

2. Update `CHANGELOG.md`: add a flat `feat:` bullet to the `## Unreleased` section (create it between the frozen preamble and `## v0.10.1` if a previous prompt has not already):
   ```
   - feat: Add a tag-triggered release-wheel workflow that builds the wheel with `uv build --wheel` (full git history for hatch-vcs version derivation) and attaches `git_ai_sync-<ver>-py3-none-any.whl` to the GitHub release, creating the release object when the tag alone exists and refreshing the asset with `--clobber` on a re-run
   ```
   Do NOT modify anything above the `# Changelog` title, the preamble, or the `## v0.10.1` section.

3. Self-check before finishing: re-run the `<verification>` commands below and confirm they pass; walk spec AC 5 against the workflow.
</requirements>

<constraints>
- Wheel artifact name is exactly `git_ai_sync-<ver>-py3-none-any.whl`; release tags are `v<semver>`; hatch-vcs derives the version from git tags, so the build needs full history (`fetch-depth: 0`).
- Attach via create-or-upload semantics: create the release object only if the tag alone exists; `--clobber` refresh on a re-run of the same tag (last upload wins).
- Do NOT add a cron schedule, release-drafter, or any other automation to this workflow — the spec scopes it to tag-triggered wheel attach only.
- Do NOT modify `.github/workflows/ci.yml` or any existing workflow.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (`make precommit` — this change does not touch Python, but the gate must stay green).
</constraints>

<verification>
Run `make precommit` — must pass (the Python gate stays green).
Confirm the workflow file exists and is wired:
`ls .github/workflows/release-wheel.yml`
`grep -n 'tags:' .github/workflows/release-wheel.yml` — must return line ≥ 1
`grep -n 'clobber' .github/workflows/release-wheel.yml` — must return line ≥ 1
`grep -n 'fetch-depth: 0' .github/workflows/release-wheel.yml` — must return line ≥ 1
Confirm the CHANGELOG entry: `grep -n 'release-wheel' CHANGELOG.md` — must return line ≥ 1 under `## Unreleased`.
</verification>
