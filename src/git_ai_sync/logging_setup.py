"""Logging configuration module.

Called once at application startup in __main__.py.
"""

import logging
import sys
from logging.handlers import RotatingFileHandler
from pathlib import Path

_LOG_FORMAT = "%(asctime)s %(levelname)-8s [%(name)s:%(lineno)d] %(message)s"
_DATE_FORMAT = "%Y-%m-%d %H:%M:%S"


def configure_logging(level: str = "INFO", log_file: Path | None = None) -> None:
    """Configure logging for the application.

    Args:
        level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
        log_file: Optional log file path; when set, log output is also written to
            this file via a RotatingFileHandler (maxBytes=5_000_000, backupCount=5).
    """
    log_level = getattr(logging, level.upper())
    if log_file is None:
        logging.basicConfig(format=_LOG_FORMAT, level=log_level, datefmt=_DATE_FORMAT)
        return

    log_path = Path(log_file)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    handlers: list[logging.Handler] = [
        logging.StreamHandler(sys.stderr),
        RotatingFileHandler(
            log_path,
            maxBytes=5_000_000,
            backupCount=5,
            encoding="utf-8",
        ),
    ]
    for handler in handlers:
        handler.setFormatter(logging.Formatter(_LOG_FORMAT, _DATE_FORMAT))
    logging.basicConfig(
        level=log_level,
        format=_LOG_FORMAT,
        datefmt=_DATE_FORMAT,
        handlers=handlers,
        force=True,
    )
