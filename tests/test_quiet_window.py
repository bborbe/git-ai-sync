"""Tests for the decaying quiet-window debounce decision."""

import pytest

from git_ai_sync.quiet_window import GateDecision, QuietWindowTracker, decayed_window


@pytest.mark.parametrize(
    ("consecutive_skips", "expected"),
    [
        (0, 32.0),
        (1, 16.0),
        (2, 8.0),
        (3, 4.0),
        (4, 2.0),
    ],
)
def test_decayed_window_halves_per_skip(consecutive_skips: int, expected: float) -> None:
    assert decayed_window(32, consecutive_skips) == expected


def test_decayed_window_starts_at_interval() -> None:
    assert decayed_window(32, 0) == 32.0


def test_decayed_window_tiny_after_many_skips() -> None:
    assert decayed_window(32, 20) < 0.001


def test_quiet_window_resets_after_success() -> None:
    tracker = QuietWindowTracker(now=0.0)
    for _ in range(3):
        tracker.record_skip()
    assert tracker.window(32) == 4.0
    assert tracker.consecutive_skips == 3

    tracker.record_success(now=90.0)
    assert tracker.window(32) == 32.0
    assert tracker.consecutive_skips == 0
    assert tracker.elapsed_since_success(90.0) == 0.0


def test_window_gate_rejects_then_accepts_at_interval_30() -> None:
    tracker = QuietWindowTracker(now=0.0)

    def decide() -> GateDecision:
        return tracker.decide(
            seconds_since_change=5.0,
            interval=30.0,
            max_wait=300.0,
            now=30.0,
        )

    tracker.record_skip()
    decision = decide()
    assert decision.sync is False
    assert decision.window == 15.0

    tracker.record_skip()
    decision = decide()
    assert decision.sync is False
    assert decision.window == 7.5

    tracker.record_skip()
    decision = decide()
    assert decision.sync is True
    assert decision.window == 3.75


def test_window_gate_rejects_then_accepts_at_interval_100() -> None:
    tracker = QuietWindowTracker(now=0.0)

    def decide() -> GateDecision:
        return tracker.decide(
            seconds_since_change=5.0,
            interval=100.0,
            max_wait=1000.0,
            now=30.0,
        )

    expected_windows = [50.0, 25.0, 12.5, 6.25]
    for expected in expected_windows:
        tracker.record_skip()
        decision = decide()
        assert decision.sync is False
        assert decision.window == expected

    tracker.record_skip()
    decision = decide()
    assert decision.sync is True
    assert decision.window == 3.125


def test_window_gate_cap_binds_at_second_boundary() -> None:
    tracker = QuietWindowTracker(now=0.0)

    first = tracker.decide(
        seconds_since_change=1.0,
        interval=30.0,
        max_wait=60.0,
        now=30.0,
    )
    assert first.sync is False
    assert first.cap_fired is False

    second = tracker.decide(
        seconds_since_change=1.0,
        interval=30.0,
        max_wait=60.0,
        now=60.0,
    )
    assert second.sync is True
    assert second.cap_fired is True


def test_window_gate_cap_binds_at_third_boundary() -> None:
    tracker = QuietWindowTracker(now=0.0)

    first = tracker.decide(
        seconds_since_change=1.0,
        interval=30.0,
        max_wait=75.0,
        now=60.0,
    )
    assert first.sync is False

    second = tracker.decide(
        seconds_since_change=1.0,
        interval=30.0,
        max_wait=75.0,
        now=90.0,
    )
    assert second.sync is True
    assert second.cap_fired is True


def test_window_gate_cap_does_not_fire_before_the_max_wait() -> None:
    tracker = QuietWindowTracker(now=0.0)

    decision = tracker.decide(
        seconds_since_change=1.0,
        interval=30.0,
        max_wait=300.0,
        now=270.0,
    )
    assert decision.cap_fired is False
    assert decision.sync is False


def test_window_gate_accepts_when_max_wait_below_interval() -> None:
    tracker = QuietWindowTracker(now=0.0)

    decision = tracker.decide(
        seconds_since_change=0.1,
        interval=30.0,
        max_wait=10.0,
        now=30.0,
    )
    assert decision.sync is True
    assert decision.cap_fired is True


def test_window_gate_zero_max_wait_always_forces() -> None:
    tracker = QuietWindowTracker(now=0.0)

    decision = tracker.decide(
        seconds_since_change=0.0,
        interval=30.0,
        max_wait=0.0,
        now=30.0,
    )
    assert decision.sync is True
    assert decision.cap_fired is True


def test_window_gate_accepts_infinite_change_age() -> None:
    tracker = QuietWindowTracker(now=0.0)

    decision = tracker.decide(
        seconds_since_change=float("inf"),
        interval=30.0,
        max_wait=300.0,
        now=30.0,
    )
    assert decision.sync is True
    assert decision.cap_fired is False
    assert decision.window == 30.0
