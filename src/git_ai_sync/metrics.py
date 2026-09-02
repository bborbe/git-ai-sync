"""Prometheus pushgateway metrics for git-ai-sync watch health."""

import base64
import logging
import time
from pathlib import Path
from urllib.parse import quote
from urllib.request import Request, urlopen

logger = logging.getLogger(__name__)

JOB = "git_ai_sync"
HEARTBEAT_METRIC = "git_ai_sync_heartbeat_timestamp"
LAST_SUCCESS_METRIC = "git_ai_sync_last_success_timestamp"
PUSH_TIMEOUT_SECONDS = 5


def vault_label(repo_path: Path) -> str:
    """Stable, human-readable vault label: the repository directory name, not the full path."""
    return repo_path.name


def push_heartbeat(
    pushgateway_url: str,
    username: str | None,
    password: str | None,
    repo_path: Path,
) -> bool:
    """Push git_ai_sync_heartbeat_timestamp to the pushgateway.

    Returns True on success, False on any failure (logged at WARNING). Never raises.
    """
    return _push_metric(
        pushgateway_url, username, password, HEARTBEAT_METRIC, vault_label(repo_path)
    )


def push_last_success(
    pushgateway_url: str,
    username: str | None,
    password: str | None,
    repo_path: Path,
) -> bool:
    """Push git_ai_sync_last_success_timestamp to the pushgateway.

    Returns True on success, False on any failure (logged at WARNING). Never raises.
    """
    return _push_metric(
        pushgateway_url, username, password, LAST_SUCCESS_METRIC, vault_label(repo_path)
    )


def _push_metric(
    pushgateway_url: str,
    username: str | None,
    password: str | None,
    metric_name: str,
    vault: str,
) -> bool:
    """Push a single gauge metric to the pushgateway under the instance grouping key.

    The vault label lives only in the body (never the URL path) to avoid the
    pushgateway's label-collision rejection. Never raises: any failure is logged
    at WARNING and reported via the return value.
    """
    base = pushgateway_url.rstrip("/")
    url = f"{base}/metrics/job/{JOB}/instance/{quote(vault, safe='')}"

    escaped_vault = vault.replace("\\", "\\\\").replace('"', '\\"')
    body = (
        f'# TYPE {metric_name} gauge\n{metric_name}{{vault="{escaped_vault}"}} {int(time.time())}\n'
    )

    headers = {"Content-Type": "text/plain; version=0.0.4"}
    if username is not None:
        token = base64.b64encode(f"{username}:{password}".encode()).decode()
        headers["Authorization"] = f"Basic {token}"

    request = Request(url, data=body.encode("utf-8"), headers=headers, method="PUT")
    try:
        urlopen(request, timeout=PUSH_TIMEOUT_SECONDS)
    except Exception as e:
        logger.warning(f"metric push failed for {metric_name}: {e}")
        return False
    return True
