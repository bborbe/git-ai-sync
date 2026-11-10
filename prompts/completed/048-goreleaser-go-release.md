---
status: completed
summary: 'Added a GoReleaser v2 config building darwin/linux amd64/arm64 archives with the tag injected into internal/version.Version, plus a keep-existing release with replace_existing_artifacts and a Homebrew cask, and rewrote the Makefile install/run/test/format/lint/check/precommit/clean targets to Go while retiring the Python toolchain from docs/dod.md and CLAUDE.md and adding one Unreleased CHANGELOG bullet. Notes: this config requires GoReleaser >= v2.19 (formats is v2.6+, homebrew_casks v2.10, hooks.post.install_steps replaced the deprecated Ruby hook in v2.19) and is inert until an operator lands the workflow, which must run after the Release is created; the ci.yml test job still runs make test/make check on a Python runner and must be replaced by the operator before merge (no .github/ edit made, per the workflows-permission constraint); the Python suite under tests/ is retired from the gate but not deleted, and src/, pyproject.toml and uv.lock stay for the Data Assistant image; the generated cask no longer registers the launchd agent that the hand-maintained tap cask did, so re-registration is tracked by the separate launchd task and the tap''s update-git-ai-sync-cask.yml must be retired in the same rollout.'
execution_id: git-ai-sync-goreleaser-exec-048-goreleaser-go-release
dark-factory-version: v0.198.17
created: "2026-11-10T21:21:37Z"
queued: "2026-11-10T21:21:37Z"
started: "2026-11-10T21:23:07Z"
completed: "2026-11-10T21:24:49Z"
---

# Add a GoReleaser config for darwin/linux Go binary archives and retire the Python build targets

<summary>
- The repository carries a GoReleaser config that can publish darwin/linux binary archives for amd64 and arm64 once a release workflow is added, alongside the Python wheel the release already carries.
- Each archive's binary carries the release tag as its version string, so `--version` reports the release instead of `dev`.
- Archives are named `<project>_<os>_<arch>.tar.gz` beside a `checksums.txt`, matching the other Go tools on this machine.
- A retried release re-uploads its assets instead of dying with `already exists`.
- A generated Homebrew cask for the tap is described in the same config, so the cask stops being hand-maintained.
- The Makefile's install, run, test, format, lint, check and precommit targets build and check the Go code instead of Python.
- The Makefile's help output no longer advertises a dependency sync that no longer exists.
- The project's own definition of done and build conventions describe the Go toolchain instead of ruff, mypy and pytest.
- The Python source tree is untouched; only the new release config, the build targets, the help text and the project docs change.
</summary>

<objective>
Add a GoReleaser v2 config that builds and archives the CLI for darwin and linux on amd64 and arm64 with the release tag injected into the version variable, and rewrite the Makefile's Python targets to their Go equivalents, so this repository can publish an installable binary. **Why:** the binary now serving the vaults exists only as a local build — no binary artifact in any release is reproducible, so there is no `brew`-installable path and nothing to upgrade.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these files before editing:
- `Makefile` — the Python targets are `install`, `sync`, `run`, `test`, `precommit`, `format`, `lint`, `typecheck`, `check`, plus the `help` block and the `clean` recipe's cache removals. The Go targets `test-go` and `precommit-go` already exist near the bottom of the file, under their own `.PHONY` line, and must not change.
- `internal/version/version.go` — `var Version = "dev"` is the ldflags target. Its package path is `github.com/bborbe/git-ai-sync/internal/version`.
- `internal/cli/cli.go` — `rootCmd.SetVersionTemplate("git-ai-sync {{.Version}}\n")` is what renders `--version`; do not change it.
- `cmd/git-ai-sync/main.go` — the CLI entry point, i.e. the GoReleaser `main` path.
- `CHANGELOG.md` — the most recent section is `## v0.18.2`; there is no `## Unreleased` heading today.
- `docs/dod.md` — this is the `validationPrompt` named in `.dark-factory.yaml`, so it is the checklist the completion report is graded against.
- `.github/workflows/ci.yml` — read it but do not edit it. Its `test` job runs `make test` and `make check` on a Python/uv runner with no Go toolchain, so after requirement 2 those two targets resolve to Go commands that runner cannot run.

There is **no** in-repo `.goreleaser.yaml` and no sibling config reachable from this container, so the required YAML shape is inlined in requirement 1. Do not go looking for an exemplar file — it is not mounted.

