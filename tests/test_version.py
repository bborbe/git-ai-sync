"""Test version information."""

from git_ai_sync import __version__


def test_version() -> None:
    """Test version is set."""
    assert __version__ == "0.0.1"
