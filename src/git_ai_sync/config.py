"""Configuration management."""

from pydantic import AliasChoices, Field
from pydantic_settings import BaseSettings, SettingsConfigDict


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
