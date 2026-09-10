"""End-to-end tests for the setup-launchd and remove-launchd subcommands.

These tests drive the real CLI entry point (``python -m git_ai_sync``) against
scratch directories, with a scripted fake ``launchctl`` shimmed onto PATH and
``HOME`` pointed at a scratch dir so ``Path.home()`` resolves inside it. They
regression-protect the launchctl argv shape, idempotence, and failure/exit-code
behavior where unit tests (which mock ``subprocess.run``) cannot reach the
real subprocess boundary.
"""

import os
import plistlib
import subprocess
import sys
from pathlib import Path
from typing import NamedTuple

import pytest

_FAKE_LAUNCHCTL_SOURCE = r"""#!/usr/bin/env python3
import os
import sys

log = os.environ.get("FAKE_LAUNCHCTL_LOG")
if log:
    with open(log, "a", encoding="utf-8") as f:
        f.write(" ".join(sys.argv[1:]) + "\n")

command = sys.argv[1] if len(sys.argv) > 1 else ""
if os.environ.get("FAKE_LAUNCHCTL_FAIL_BOOTSTRAP") and command == "bootstrap":
    sys.exit(1)
if os.environ.get("FAKE_LAUNCHCTL_FAIL_PRINT") and command == "print":
    sys.exit(1)
# Real launchd addresses services by their full label: a print/kickstart/bootout
# target that is not a full label fails with "Could not find service".
if (
    command in ("print", "kickstart", "bootout")
    and "com.github.bborbe.git-ai-sync-" not in sys.argv[-1]
):
    sys.stderr.write(
        'Bad request. Could not find service "%s" in domain for user gui: %s\n'
        % (sys.argv[-1], os.getuid())
    )
    sys.exit(1)
sys.exit(0)
"""


_SHORT_LABEL = "personal"  # deliberately NOT the full label — the contract test probes it


class FakeLaunchctl(NamedTuple):
    """Locations of the fake launchctl shim and its invocation log."""

    shim: Path
    log: Path


@pytest.fixture
def fake_launchctl(tmp_path: Path) -> FakeLaunchctl:
    """Write an executable fake launchctl shim that logs each invocation."""
    shim = tmp_path / "bin"
    shim.mkdir()
    log = tmp_path / "launchctl.log"
    script = shim / "launchctl"
    script.write_text(_FAKE_LAUNCHCTL_SOURCE, encoding="utf-8")
    script.chmod(0o755)
    return FakeLaunchctl(shim=shim, log=log)


@pytest.fixture
def scratch(tmp_path: Path) -> Path:
    """Scratch root that doubles as HOME via the subprocess env; holds a vault."""
    (tmp_path / "vaults" / "Personal").mkdir(parents=True)
    return tmp_path


