"""Tests for logging configuration."""

import logging
from unittest.mock import patch

from git_ai_sync.logging_setup import configure_logging


def test_configure_logging_default_level() -> None:
    """Default level should be INFO."""
    with patch("logging.basicConfig") as mock:
        configure_logging()
        mock.assert_called_once()
        assert mock.call_args[1]["level"] == logging.INFO


def test_configure_logging_debug_level() -> None:
    """Explicit DEBUG level should be passed through."""
    with patch("logging.basicConfig") as mock:
        configure_logging("DEBUG")
        mock.assert_called_once()
        assert mock.call_args[1]["level"] == logging.DEBUG


def test_configure_logging_case_insensitive() -> None:
    """Level string should be case-insensitive."""
    with patch("logging.basicConfig") as mock:
        configure_logging("warning")
        mock.assert_called_once()
        assert mock.call_args[1]["level"] == logging.WARNING


def test_configure_logging_format() -> None:
    """Format string must contain required fields."""
    with patch("logging.basicConfig") as mock:
        configure_logging()
        fmt = mock.call_args[1]["format"]
        assert "%(asctime)s" in fmt
        assert "%(levelname)" in fmt
        assert "%(name)s" in fmt
        assert "%(lineno)d" in fmt
        assert "%(message)s" in fmt


def test_configure_logging_datefmt() -> None:
    """Date format should be ISO-style."""
    with patch("logging.basicConfig") as mock:
        configure_logging()
        assert mock.call_args[1]["datefmt"] == "%Y-%m-%d %H:%M:%S"
