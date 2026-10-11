"""Best-effort attention-board alerting for a stalled git-ai-sync watch loop.

This is a side channel: every operation returns a value and never raises, so a
missing, unreadable, failing, or hanging attention-ask script can never break or
delay a sync cycle.
"""

import logging
import os
import re
import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from git_ai_sync import metrics

logger = logging.getLogger(__name__)

PRODUCER_KIND = "cron"
ITEM_ID_HEX_LEN = 32
CONTEXT_ERROR_MAX_CHARS = 200
_OUTPUT_TAIL_MAX_CHARS = 200
_ITEM_ID_PREFIX = "ITEM_ID: "
_OPTION_ACTIONABLE = "Investigate the vault's sync failure now"
_OPTION_DEFERRING = "Leave it — it will retry automatically"
_HEX_DIGITS = frozenset("0123456789abcdef")
_URL_RE = re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://\S+")


class ConsecutiveFailureTracker:
    """Consecutive failed-cycle counter and outstanding-card guard for the watch loop."""

    def __init__(self) -> None:
        """Start with no failures recorded and no card outstanding."""
        self.consecutive_failures: int = 0
        self.outstanding_item_id: str | None = None
        self.first_failure_at: float = 0.0

    def record_failure(self) -> None:
        """Count one failed cycle; stamp the run's start on the 0 -> 1 transition."""
        if self.consecutive_failures == 0:
            self.first_failure_at = time.time()
        self.consecutive_failures += 1

    def record_success(self) -> None:
        """Reset the counter and clear the outstanding-card guard."""
        self.consecutive_failures = 0
        self.first_failure_at = 0.0
        self.outstanding_item_id = None

    def record_skip(self) -> None:
        """Leave the counter and any outstanding card unchanged."""
        # A debounce skip is neither a failure nor a recovery.


def dedup_key(vault: str) -> str:
    """The attention store's re-post suppression key for one vault's stall card."""
    return f"git-ai-sync-{vault}-stalled"


def producer_id(vault: str) -> str:
    """The explicit producer id for one vault's stall card."""
    return f"git-ai-sync-{vault}"


def is_valid_item_id(candidate: str) -> bool:
    """True iff candidate is a 32-character lowercase-hex attention store item id."""
    return len(candidate) == ITEM_ID_HEX_LEN and all(c in _HEX_DIGITS for c in candidate)


def should_post_card(
    consecutive_failures: int, threshold: int, outstanding_item_id: str | None
) -> bool:
    """True iff the failure count has reached the threshold and no card is outstanding."""
    return consecutive_failures >= threshold and outstanding_item_id is None


def validate_attention_script(script_path: str) -> Path | None:
    """Return the resolved attention-ask script path, or warn once and return None.

    The script must be a regular, readable file. The executable bit is
    deliberately NOT required: every installed copy of the supervisor's
    attention-ask.py ships mode 100644 and every caller runs it through the
    interpreter, so an X_OK gate would silently disable alerting everywhere.
    """
    try:
        path = Path(script_path)
        if path.is_file() and os.access(path, os.R_OK):
            return path.resolve()
    except Exception as e:
        logger.warning(f"attention stall alert disabled: cannot use {script_path}: {e}")
        return None
    logger.warning(f"attention stall alert disabled: {script_path} is not a readable file")
    return None


def redact_error_summary(text: str) -> str:
    """Replace URL-shaped runs with <url> and truncate, so no credential reaches a card.

    Redaction happens before truncation, so a URL cannot survive as a fragment.
    """
    return _URL_RE.sub("<url>", text)[:CONTEXT_ERROR_MAX_CHARS]


def post_stall_card(
    script_path: Path,
    vault: str,
    repo_path: Path,
    failure_count: int,
    first_failure_at: float,
    last_error: str,
) -> str | None:
    """Post one stall card through the attention-ask script; return its item id or None.

    Returns the 32-hex item id on success, None on any failure (logged at
    WARNING). Never raises, never retries within the call.
    """
    first_failure_iso = datetime.fromtimestamp(first_failure_at, tz=UTC).isoformat()
    payload = f"{vault}: sync has failed {failure_count} consecutive cycles"
    context = (
        f"Repository {repo_path} has not synced since {first_failure_iso}. "
        f"Last error: {redact_error_summary(last_error)}"
    )
    argv = [
        sys.executable,
        str(script_path),
        "post",
        "--dedup-key",
        dedup_key(vault),
        "--producer-id",
        producer_id(vault),
        "--producer-kind",
        PRODUCER_KIND,
        "--payload",
        payload,
        "--context",
        context,
        "--option",
        _OPTION_ACTIONABLE,
        "--option",
        _OPTION_DEFERRING,
        "--recommend",
        _OPTION_ACTIONABLE,
    ]
    try:
        result = subprocess.run(
            argv,
            check=False,
            capture_output=True,
            text=True,
            timeout=metrics.PUSH_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired:
        logger.warning(
            f"stall card post timed out after {metrics.PUSH_TIMEOUT_SECONDS}s for {vault}"
        )
        return None
    except OSError as e:
        logger.warning(f"stall card post could not start for {vault}: {e}")
        return None

    if result.returncode != 0:
        logger.warning(
            f"stall card post failed for {vault}: exit code {result.returncode}; "
            f"output tail: {_output_tail(result)}"
        )
        return None

    item_id = _parse_item_id(result.stdout)
    if item_id is None:
        logger.warning(
            f"stall card posted for {vault} but the item id was unreadable; "
            f"output tail: {_output_tail(result)}"
        )
        return None
    return item_id


def close_stall_card(store_url: str, item_id: str) -> bool:
    """POST {store}/api/1.0/attention/{item_id}/close; return True on success.

    Best-effort: never raises, never retries. Any failure — an unreadable item
    id, an unreachable store, or a rejected close — is logged at WARNING and
    reported as False.
    """
    if not is_valid_item_id(item_id):
        logger.warning(f"stall card close skipped: {item_id!r} is not a 32-hex store id")
        return False

    url = f"{store_url.rstrip('/')}/api/1.0/attention/{item_id}/close"
    request = Request(url, method="POST")
    try:
        urlopen(request, timeout=metrics.PUSH_TIMEOUT_SECONDS)
    except HTTPError as e:
        logger.warning(f"stall card close rejected with HTTP {e.code} for {item_id}")
        return False
    except Exception as e:
        logger.warning(f"stall card close failed for {item_id}: {e}")
        return False
    return True


def _parse_item_id(stdout: str) -> str | None:
    """Extract the first valid item id from a script's stdout, or None."""
    for line in stdout.splitlines():
        if line.startswith(_ITEM_ID_PREFIX):
            candidate = line[len(_ITEM_ID_PREFIX) :].strip()
            if is_valid_item_id(candidate):
                return candidate
    return None


def _output_tail(result: subprocess.CompletedProcess[str]) -> str:
    """A bounded tail of the script's combined output, safe to log."""
    combined = (result.stdout or "") + (result.stderr or "")
    return combined[-_OUTPUT_TAIL_MAX_CHARS:].strip()
