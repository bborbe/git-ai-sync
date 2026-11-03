.PHONY: help install sync run test check precommit format lint typecheck clean build upload buca

DOCKER_REGISTRY ?= docker.io
IMAGE ?= bborbe/git-ai-sync
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)

# Which machine builds the image. `local` builds here with the local daemon; any
# other value is a cluster alias (sun, nukedev, nukeprod) or a raw kubectl
# context name, and the build runs as a Job there.
BUILD_TARGET ?= local

# Cluster-build inputs. The vendored cluster script defaults the namespace,
# timeout and Dockerfile; the clone URL, tag and image come from this repository.
BUILD_TIMEOUT ?= 3600
BUILD_REPO_URL ?= https://github.com/$(IMAGE).git
BUILD_DOCKERFILE ?= Dockerfile
BUILD_PUSH_SECRET ?= buildkit-push

# The commit the cluster clones. Empty on purpose: the cluster arm must be told
# which commit to build, and the script refuses to run without it.
REF ?=

# The tag the image carries. Defaults to the git description so a plain
# `make build` works. The cluster arm refuses this default and demands an
# explicit scratch tag, so a cluster build can never overwrite a moving tag.
VERSION ?= $(shell git describe --tags --always --dirty)

help:
	@echo "Available targets:"
	@echo "  install    - Install dependencies"
	@echo "  sync       - Sync dependencies with uv"
	@echo "  run        - Run git-ai-sync"
	@echo "  test       - Run tests"
	@echo "  check      - Run lint and typecheck"
	@echo "  precommit  - Run all precommit checks"
	@echo "  clean      - Clean build artifacts"

install: sync

sync:
	@uv sync --all-extras

run:
	uv run git-ai-sync

test: sync
	uv run pytest

precommit: sync format test check
	@echo "✅ All precommit checks passed"

format:
	uv run ruff format .
	uv run ruff check --fix . || true

lint:
	uv run ruff check .

typecheck:
	uv run mypy src

check: lint typecheck

ifeq ($(BUILD_TARGET),local)
clean:
	docker rmi $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) || true
	rm -rf .venv dist *.egg-info .pytest_cache .mypy_cache .ruff_cache
	find . -type d -name __pycache__ -exec rm -rf {} +
else
clean:
	@echo "clean: skipped — BUILD_TARGET=$(BUILD_TARGET); no local image to remove"
endif

.PHONY: build
ifeq ($(BUILD_TARGET),local)
build:
	docker build \
		--no-cache \
		--rm=true \
		--platform=linux/amd64 \
		--build-arg BUILD_GIT_VERSION=$$(git describe --tags --always --dirty) \
		--build-arg BUILD_GIT_COMMIT=$$(git rev-parse --short HEAD) \
		--build-arg BUILD_DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
		-t $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) \
		-f Dockerfile .
else
build:
	@if [ -z "$(REF)" ]; then \
		echo "ERROR: REF is required for a cluster build — pass REF=<commit>" >&2; \
		exit 1; \
	fi
	@if [ "$(origin VERSION)" = "file" ] || [ "$(origin VERSION)" = "undefined" ]; then \
		echo "ERROR: VERSION is required for a cluster build — pass VERSION=<scratch-tag>." >&2; \
		echo "  The cluster arm pushes, so an unset tag would overwrite a moving reference." >&2; \
		echo "  VERSION defaults to the git description ($(VERSION)), which is not an acceptable target." >&2; \
		exit 1; \
	fi
	BUILD_TARGET='$(BUILD_TARGET)' \
	NAMESPACE='build' \
	TIMEOUT='$(BUILD_TIMEOUT)' \
	REPO_URL='$(BUILD_REPO_URL)' \
	REF='$(REF)' \
	TAG='$(VERSION)' \
	DOCKERFILE='$(BUILD_DOCKERFILE)' \
	IMAGE='$(DOCKER_REGISTRY)/$(IMAGE)' \
	PUSH='true' \
	PUSH_SECRET='$(BUILD_PUSH_SECRET)' \
	hack/build-cluster.sh
endif

.PHONY: upload
upload:
ifeq ($(BUILD_TARGET),local)
	docker push $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION)
else
	@echo "upload: skipped — BUILD_TARGET=$(BUILD_TARGET); the cluster Job already pushed $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION)"
endif

.PHONY: clean-image
ifeq ($(BUILD_TARGET),local)
clean-image:
	docker rmi $(DOCKER_REGISTRY)/$(IMAGE):$(VERSION) || true
else
clean-image:
	@echo "clean-image: skipped — BUILD_TARGET=$(BUILD_TARGET); no local image to remove"
endif

.PHONY: buca
buca: build upload clean-image