The container gate is `make precommit-go` (`gofmt -l cmd internal`, `go vet ./...`, `go test ./... -count=1`). `make precommit` is Python-only until requirement 2 lands, so do not rely on it before then; after requirement 2 it resolves to `precommit-go` and must also pass.
</context>

<requirements>
1. Create `.goreleaser.yaml` at the repository root with exactly this content:

   ```yaml
   version: 2

   project_name: git-ai-sync

   builds:
     - id: git-ai-sync
       binary: git-ai-sync
       main: ./cmd/git-ai-sync
       env:
         - CGO_ENABLED=0
       goos:
         - darwin
         - linux
       goarch:
         - amd64
         - arm64
       ldflags:
         - -s -w
         - -X github.com/bborbe/git-ai-sync/internal/version.Version={{ .Tag }}

   archives:
     - id: git-ai-sync
       formats: [tar.gz]
       name_template: "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"

   checksum:
     name_template: "checksums.txt"

   release:
     github:
       owner: bborbe
       name: git-ai-sync

     # `mode` governs the release NOTES only — it has nothing to do with artifacts.
     # This config is inert until a GoReleaser workflow lands (`.github/` is out of
     # scope here). That workflow must run after the Release is created, because
     # `release-wheel.yml` creates it on tag push; `keep-existing` then preserves
     # whatever notes that step wrote instead of stacking GoReleaser's commit list
     # underneath on every run.
     mode: keep-existing

     # Retry safety. Assets are uploaded before the cask step, so a run that fails
     # at the cask leaves them behind and the next attempt dies with
     # `422 Validation Failed [Code:already_exists]`. THIS is the artifact-level
     # setting; `mode` is not.
     replace_existing_artifacts: true

   homebrew_casks:
     - name: git-ai-sync
       description: "Automatic Git repository sync"
       homepage: "https://github.com/bborbe/git-ai-sync"
       repository:
         owner: bborbe
         name: homebrew-tap
         token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
       commit_author:
         name: goreleaser-bot
         email: goreleaser@benjamin-borbe.de
       # The binary is unsigned; strip the download quarantine so it runs without a
       # Gatekeeper "unidentified developer" prompt on first use.
       hooks:
         post:
           install_steps: |
             /usr/bin/xattr -dr com.apple.quarantine "#{staged_path}/git-ai-sync"
   ```

   Do not add a `before` hook, a `snapshot` block, `changelog`, `dockers`, `nfpms`, `brews`, or a `universal_binaries` entry. Do not change the ldflags package path — `internal/version.Version` is the only symbol that exists.

   This config requires GoReleaser **>= v2.19**: `archives[].formats` is the v2.6+ spelling, `homebrew_casks` landed in v2.10, and `hooks.post.install_steps` replaced the raw-Ruby `hooks.post.install` hook that v2.19 deprecated. The operator pins that floor when the workflow lands and confirms it with `goreleaser check` against that pinned version; state the floor in the completion report.

2. Rewrite the Makefile's Python targets. Keep every other target (`build`, `upload`, `clean-image`, `buca`, `test-go`, `precommit-go`) and every variable block byte-identical.

   - The first `.PHONY` line becomes:
     ```make
     .PHONY: help install run test check precommit format lint clean build upload buca
     ```
     — `sync` and `typecheck` are removed from it. Do **not** add `test-go` or `precommit-go` here; they already carry their own `.PHONY` line further down.
   - The `help` recipe becomes:
     ```make
     help:
     	@echo "Available targets:"
     	@echo "  install    - Install the git-ai-sync binary into GOBIN"
     	@echo "  run        - Run git-ai-sync from source"
     	@echo "  test       - Run Go tests"
     	@echo "  check      - Run go vet"
     	@echo "  precommit  - Run all precommit checks"
     	@echo "  clean      - Clean build artifacts"
     ```
   - `install: sync` becomes `install:` with the recipe `go install ./cmd/git-ai-sync`.
   - Delete the `sync` target entirely.
   - `run` becomes `go run ./cmd/git-ai-sync`.
   - `test: sync` with recipe `uv run pytest` becomes `test: test-go` with no recipe of its own — the existing `test-go` target already runs `go test ./... -count=1`, so do not duplicate that line.
   - `precommit: sync format test check` becomes `precommit: precommit-go`, keeping its `@echo "✅ All precommit checks passed"` recipe line.
   - `format` becomes `gofmt -w cmd internal`.
   - `lint` becomes `go vet ./...`.
   - Delete the `typecheck` target entirely.
   - `check: lint typecheck` becomes `check: lint`.
   - In the `local` branch of `clean`, the recipe becomes exactly these two lines — the `docker rmi` line unchanged, then `rm -rf dist`:
     ```make
     	docker rmi $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) || true
     	rm -rf dist
     ```
     Delete the `.venv`, `*.egg-info`, `.pytest_cache`, `.mypy_cache` and `.ruff_cache` removals and the `find . -type d -name __pycache__` line. The `else` branch is unchanged.

