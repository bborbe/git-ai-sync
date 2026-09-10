"""Per-vault macOS launchd agent setup and removal.

Generates and tears down launchd agents for the git-ai-sync watch command,
reproducing the frozen shape of the hand-written live plists: the label is
derived from the last path component of the vault directory, the program runs
``watch <vault-dir> --interval 30 --strategy merge`` with KeepAlive and
RunAtLoad, logs to ``/tmp/git-ai-sync-<label>.log``, applies the fixed soft and
hard resource limits, and passes the pushgateway environment variables through
only when set in the setup environment.
"""

import logging
import os
import plistlib
import re
import shutil
import subprocess
import sys
from collections.abc import Mapping
from pathlib import Path

logger = logging.getLogger(__name__)

_PUSHGATEWAY_ENV_KEYS = (
    "GIT_AI_SYNC_PUSHGATEWAY_URL",
    "GIT_AI_SYNC_PUSHGATEWAY_USERNAME",
    "GIT_AI_SYNC_PUSHGATEWAY_PASSWORD",
)


class LaunchdError(Exception):
    """Raised when a launchd setup or removal step fails."""


def derive_label(vault_dir: Path) -> str:
    """Derive the launchd label suffix from the last path component.

    The last component is lowercased and every run of non-alphanumeric
    characters is collapsed to a single hyphen, then leading/trailing hyphens
    are stripped. Because only the last component and an alphanumeric-only
    output are used, a hostile directory name cannot inject directory
    separators or plist syntax into the label.

    Args:
        vault_dir: Path to the vault directory

    Returns:
        Label suffix, e.g. ``Path("/Users/bborbe/Documents/Obsidian/Personal")``
        -> ``"personal"``
    """
    return re.sub(r"[^a-z0-9]+", "-", vault_dir.name.lower()).strip("-")


def resolve_binary(argv0: str | None = None) -> Path:
    """Resolve the git-ai-sync executable path for the launchd plist.

    Resolution order:
    1. If the invocation came from an explicit absolute binary path named
       ``git-ai-sync`` (e.g. the Homebrew cask postflight), return it. The
       ``name == "git-ai-sync"`` guard makes ``python -m git_ai_sync`` (argv0
       is the module file) fall through to PATH lookup.
    2. Otherwise look up ``git-ai-sync`` on PATH.
    3. Otherwise fall back to ``~/.local/bin/git-ai-sync`` (the ``uv tool
       install`` location).

    Args:
        argv0: Optional explicit invocation path; defaults to ``sys.argv[0]``

    Returns:
        Resolved absolute path to the git-ai-sync binary
    """
    argv0 = argv0 if argv0 else sys.argv[0]
    if Path(argv0).is_absolute() and Path(argv0).exists() and Path(argv0).name == "git-ai-sync":
        return Path(argv0).resolve()
    found = shutil.which("git-ai-sync")
    if found:
        return Path(found).resolve()
    return (Path.home() / ".local" / "bin" / "git-ai-sync").resolve()


def generate_plist(
    binary: Path,
    vault_dir: Path,
    label: str,
    env: Mapping[str, str] | None = None,
) -> dict[str, object]:
    """Generate the launchd plist dict reproducing the frozen live-plist shape.

    The caller passes ``vault_dir`` already resolved to an absolute path. The
    ``watch`` command and its defaults (``--interval 30 --strategy merge``) are
    hardcoded and deliberately not read from ``Config`` or the environment. The
    pushgateway environment variables are carried verbatim, each key present
    only when set in ``env``.

    Args:
        binary: Absolute path to the git-ai-sync binary
        vault_dir: Absolute path to the vault directory to watch
        label: Launchd label suffix (see :func:`derive_label`)
        env: Environment mapping; defaults to ``os.environ``

    Returns:
        The plist as a dict ready for ``plistlib`` serialization
    """
    env = env if env is not None else os.environ
    return {
        "Label": f"com.github.bborbe.git-ai-sync-{label}",
        "ProgramArguments": [
            str(binary),
            "watch",
            str(vault_dir),
            "--interval",
            "30",
            "--strategy",
            "merge",
        ],
        "KeepAlive": True,
        "RunAtLoad": True,
        "StandardOutPath": f"/tmp/git-ai-sync-{label}.log",
        "StandardErrorPath": f"/tmp/git-ai-sync-{label}.log",
        "SoftResourceLimits": {"NumberOfFiles": 1024, "ResidentSetSize": 536870912},
        "HardResourceLimits": {"NumberOfFiles": 2048, "ResidentSetSize": 1073741824},
        "EnvironmentVariables": {key: env[key] for key in _PUSHGATEWAY_ENV_KEYS if key in env},
    }