def run_cli(
    *args: str,
    scratch: Path,
    shim: Path,
    log: Path,
    extra_env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    """Run the real CLI against a scratch HOME with the fake launchctl on PATH."""
    env = {
        **os.environ,
        "HOME": str(scratch),
        "PATH": f"{shim}:{os.environ['PATH']}",
        "FAKE_LAUNCHCTL_LOG": str(log),
        **(extra_env or {}),
    }
    return subprocess.run(
        [sys.executable, "-m", "git_ai_sync", *args],
        capture_output=True,
        text=True,
        env=env,
        check=False,
    )


def _plist_path(scratch: Path, label: str) -> Path:
    """Path of the launchd plist for a label under the scratch HOME."""
    return scratch / "Library" / "LaunchAgents" / f"com.github.bborbe.git-ai-sync-{label}.plist"


def test_setup_writes_plist_and_bootstraps(scratch: Path, fake_launchctl: FakeLaunchctl) -> None:
    """setup-launchd writes the plist under the scratch HOME and boots the job."""
    vault = scratch / "vaults" / "Personal"
    proc = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert proc.returncode == 0, proc.stderr

    plist_path = _plist_path(scratch, "personal")
    assert plist_path.exists()
    decoded = plistlib.loads(plist_path.read_bytes())
    assert decoded["Label"] == "com.github.bborbe.git-ai-sync-personal"
    assert Path(decoded["ProgramArguments"][0]).is_absolute()
    assert decoded["ProgramArguments"][1] == "watch"
    assert decoded["ProgramArguments"][2] == str(vault.resolve())

    lines = fake_launchctl.log.read_text().splitlines()
    assert lines == [
        f"bootstrap gui/{os.getuid()} {plist_path}",
        f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal",
    ]


def test_setup_spaced_dir_derives_label_at_cli_layer(
    scratch: Path, fake_launchctl: FakeLaunchctl
) -> None:
    """A vault directory containing spaces derives a hyphenated label end-to-end."""
    spaced = scratch / "vaults" / "Path With.Spaces"
    spaced.mkdir()
    proc = run_cli(
        "setup-launchd",
        str(spaced),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert proc.returncode == 0, proc.stderr
    assert _plist_path(scratch, "path-with-spaces").exists()
    lines = fake_launchctl.log.read_text().splitlines()
    assert f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-path-with-spaces" in lines


def test_setup_rerun_is_identical_and_exits_0(scratch: Path, fake_launchctl: FakeLaunchctl) -> None:
    """Re-running setup is byte-identical and still exits 0."""
    vault = scratch / "vaults" / "Personal"
    first = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert first.returncode == 0, first.stderr
    plist_path = _plist_path(scratch, "personal")
    first_bytes = plist_path.read_bytes()

    second = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert second.returncode == 0, second.stderr
    assert plist_path.read_bytes() == first_bytes


def test_setup_tolerates_already_loaded_job(scratch: Path, fake_launchctl: FakeLaunchctl) -> None:
    """A failed bootstrap with a successful print probe is tolerated."""
    vault = scratch / "vaults" / "Personal"
    proc = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
        extra_env={"FAKE_LAUNCHCTL_FAIL_BOOTSTRAP": "1"},
    )
    assert proc.returncode == 0, proc.stderr
    assert _plist_path(scratch, "personal").exists()
    lines = fake_launchctl.log.read_text().splitlines()
    assert f"print gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal" in lines
    assert f"kickstart -k gui/{os.getuid()}/com.github.bborbe.git-ai-sync-personal" in lines


def test_setup_hard_launchd_failure_names_manual_commands(
    scratch: Path, fake_launchctl: FakeLaunchctl
) -> None:
    """A hard launchd failure exits non-zero and names the manual commands."""
    vault = scratch / "vaults" / "Personal"
    proc = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
        extra_env={"FAKE_LAUNCHCTL_FAIL_BOOTSTRAP": "1", "FAKE_LAUNCHCTL_FAIL_PRINT": "1"},
    )
    assert proc.returncode != 0
    output = proc.stdout + proc.stderr
    assert "launchctl bootstrap" in output
    assert "launchctl kickstart" in output


def test_setup_missing_dir_exits_nonzero_before_any_write(
    scratch: Path, fake_launchctl: FakeLaunchctl
) -> None:
    """A missing vault dir fails before writing anything or invoking launchctl."""
    proc = run_cli(
        "setup-launchd",
        str(scratch / "vaults" / "DoesNotExist"),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert proc.returncode != 0
    assert not (scratch / "Library" / "LaunchAgents").exists()
    assert not fake_launchctl.log.exists()


def test_remove_deletes_plist_and_bootouts(scratch: Path, fake_launchctl: FakeLaunchctl) -> None:
    """remove-launchd bootouts the job and deletes the plist after a setup."""
    vault = scratch / "vaults" / "Personal"
    setup = run_cli(
        "setup-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert setup.returncode == 0, setup.stderr
    plist_path = _plist_path(scratch, "personal")
    assert plist_path.exists()

    remove = run_cli(
        "remove-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert remove.returncode == 0, remove.stderr
    assert not plist_path.exists()
    lines = fake_launchctl.log.read_text().splitlines()
    assert lines[-1] == f"bootout gui/{os.getuid()} com.github.bborbe.git-ai-sync-personal"


def test_fake_launchctl_rejects_short_label(fake_launchctl: FakeLaunchctl) -> None:
    """The fake shim fails a short-label target exactly like real launchd."""
    proc = subprocess.run(
        [str(fake_launchctl.shim / "launchctl"), "print", f"gui/{os.getuid()}/{_SHORT_LABEL}"],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode != 0
    assert "Could not find service" in proc.stderr


def test_remove_never_setup_exits_0(scratch: Path, fake_launchctl: FakeLaunchctl) -> None:
    """remove-launchd on a never-set-up vault exits 0 without error text."""
    vault = scratch / "vaults" / "SomeVault"
    proc = run_cli(
        "remove-launchd",
        str(vault),
        scratch=scratch,
        shim=fake_launchctl.shim,
        log=fake_launchctl.log,
    )
    assert proc.returncode == 0, proc.stderr
    assert not _plist_path(scratch, "somevault").exists()
    output = proc.stdout + proc.stderr
    assert "error" not in output.lower()
