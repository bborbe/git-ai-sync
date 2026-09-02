"""Tests for logging configuration."""

import logging
import logging.handlers
from collections.abc import Generator
from pathlib import Path
from unittest.mock import patch

import pytest

from git_ai_sync.logging_setup import configure_logging


@pytest.fixture(autouse=True)
def _cleanup_file_handlers() -> Generator[None]:
    """Remove and close handlers added by configure_logging during the test."""
    original = list(logging.getLogger().handlers)
    yield
    root = logging.getLogger()
    for handler in list(root.handlers):
        if handler not in original:
            handler.close()
            root.removeHandler(handler)


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


def test_no_file_handler_without_log_file() -> None:
    """configure_logging() should not attach a RotatingFileHandler."""
    configure_logging()
    assert not any(
        isinstance(handler, logging.handlers.RotatingFileHandler)
        for handler in logging.getLogger().handlers
    )


def test_file_handler_attached_with_rotation(tmp_path: Path) -> None:
    """configure_logging(log_file=...) attaches a rotating file handler."""
    log_file = tmp_path / "git-ai-sync.log"
    configure_logging("INFO", log_file)
    rotating_handlers = [
        handler
        for handler in logging.getLogger().handlers
        if isinstance(handler, logging.handlers.RotatingFileHandler)
    ]
    assert len(rotating_handlers) == 1
    assert rotating_handlers[0].maxBytes == 5_000_000
    assert rotating_handlers[0].backupCount == 5

    logging.getLogger("rotation_probe").info("rotation probe message")
    assert log_file.exists()
    assert "rotation probe message" in log_file.read_text()


def test_rotation_caps_live_file_and_backups(tmp_path: Path) -> None:
    """Writing >5 MB through the logger rotates and keeps backups bounded."""
    log_file = tmp_path / "git-ai-sync.log"
    configure_logging("INFO", log_file)
    logger = logging.getLogger("rotation_probe")
    for _ in range(12_000):
        logger.info("x" * 1000)

    assert log_file.stat().st_size <= 5_000_000
    backups = sorted(tmp_path.glob("git-ai-sync.log.*"))
    assert len(backups) <= 5
