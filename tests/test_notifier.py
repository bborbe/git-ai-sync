"""Tests for the best-effort attention-board notifier."""

import socket
import sys
import time
from pathlib import Path
from unittest.mock import patch

import pytest

from git_ai_sync import notifier
from git_ai_sync.notifier import (
    ConsecutiveFailureTracker,
    close_stall_card,
    dedup_key,
    is_valid_item_id,
    post_stall_card,
    producer_id,
    redact_error_summary,
    should_post_card,
    validate_attention_script,
)

FIXED_TIME = 1720000000.0


def _closed_port_url() -> str:
    """A URL on a port nothing is listening on."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    return f"http://127.0.0.1:{port}"


def _argv_value(argv: list[str], flag: str) -> str:
    """The value following flag in argv."""
    return argv[argv.index(flag) + 1]


class TestConsecutiveFailureTracker:
    def test_failure_stamps_first_failure_only_once(self) -> None:
        tracker = ConsecutiveFailureTracker()
        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            tracker.record_failure()
        assert tracker.consecutive_failures == 1
        assert tracker.first_failure_at == FIXED_TIME

        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME + 60):
            tracker.record_failure()
        assert tracker.consecutive_failures == 2
        assert tracker.first_failure_at == FIXED_TIME

    def test_success_resets_count_stamp_and_guard(self) -> None:
        tracker = ConsecutiveFailureTracker()
        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            tracker.record_failure()
        tracker.outstanding_item_id = "a" * 32

        tracker.record_success()

        assert tracker.consecutive_failures == 0
        assert tracker.first_failure_at == 0.0
        assert tracker.outstanding_item_id is None

    def test_skip_changes_nothing(self) -> None:
        tracker = ConsecutiveFailureTracker()
        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            tracker.record_failure()
        tracker.outstanding_item_id = "b" * 32

        tracker.record_skip()

        assert tracker.consecutive_failures == 1
        assert tracker.first_failure_at == FIXED_TIME
        assert tracker.outstanding_item_id == "b" * 32


class TestShouldPostCard:
    @pytest.mark.parametrize(
        ("failures", "threshold", "outstanding", "expected"),
        [
            (9, 10, None, False),
            (10, 10, None, True),
            (11, 10, None, True),
            (10, 10, "a" * 32, False),
        ],
    )
    def test_should_post_card(
        self, failures: int, threshold: int, outstanding: str | None, expected: bool
    ) -> None:
        assert should_post_card(failures, threshold, outstanding) is expected


class TestIsValidItemId:
    @pytest.mark.parametrize(
        ("candidate", "expected"),
        [
            ("0" * 32, True),
            ("0123456789abcdef0123456789abcdef", True),
            ("0" * 31, False),
            ("0" * 33, False),
            ("0" * 31 + "A", False),
            ("0" * 31 + "g", False),
            ("", False),
        ],
    )
    def test_is_valid_item_id(self, candidate: str, expected: bool) -> None:
        assert is_valid_item_id(candidate) is expected


class TestKeys:
    def test_dedup_key_format(self) -> None:
        assert dedup_key("Personal") == "git-ai-sync-Personal-stalled"

    def test_producer_id_format(self) -> None:
        assert producer_id("Personal") == "git-ai-sync-Personal"


class TestValidateAttentionScript:
    def test_missing_path_warns_once_and_returns_none(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        missing = tmp_path / "nope.py"
        with caplog.at_level("WARNING"):
            assert validate_attention_script(str(missing)) is None
        assert len(caplog.records) == 1
        assert str(missing) in caplog.text

    def test_directory_warns_and_returns_none(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        with caplog.at_level("WARNING"):
            assert validate_attention_script(str(tmp_path)) is None
        assert str(tmp_path) in caplog.text

    def test_non_executable_readable_file_is_accepted(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        script = tmp_path / "attention-ask.py"
        script.write_text("print('hi')\n")
        script.chmod(0o644)

        with caplog.at_level("WARNING"):
            result = validate_attention_script(str(script))

        assert result == script.resolve()
        assert caplog.records == []

    def test_unreadable_file_warns_and_returns_none(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        script = tmp_path / "unreadable.py"
        script.write_text("print('hi')\n")
        script.chmod(0o000)

        with caplog.at_level("WARNING"):
            assert validate_attention_script(str(script)) is None
        assert str(script) in caplog.text

    def test_never_raises_for_arbitrary_input(self, caplog: pytest.LogCaptureFixture) -> None:
        with caplog.at_level("WARNING"):
            assert validate_attention_script("") is None


class TestRedactErrorSummary:
    def test_url_with_credential_is_replaced(self) -> None:
        result = redact_error_summary("fatal: unable to access 'https://user:s3cret@host/repo.git'")
        assert "s3cret" not in result
        assert "https://" not in result
        assert "<url>" in result

    def test_long_text_is_truncated_to_200(self) -> None:
        assert len(redact_error_summary("x" * 300)) == 200

    def test_short_text_is_unchanged(self) -> None:
        assert redact_error_summary("Sync failed: boom") == "Sync failed: boom"


class TestPostStallCard:
    def test_card_contract(self, stub_notifier) -> None:
        stub = stub_notifier()
        item_id = post_stall_card(
            stub.script,
            "Personal",
            Path("/vaults/Personal"),
            2,
            FIXED_TIME,
            "Sync failed: boom",
        )

        assert item_id == "0" * 32
        assert len(stub.invocations()) == 1
        argv = stub.invocations()[0]
        assert argv[0] == sys.executable
        assert argv[1] == str(stub.script)
        assert argv[2] == "post"

        assert argv.count("--dedup-key") == 1
        assert _argv_value(argv, "--dedup-key") == "git-ai-sync-Personal-stalled"
        assert _argv_value(argv, "--producer-id") == "git-ai-sync-Personal"
        assert _argv_value(argv, "--producer-kind") == "cron"

        assert argv.count("--option") == 2
        options = [argv[i + 1] for i, a in enumerate(argv) if a == "--option"]
        assert argv.count("--recommend") == 1
        assert _argv_value(argv, "--recommend") in options

        payload = _argv_value(argv, "--payload")
        assert "Personal" in payload
        assert "2" in payload

        context = _argv_value(argv, "--context")
        assert "/vaults/Personal" in context
        assert "Sync failed: boom" in context

        assert "--liveness-ref" not in argv
        assert "--expires-at" not in argv

    def test_missing_script_returns_none(
        self, tmp_path: Path, caplog: pytest.LogCaptureFixture
    ) -> None:
        with caplog.at_level("WARNING"):
            result = post_stall_card(
                tmp_path / "nope.py", "Personal", Path("/vaults/Personal"), 2, FIXED_TIME, "boom"
            )
        assert result is None
        assert any(r.levelname == "WARNING" for r in caplog.records)

    def test_nonzero_exit_returns_none(
        self, stub_notifier, caplog: pytest.LogCaptureFixture
    ) -> None:
        stub = stub_notifier(exit_code=1)
        with caplog.at_level("WARNING"):
            result = post_stall_card(
                stub.script, "Personal", Path("/vaults/Personal"), 2, FIXED_TIME, "boom"
            )
        assert result is None
        assert any("exit code 1" in r.message for r in caplog.records)

    def test_invalid_item_id_in_output_returns_none(
        self, stub_notifier, caplog: pytest.LogCaptureFixture
    ) -> None:
        stub = stub_notifier(item_id="not-hex")
        with caplog.at_level("WARNING"):
            result = post_stall_card(
                stub.script, "Personal", Path("/vaults/Personal"), 2, FIXED_TIME, "boom"
            )
        assert result is None
        assert any("unreadable" in r.message for r in caplog.records)

    def test_hanging_script_times_out_without_raising(
        self, stub_notifier, caplog: pytest.LogCaptureFixture
    ) -> None:
        stub = stub_notifier(sleep_seconds=30)
        started = time.monotonic()
        with (
            patch("git_ai_sync.metrics.PUSH_TIMEOUT_SECONDS", 0.2),
            caplog.at_level("WARNING"),
        ):
            result = post_stall_card(
                stub.script, "Personal", Path("/vaults/Personal"), 2, FIXED_TIME, "boom"
            )
        elapsed = time.monotonic() - started
        assert result is None
        assert elapsed < 5
        assert any("timed out" in r.message and "0.2" in r.message for r in caplog.records)


class TestCloseStallCard:
    def test_successful_close_posts_expected_path(self, recording_store) -> None:
        item_id = "a" * 32
        assert close_stall_card(recording_store.url, item_id) is True
        assert recording_store.requests == [("POST", f"/api/1.0/attention/{item_id}/close")]

    def test_409_is_warned_and_not_retried(
        self, recording_store, caplog: pytest.LogCaptureFixture
    ) -> None:
        recording_store.status = 409
        item_id = "a" * 32
        with caplog.at_level("WARNING"):
            assert close_stall_card(recording_store.url, item_id) is False
        assert any("409" in r.message for r in caplog.records)
        assert len(recording_store.requests) == 1

    def test_invalid_item_id_issues_no_request(
        self, recording_store, caplog: pytest.LogCaptureFixture
    ) -> None:
        with caplog.at_level("WARNING"):
            assert close_stall_card(recording_store.url, "not-hex") is False
        assert any("not-hex" in r.message for r in caplog.records)
        assert recording_store.requests == []

    def test_unreachable_store_returns_false(self, caplog: pytest.LogCaptureFixture) -> None:
        with caplog.at_level("WARNING"):
            assert close_stall_card(_closed_port_url(), "a" * 32) is False
        assert any(r.levelname == "WARNING" for r in caplog.records)


class TestComposedRecoveryRun:
    def test_recovery_clears_guard_so_later_stall_posts_again(
        self, stub_notifier, recording_store
    ) -> None:
        stub = stub_notifier()
        tracker = ConsecutiveFailureTracker()
        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            for _ in range(10):
                tracker.record_failure()

        assert should_post_card(tracker.consecutive_failures, 10, tracker.outstanding_item_id)
        tracker.outstanding_item_id = post_stall_card(
            stub.script, "Personal", Path("/vaults/Personal"), 10, FIXED_TIME, "boom"
        )
        assert len(stub.invocations()) == 1

        assert close_stall_card(recording_store.url, tracker.outstanding_item_id or "") is True
        tracker.record_success()
        assert len(recording_store.requests) == 1
        assert tracker.consecutive_failures == 0
        assert tracker.outstanding_item_id is None

        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            for _ in range(10):
                tracker.record_failure()
        tracker.outstanding_item_id = post_stall_card(
            stub.script, "Personal", Path("/vaults/Personal"), 10, FIXED_TIME, "boom"
        )

        invocations = stub.invocations()
        assert len(invocations) == 2
        assert _argv_value(invocations[0], "--dedup-key") == _argv_value(
            invocations[1], "--dedup-key"
        )

    def test_rejected_close_still_clears_counter_and_guard(
        self, stub_notifier, recording_store, caplog: pytest.LogCaptureFixture
    ) -> None:
        stub = stub_notifier()
        recording_store.status = 409
        tracker = ConsecutiveFailureTracker()
        with patch("git_ai_sync.notifier.time.time", return_value=FIXED_TIME):
            for _ in range(10):
                tracker.record_failure()
        tracker.outstanding_item_id = post_stall_card(
            stub.script, "Personal", Path("/vaults/Personal"), 10, FIXED_TIME, "boom"
        )

        with caplog.at_level("WARNING"):
            assert close_stall_card(recording_store.url, tracker.outstanding_item_id or "") is False
        tracker.record_success()

        assert any("409" in r.message for r in caplog.records)
        assert len(recording_store.requests) == 1
        assert tracker.consecutive_failures == 0
        assert tracker.outstanding_item_id is None


class TestNoCredentialOnCard:
    def test_credential_in_error_never_reaches_card_or_log(
        self, stub_notifier, caplog: pytest.LogCaptureFixture
    ) -> None:
        stub = stub_notifier()
        with caplog.at_level("WARNING"):
            post_stall_card(
                stub.script,
                "Personal",
                Path("/vaults/Personal"),
                2,
                FIXED_TIME,
                "fatal: unable to access 'https://user:s3cret@host/repo.git'",
            )

        recorded = "\n".join("\n".join(argv) for argv in stub.invocations())
        assert "s3cret" not in recorded
        assert "s3cret" not in caplog.text
        assert "<url>" in recorded


def test_module_exports_contract() -> None:
    assert notifier.dedup_key("V") == "git-ai-sync-V-stalled"
    assert notifier.producer_id("V") == "git-ai-sync-V"
    assert notifier.is_valid_item_id("0" * 32)
    assert notifier.should_post_card(2, 2, None)
