"""Test version information."""

import re

from git_ai_sync import __version__


def test_version() -> None:
    """Test version is set and follows a valid version-like format."""
    assert __version__
    assert re.match(r"^\d+\.\d+(?:\.\d+)?(?:[.+-].+)?$", __version__)
