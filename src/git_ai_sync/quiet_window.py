"""Decaying quiet-window gate for the watch loop's debounce decision.

The quiet window starts at the configured poll interval and halves on every
consecutive skipped cycle, so a vault that never stops changing still becomes
eligible to sync. A separate maximum wait bounds the total delay: the cycle is
accepted once the maximum wait has elapsed since the last successful sync,
even if the vault is still being written.

The module is a pure decision unit: it reads no clock, logs nothing, and takes
every input as a parameter, so its schedule and cap arithmetic are testable
without running the watch loop.
"""

from dataclasses import dataclass


@dataclass(frozen=True)
class GateDecision:
    """One watch cycle's debounce verdict.

    Attributes:
        sync: Whether the cycle should run the sync sequence.
        window: The quiet window in force for this cycle, in seconds.
        cap_fired: Whether the decision was forced by the maximum wait.
    """

    sync: bool
    window: float
    cap_fired: bool


def decayed_window(interval: float, consecutive_skips: int) -> float:
    """Return the quiet window required after ``consecutive_skips`` skips.

    The window starts at ``interval`` and halves on every consecutive skip,
    so a vault that never goes quiet still becomes eligible to sync.

    Args:
        interval: The configured poll interval, in seconds.
        consecutive_skips: Consecutive skipped cycles since the last success.

    Returns:
        The required quiet window, in seconds.
    """
    return interval * (0.5**consecutive_skips)


class QuietWindowTracker:
    """Tracks consecutive skipped cycles and the last successful sync.

    The quiet window halves on every consecutive skip and returns to the
    full interval after any successful sync. The elapsed clock is monotonic
    and restarts on each success, so a failed cycle never resets it.
    """

    def __init__(self, now: float) -> None:
        """Start the tracker with a zero skip count and the current clock.

        Args:
            now: The caller's monotonic clock reading at process start.
        """
        self._consecutive_skips: int = 0
        self._last_success: float = now

    @property
    def consecutive_skips(self) -> int:
        """Consecutive skipped cycles since the last successful sync."""
        return self._consecutive_skips

    def window(self, interval: float) -> float:
        """The quiet window in force, in seconds."""
        return decayed_window(interval, self._consecutive_skips)

    def elapsed_since_success(self, now: float) -> float:
        """Seconds of monotonic time since the last successful sync."""
        return now - self._last_success

    def decide(
        self,
        *,
        seconds_since_change: float,
        interval: float,
        max_wait: float,
        now: float,
    ) -> GateDecision:
        """Decide whether this cycle may sync.

        The cycle is accepted when the newest change is older than the
        window in force, or when the maximum wait has elapsed since the last
        successful sync — whichever happens first.
        """
        window = self.window(interval)
        cap_fired = self.elapsed_since_success(now) >= max_wait
        return GateDecision(
            sync=cap_fired or seconds_since_change >= window,
            window=window,
            cap_fired=cap_fired,
        )

    def record_skip(self) -> None:
        """Count one skipped cycle, halving the window for the next one."""
        self._consecutive_skips += 1

    def record_success(self, now: float) -> None:
        """Reset the decay and restart the maximum-wait clock."""
        self._consecutive_skips = 0
        self._last_success = now
