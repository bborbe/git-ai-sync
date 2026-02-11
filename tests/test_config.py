"""Tests for configuration module."""

import pytest

from git_ai_sync.config import Config


class TestConfigDefaults:
    def test_defaults(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("GIT_AI_SYNC_INTERVAL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_COMMIT_PREFIX", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_MODEL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_LOG_LEVEL", raising=False)
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)

        config = Config()
        assert config.interval == 30
        assert config.commit_prefix == "auto"
        assert config.model == "claude-sonnet-4-5-20250929"
        assert config.log_level == "INFO"

    def test_anthropic_key_none_by_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
        config = Config()
        assert config.anthropic_api_key is None


class TestConfigFromEnv:
    def test_interval_from_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_INTERVAL", "60")
        config = Config()
        assert config.interval == 60

    def test_commit_prefix_from_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_COMMIT_PREFIX", "vault backup")
        config = Config()
        assert config.commit_prefix == "vault backup"

    def test_model_from_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_MODEL", "claude-opus-4-6")
        config = Config()
        assert config.model == "claude-opus-4-6"

    def test_anthropic_key_alias(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test-key-123")
        config = Config()
        assert config.anthropic_api_key == "sk-test-key-123"