def write_plist(plist_path: Path, plist: dict[str, object]) -> None:
    """Write the plist atomically so a crash never leaves a truncated file.

    The plist is serialized with ``sort_keys=True`` for deterministic XML
    (byte-identical across re-runs) and written to a temp file in the same
    directory before ``os.replace``.

    Args:
        plist_path: Destination path for the plist file
        plist: Plist dict to serialize

    Raises:
        LaunchdError: If the plist cannot be written
    """
    try:
        plist_path.parent.mkdir(parents=True, exist_ok=True)
        data = plistlib.dumps(plist, sort_keys=True)
        tmp_path = plist_path.with_name(plist_path.name + ".tmp")
        tmp_path.write_bytes(data)
        os.replace(tmp_path, plist_path)
    except OSError as e:
        raise LaunchdError(f"Failed to write plist {plist_path}: {e}") from e


def setup_launchd(vault_dir: Path) -> None:
    """Install a launchd agent for the given vault directory.

    The vault directory must exist and be a directory before anything is
    written. The plist is written to ``~/Library/LaunchAgents/`` and the job is
    bootstrapped and kickstarted into the user's GUI domain. Re-runs converge:
    an already-loaded job is tolerated, and a plist left on disk after a
    failed bootstrap is inert.

    Args:
        vault_dir: Vault directory to watch

    Raises:
        LaunchdError: If any setup step fails, naming the failing step
    """
    vault_dir = vault_dir.resolve()
    if not vault_dir.is_dir():
        raise LaunchdError(
            "dir validation failed: vault directory must exist and be a directory"
            f" (got: {vault_dir})"
        )

    label = derive_label(vault_dir)
    binary = resolve_binary()
    logger.info(f"Resolved binary: {binary}; label: {label}")

    plist = generate_plist(binary, vault_dir, label)

    plist_path = (
        Path.home() / "Library" / "LaunchAgents" / f"com.github.bborbe.git-ai-sync-{label}.plist"
    )
    try:
        write_plist(plist_path, plist)
    except LaunchdError as e:
        raise LaunchdError(f"plist write failed: {e}") from e

    gui_domain = f"gui/{os.getuid()}"
    try:
        bootstrap = subprocess.run(
            ["launchctl", "bootstrap", gui_domain, str(plist_path)],
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except subprocess.TimeoutExpired as e:
        raise LaunchdError(f"launchctl failed (bootstrap timed out): {e}") from e

    if bootstrap.returncode != 0:
        try:
            probe = subprocess.run(
                ["launchctl", "print", f"{gui_domain}/{label}"],
                check=False,
                capture_output=True,
                text=True,
                timeout=30,
            )
        except subprocess.TimeoutExpired as e:
            raise LaunchdError(f"launchctl failed (print probe timed out): {e}") from e
        if probe.returncode != 0:
            raise LaunchdError(
                "launchctl bootstrap failed: "
                f"{bootstrap.stderr.strip()}\n"
                "Run manually:\n"
                f"  launchctl bootstrap {gui_domain} {plist_path}\n"
                f"  launchctl kickstart -k {gui_domain}/{label}"
            )
        logger.info(f"Agent already loaded: {gui_domain}/{label}")

    try:
        kickstart = subprocess.run(
            ["launchctl", "kickstart", "-k", f"{gui_domain}/{label}"],
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except subprocess.TimeoutExpired as e:
        raise LaunchdError(f"launchctl failed (kickstart timed out): {e}") from e

    if kickstart.returncode != 0:
        raise LaunchdError(
            f"launchctl kickstart failed for {gui_domain}/{label}: {kickstart.stderr.strip()}"
        )

    logger.info(f"Registered launchd agent: {gui_domain}/{label}")


def remove_launchd(vault_dir: Path) -> None:
    """Remove the launchd agent for the given vault directory (best-effort).

    No directory validation is performed: a never-set-up or non-existent vault
    directory still succeeds. The launchctl bootout is best-effort (any failure
    is logged as a warning and ignored); the plist is then deleted.

    Args:
        vault_dir: Vault directory whose agent should be removed

    Raises:
        LaunchdError: If an existing plist cannot be deleted, naming the
            "plist delete" step
    """
    label = derive_label(vault_dir)
    gui_domain = f"gui/{os.getuid()}"
    try:
        result = subprocess.run(
            ["launchctl", "bootout", gui_domain, label],
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except subprocess.TimeoutExpired as e:
        logger.warning(f"launchctl bootout timed out: {e}")
    else:
        if result.returncode != 0:
            logger.warning(
                f"launchctl bootout returned {result.returncode}: {result.stderr.strip()}"
            )

    plist_path = (
        Path.home() / "Library" / "LaunchAgents" / f"com.github.bborbe.git-ai-sync-{label}.plist"
    )
    try:
        plist_path.unlink(missing_ok=True)
    except OSError as e:
        raise LaunchdError(f"plist delete failed for {plist_path}: {e}") from e
