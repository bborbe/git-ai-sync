.PHONY: help install sync run test check precommit format lint typecheck clean

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

test:
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

clean:
	rm -rf .venv dist *.egg-info .pytest_cache .mypy_cache .ruff_cache
	find . -type d -name __pycache__ -exec rm -rf {} +