3. Add a `## Unreleased` section to `CHANGELOG.md`, immediately above the `## v0.18.2` heading, containing exactly one bullet:

   ```
   - feat: publish darwin/linux Go binary archives via GoReleaser and retire the Python install/test/format/lint targets, so the CLI installs from a release asset instead of a local build
   ```

   Add the blank line after the heading and the blank line after the bullet, matching the spacing of the existing sections. Do not touch any released section.

4. Retire the Python toolchain from the project's own definition of done and build conventions, so the completion report is graded against the toolchain that actually exists after this change.

   - In `docs/dod.md`, in the build checklist, replace the `make precommit` line with `- [ ] \`make precommit\` passes (gofmt + go vet + go test)` and delete the "No new ruff warnings or errors" and "No new mypy errors (strict mode)" lines. In the tests checklist, replace "Tests use pytest conventions (no unittest)" with "Go tests use the standard `testing` package". In the Code Quality checklist, delete the "Type hints on all function signatures" and "Docstrings on all public functions (following existing style)" lines. In the Style checklist, delete the "subprocess.run with `check=False` and explicit error handling" line and change "(see `git_operations.py`)" to "(see `internal/`)". In the Tests checklist, keep "New functions have tests" and "Existing tests still pass" unchanged. Leave every other line in the file unchanged.
   - In `CLAUDE.md`, under `### Build and test`, replace the three bullets with `- \`make precommit\` — gofmt + go vet + go test`, `- \`make test\` — Go tests only` and `- \`make check\` — go vet`. Delete the `### Test conventions` subsection entirely, and delete the `ruff`, `mypy`, `uv sync --all-extras` and `uv run` bullets from `### Dependencies` and `### Toolchain`. Replace the remaining `### Toolchain` bullet `- Python 3.14, uv package manager` with `- Go, version per go.mod` and `- Python 3.14 / uv — Data Assistant image only`, and add a single line `All Python dependencies below are for the Data Assistant image only:` immediately under the `### Dependencies` heading. In the H1 intro line, change `(Python/uv)` to `(Go; the Python tree is retained only to build the Data Assistant image)`. Do not touch `## Dark Factory Workflow`, `## Architecture` or `## Key Design Decisions`.

5. Do not add Go tests. This prompt changes build configuration and documentation, not behaviour: there is no new Go function to exercise. `goreleaser check` is not installed in the container; the operator runs it against the pinned version before the workflow lands.

   The 14 Python test modules under `tests/` (plus `conftest.py`) lose their only runner once `make test` points at `test-go`, and no CI job will run them. Retire them from the gate — do not delete them, and do not add a `test-py` target. `src/`, `pyproject.toml` and `uv.lock` stay because the Data Assistant image is built from them (`Dockerfile` copies `pyproject.toml`, `uv.lock`, `README.md` and `src/`); `tests/` stays because deleting the Python suite is out of scope here. State this retirement plainly in the completion report.
</requirements>

