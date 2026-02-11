"""Configuration management."""

from pydantic import Field
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
    model: str = Field(default="claude-sonnet-4-5-20250929", description="Claude model to use")

    # Logging
    log_level: str = Field(default="INFO", description="Logging level")
