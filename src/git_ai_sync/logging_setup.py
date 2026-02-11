"""Logging configuration module.

Called once at application startup in __main__.py.
"""

import logging


def configure_logging(level: str = "INFO") -> None:
    """Configure logging for the application.

    Args:
        level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
    """
    logging.basicConfig(
        format="%(asctime)s %(levelname)-8s [%(name)s:%(lineno)d] %(message)s",
        level=getattr(logging, level.upper()),
        datefmt="%Y-%m-%d %H:%M:%S",
    )