<constraints>
- Change only `.goreleaser.yaml` (new), `Makefile`, `CHANGELOG.md`, `docs/dod.md` and `CLAUDE.md`.
- Do NOT touch `.github/` or anything under `.github/workflows/`. The CI GitHub App holds no `workflows` permission and any commit there is rejected at push; the workflow files land as separate operator commits. This is why the `ci.yml` `test` job is named in `<context>` but not fixed here — say so plainly in the completion report.
- The `ci.yml` `test` job will fail after requirement 2 (Python runner, Go targets). Do not edit `.github/`; state in the completion report that the operator must replace that job before merge.
- The Homebrew cask in `bborbe/homebrew-tap` is replaced by this config, not edited here. The existing `Casks/git-ai-sync.rb` is a hand-maintained wheel cask carrying `depends_on formula: "uv"` and a `setup-launchd` postflight that registers the launchd agent; the generated cask drops both, and `update-git-ai-sync-cask.yml` in the tap must be retired in the same rollout. State in the completion report that the cask no longer registers the launchd agent and that re-registration is tracked by the separate launchd task.
- Do NOT delete `pyproject.toml`, `uv.lock`, `src/` or `tests/`. `Dockerfile` copies `pyproject.toml`, `uv.lock`, `README.md` and `src/` to build the Data Assistant image, so removing them breaks a live pod's build.
- Do NOT change `internal/version/version.go`, `internal/cli/cli.go` or any Go source. The version variable and the `--version` template are already correct.
- Do NOT change `test-go`, `precommit-go`, the `build`/`upload`/`clean-image`/`buca` targets, or any variable block (`DOCKER_REGISTRY`, `IMAGE`, `BRANCH`, `BUILD_TARGET`, `BUILD_*`, `REF`, `VERSION`).
- The archive name template stays `{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}` and the checksum file stays `checksums.txt` — the downstream cask and the release-asset checks depend on both.
- `release.mode` is `keep-existing` and `release.replace_existing_artifacts` is `true`. Do not swap one for the other: they govern different things.
- The GitHub workflow files are out of scope; they land separately as operator commits.
- Do NOT commit — dark-factory handles git.
- Existing Go tests must still pass. The Python suite under `tests/` is no longer run by any target (see requirement 5).
</constraints>

<verification>
Run these from the repository root:

```
test -f .goreleaser.yaml
yq -e '.version == 2' .goreleaser.yaml
yq -e '.builds[0].main == "./cmd/git-ai-sync"' .goreleaser.yaml
yq -e '.builds[0].ldflags[1] == "-X github.com/bborbe/git-ai-sync/internal/version.Version={{ .Tag }}"' .goreleaser.yaml
yq -e '.archives[0].formats[0] == "tar.gz"' .goreleaser.yaml
yq -e '.archives[0].name_template == "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"' .goreleaser.yaml
yq -e '.checksum.name_template == "checksums.txt"' .goreleaser.yaml
yq -e '.release.mode == "keep-existing"' .goreleaser.yaml
yq -e '.release.replace_existing_artifacts == true' .goreleaser.yaml
yq -e '.homebrew_casks[0].repository.name == "homebrew-tap"' .goreleaser.yaml
yq -e '.homebrew_casks[0].hooks.post.install_steps' .goreleaser.yaml
go build ./...
make test
make check
make precommit
```

Expected: every `yq -e` line exits 0; `go build ./...`, `make test`, `make check` and `make precommit` all exit 0. A `grep` on a key would pass even when the key is nested under the wrong parent, so the structure is asserted with `yq`, not with substrings. `make precommit` must be green — it is the repo's configured validation command, so a pass on `make precommit-go` alone is not enough.

Confirm the Python path is gone from the build targets and the project docs:

```
! grep -q 'uv\|pyproject' Makefile
! grep -qE '^sync:|^typecheck:' Makefile
grep -q 'go install ./cmd/git-ai-sync' Makefile
grep -q 'go vet ./...' Makefile
! make help | grep -q 'uv'
! grep -q 'ruff\|mypy\|pytest' docs/dod.md
grep -q 'gofmt + go vet + go test' docs/dod.md
! grep -q 'ruff\|mypy\|pytest' CLAUDE.md
grep -q 'go vet' CLAUDE.md
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1"
```

Expected: every `! grep` and `! make help | grep` line exits 0; each positive `grep -q` exits 0; the `CHANGELOG.md` count prints `1`. The absence assertions use `! grep -q` rather than `test -z "$(grep …)"` so a deleted or empty file fails instead of passing.

Before finishing, re-run every command in `<verification>` and confirm it exits 0, then walk the assigned criteria against the change:
- `.goreleaser.yaml` builds `./cmd/git-ai-sync` for darwin/linux × amd64/arm64 with `CGO_ENABLED=0` and the tag injected into `internal/version.Version`;
- the archives are `tar.gz` named `<project>_<os>_<arch>` and the checksum file is `checksums.txt`;
- `release.mode` is `keep-existing` and `replace_existing_artifacts` is `true`;
- the `homebrew_casks` stanza targets `bborbe/homebrew-tap` with the `HOMEBREW_TAP_TOKEN` env, the `goreleaser-bot` commit author and the quarantine-strip hook;
- `Makefile` has no `uv` or `pyproject` reference left, `make help` no longer mentions a dependency sync, and `make precommit` resolves to the Go checks;
- `docs/dod.md` and `CLAUDE.md` no longer name ruff, mypy or pytest;
- `CHANGELOG.md` carries exactly one `## Unreleased` heading with one bullet.
</verification>
