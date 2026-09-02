"""Tests for configuration module."""

import pytest

from git_ai_sync.config import Config


class TestConfigDefaults:
    def test_defaults(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("GIT_AI_SYNC_INTERVAL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_COMMIT_PREFIX", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_MODEL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_LOG_LEVEL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_URL", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_USERNAME", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_PUSHGATEWAY_PASSWORD", raising=False)
        monkeypatch.delenv("GIT_AI_SYNC_LOG_FILE", raising=False)
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
        monkeypatch.delenv("ANTHROPIC_MODEL", raising=False)

        config = Config()
        assert config.interval == 30
        assert config.commit_prefix == "auto"
        assert config.model == "claude-sonnet-4-5-20250929"
        assert config.log_level == "INFO"
        assert config.pushgateway_url is None
        assert config.pushgateway_username is None
        assert config.pushgateway_password is None
        assert config.log_file is None

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
        monkeypatch.delenv("ANTHROPIC_MODEL", raising=False)
        monkeypatch.setenv("GIT_AI_SYNC_MODEL", "claude-opus-4-6")
        config = Config()
        assert config.model == "claude-opus-4-6"

    def test_model_from_anthropic_model_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("GIT_AI_SYNC_MODEL", raising=False)
        monkeypatch.setenv("ANTHROPIC_MODEL", "MiniMax-M2.7")
        config = Config()
        assert config.model == "MiniMax-M2.7"

    def test_anthropic_model_wins_over_git_ai_sync_model(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_MODEL", "claude-opus-4-6")
        monkeypatch.setenv("ANTHROPIC_MODEL", "MiniMax-M2.7")
        config = Config()
        assert config.model == "MiniMax-M2.7"

    def test_anthropic_key_alias(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test-key-123")
        config = Config()
        assert config.anthropic_api_key == "sk-test-key-123"

    def test_pushgateway_from_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_PUSHGATEWAY_URL", "https://pushgateway.test")
        monkeypatch.setenv("GIT_AI_SYNC_PUSHGATEWAY_USERNAME", "monitoring")
        monkeypatch.setenv("GIT_AI_SYNC_PUSHGATEWAY_PASSWORD", "s3cret")
        config = Config()
        assert config.pushgateway_url == "https://pushgateway.test"
        assert config.pushgateway_username == "monitoring"
        assert config.pushgateway_password == "s3cret"

    def test_log_file_from_env(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("GIT_AI_SYNC_LOG_FILE", "/tmp/git-ai-sync.log")
        config = Config()
        assert config.log_file == "/tmp/git-ai-sync.log"
