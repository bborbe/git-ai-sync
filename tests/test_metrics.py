"""Tests for the pushgateway metrics module."""

import base64
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import HTTPError, URLError

import pytest

from git_ai_sync.metrics import (
    HEARTBEAT_METRIC,
    LAST_SUCCESS_METRIC,
    push_heartbeat,
    push_last_success,
    vault_label,
)


class TestVaultLabel:
    def test_vault_label_is_directory_name(self) -> None:
        assert vault_label(Path("/a/b/Personal")) == "Personal"


class TestPushHeartbeat:
    def test_push_heartbeat_url_method_and_auth(self) -> None:
        with (
            patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen,
            patch("git_ai_sync.metrics.time.time", return_value=1720000000.0),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            assert (
                req.full_url == "https://pushgateway.test/metrics/job/git_ai_sync/instance/Personal"
            )
            assert req.method == "PUT"
            assert req.headers["Authorization"] == (
                "Basic " + base64.b64encode(b"monitoring:s3cret").decode()
            )
            # urllib.request.Request canonicalizes header keys via key.capitalize()
            assert req.headers["Content-type"] == "text/plain; version=0.0.4"
            body = req.data.decode()
            assert f"# TYPE {HEARTBEAT_METRIC} gauge" in body
            assert f'{HEARTBEAT_METRIC}{{vault="Personal"}} 1720000000' in body

    def test_push_heartbeat_tolerates_trailing_slash(self) -> None:
        with patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen:
            result = push_heartbeat(
                "https://pushgateway.test/", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            assert (
                req.full_url == "https://pushgateway.test/metrics/job/git_ai_sync/instance/Personal"
            )

    def test_push_heartbeat_url_encodes_vault_in_path_only(self) -> None:
        with (
            patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen,
            patch("git_ai_sync.metrics.time.time", return_value=1720000000.0),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/My Vault")
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            assert "instance/My%20Vault" in req.full_url
            assert 'vault="My Vault"' in req.data.decode()

    def test_push_heartbeat_escapes_quote_in_label_value(self) -> None:
        with (
            patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen,
            patch("git_ai_sync.metrics.time.time", return_value=1720000000.0),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path('/vaults/My"Vault')
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            assert 'vault="My\\"Vault"' in req.data.decode()

    def test_push_heartbeat_no_auth_header_without_username(self) -> None:
        with patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen:
            result = push_heartbeat(
                "https://pushgateway.test", None, None, Path("/vaults/Personal")
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            assert "Authorization" not in req.headers


class TestPushLastSuccess:
    def test_push_last_success_metric_name(self) -> None:
        with (
            patch("git_ai_sync.metrics.urlopen", return_value=MagicMock()) as mock_urlopen,
            patch("git_ai_sync.metrics.time.time", return_value=1720000000.0),
        ):
            result = push_last_success(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is True
            req = mock_urlopen.call_args.args[0]
            body = req.data.decode()
            assert f"# TYPE {LAST_SUCCESS_METRIC} gauge" in body
            assert f'{LAST_SUCCESS_METRIC}{{vault="Personal"}} 1720000000' in body


class TestPushFailures:
    def test_push_failure_returns_false_and_logs_warning(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        with (
            patch(
                "git_ai_sync.metrics.urlopen",
                side_effect=HTTPError("https://pushgateway.test", 401, "Unauthorized", {}, None),
            ),
            caplog.at_level("WARNING"),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is False
            assert any("metric push failed" in r.message for r in caplog.records)
            assert "s3cret" not in caplog.text

    def test_push_timeout_returns_false(self) -> None:
        with patch("git_ai_sync.metrics.urlopen", side_effect=TimeoutError("timed out")):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is False

    def test_push_unreachable_returns_false(self, caplog: pytest.LogCaptureFixture) -> None:
        with (
            patch(
                "git_ai_sync.metrics.urlopen",
                side_effect=URLError("connection refused"),
            ),
            caplog.at_level("WARNING"),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is False
            assert any("metric push failed" in r.message for r in caplog.records)

    def test_push_never_raises_on_arbitrary_error(self, caplog: pytest.LogCaptureFixture) -> None:
        with (
            patch("git_ai_sync.metrics.urlopen", side_effect=RuntimeError("boom")),
            caplog.at_level("WARNING"),
        ):
            result = push_heartbeat(
                "https://pushgateway.test", "monitoring", "s3cret", Path("/vaults/Personal")
            )
            assert result is False
            assert any("metric push failed" in r.message for r in caplog.records)
