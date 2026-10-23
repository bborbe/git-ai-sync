"""Configuration management."""

from pathlib import Path

from pydantic import AliasChoices, Field
from pydantic_settings import BaseSettings, SettingsConfigDict


def _default_attention_script() -> str:
    """The supervisor plugin's attention-ask.py, resolved from the home directory at runtime.

    Deliberately the marketplace path, not the version-numbered plugin cache path,
    which changes on every plugin update.
    """
    return str(
        Path.home()
        / ".claude"
        / "plugins"
        / "marketplaces"
        / "claude-supervisor"
        / "scripts"
        / "attention-ask.py"
    )


class Config(BaseSettings):
    """Application configuration loaded from environment variables."""

    model_config = SettingsConfigDict(
        env_prefix="GIT_AI_SYNC_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # Sync settings
    interval: int = Field(default=30, description="Sync interval in seconds")
    commit_prefix: str = Field(default="auto", description="Auto-commit message prefix")

    # Watch debounce bound (watch mode only)
    max_wait: int = Field(
        default=300,
        description=(
            "Maximum seconds without a successful watch sync before one is forced"
            " regardless of how recently files changed (GIT_AI_SYNC_MAX_WAIT)"
        ),
    )

    # Claude settings (for future conflict resolution)
    anthropic_api_key: str | None = Field(
        default=None,
        alias="ANTHROPIC_API_KEY",
        description="Anthropic API key for AI conflict resolution",
    )
    model: str = Field(
        default="claude-sonnet-4-5-20250929",
        validation_alias=AliasChoices("ANTHROPIC_MODEL", "GIT_AI_SYNC_MODEL"),
        description="Model name (set ANTHROPIC_MODEL for alt-provider routing)",
    )

    # Logging
    log_level: str = Field(default="INFO", description="Logging level")

    # Pushgateway metrics
    pushgateway_url: str | None = Field(
        default=None,
        description="Prometheus pushgateway base URL (e.g. https://pushgateway.dev.nuke.benjamin-borbe.de)",
    )
    pushgateway_username: str | None = Field(
        default=None,
        description="Pushgateway basic-auth username",
    )
    pushgateway_password: str | None = Field(
        default=None,
        description="Pushgateway basic-auth password",
    )

    # Consecutive-failure attention alert (watch mode only)
    stall_threshold: int = Field(
        default=10,
        description=(
            "Consecutive failed watch cycles before one attention card is posted"
            " (must be >= 2; the check is applied when watch starts)"
        ),
    )
    attention_script: str = Field(
        default_factory=_default_attention_script,
        description="Path to the supervisor attention-ask.py script (GIT_AI_SYNC_ATTENTION_SCRIPT)",
    )
    attention_store_url: str = Field(
        default="http://localhost:18080",
        alias="ATTENTION_STORE_URL",
        description="Attention store base URL — the same variable the posting script reads",
    )

    # Rotating file logging
    log_file: str | None = Field(
        default=None,
        description="Log file path for the rotating file handler (GIT_AI_SYNC_LOG_FILE)",
    )
