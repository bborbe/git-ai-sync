"""Tests for the launchd module."""

import os
import plistlib
import subprocess
from pathlib import Path
from unittest.mock import patch

import pytest

from git_ai_sync.launchd import (
    LaunchdError,
    derive_label,
    generate_plist,
    remove_launchd,
    resolve_binary,
    setup_launchd,
    write_plist,
)

_BINARY = Path("/usr/local/bin/git-ai-sync")
_VAULT = Path("/Users/bborbe/Documents/Obsidian/Personal")


class _FakeResult:
    """Minimal stand-in for subprocess.CompletedProcess."""

    def __init__(self, returncode: int, stdout: str = "", stderr: str = "") -> None:
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


class TestDeriveLabel:
    @pytest.mark.parametrize(
        ("vault_dir", "expected"),
        [
            (Path("/Users/bborbe/Documents/Obsidian/Personal"), "personal"),
            (Path("/x/Path With.Spaces"), "path-with-spaces"),
            (Path("/x/y/Zoo"), "zoo"),
            (Path("/X/Y/ZooBAR"), "zoobar"),
            (Path("/Foo  Bar__Baz"), "foo-bar-baz"),
            (Path("/.hidden."), "hidden"),
        ],
    )
    def test_derive_label(self, vault_dir: Path, expected: str) -> None:
        assert derive_label(vault_dir) == expected


class TestResolveBinary:
    def test_absolute_existing_binary_returned(self, tmp_path: Path) -> None:
        binary = tmp_path / "git-ai-sync"
        binary.write_text("#!/bin/sh\n", encoding="utf-8")
        assert resolve_binary(str(binary)) == binary.resolve()

    def test_bare_name_uses_which(self, tmp_path: Path) -> None:
        found = tmp_path / "bin" / "git-ai-sync"
        found.parent.mkdir(parents=True)
        found.write_text("#!/bin/sh\n", encoding="utf-8")
        with patch("git_ai_sync.launchd.shutil.which", return_value=str(found)):
            assert resolve_binary("git-ai-sync") == found.resolve()

    def test_bare_name_falls_back_to_local_bin(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        monkeypatch.setenv("HOME", str(tmp_path))
        with patch("git_ai_sync.launchd.shutil.which", return_value=None):
            assert (
                resolve_binary("git-ai-sync")
                == (tmp_path / ".local" / "bin" / "git-ai-sync").resolve()
            )

    def test_python_dash_m_falls_through_to_which(self, tmp_path: Path) -> None:
        module_file = tmp_path / "__main__.py"
        module_file.write_text("", encoding="utf-8")
        found = tmp_path / "bin" / "git-ai-sync"
        found.parent.mkdir(parents=True)
        found.write_text("#!/bin/sh\n", encoding="utf-8")
        with patch("git_ai_sync.launchd.shutil.which", return_value=str(found)):
            assert resolve_binary(str(module_file)) == found.resolve()


class TestGeneratePlist:
    def test_full_plist_round_trip(self) -> None:
        env = {
            "GIT_AI_SYNC_PUSHGATEWAY_URL": "https://pushgateway.example.com",
            "GIT_AI_SYNC_PUSHGATEWAY_USERNAME": "monitoring",
            "GIT_AI_SYNC_PUSHGATEWAY_PASSWORD": "s3cret",
        }
        plist = generate_plist(_BINARY, _VAULT, "personal", env)
        decoded = plistlib.loads(plistlib.dumps(plist))
        assert decoded == {
            "Label": "com.github.bborbe.git-ai-sync-personal",
            "ProgramArguments": [
                str(_BINARY),
                "watch",
                str(_VAULT),
                "--interval",
                "30",
                "--strategy",
                "merge",
            ],
            "KeepAlive": True,
            "RunAtLoad": True,
            "StandardOutPath": "/tmp/git-ai-sync-personal.log",
            "StandardErrorPath": "/tmp/git-ai-sync-personal.log",
            "SoftResourceLimits": {"NumberOfFiles": 1024, "ResidentSetSize": 536870912},
            "HardResourceLimits": {"NumberOfFiles": 2048, "ResidentSetSize": 1073741824},
            "EnvironmentVariables": {
                "GIT_AI_SYNC_PUSHGATEWAY_URL": "https://pushgateway.example.com",
                "GIT_AI_SYNC_PUSHGATEWAY_USERNAME": "monitoring",
                "GIT_AI_SYNC_PUSHGATEWAY_PASSWORD": "s3cret",
            },
        }

    def test_env_subset_url_only(self) -> None:
        env = {"GIT_AI_SYNC_PUSHGATEWAY_URL": "https://pushgateway.example.com"}
        plist = generate_plist(_BINARY, _VAULT, "personal", env)
        assert plist["EnvironmentVariables"] == {
            "GIT_AI_SYNC_PUSHGATEWAY_URL": "https://pushgateway.example.com"
        }

    def test_env_none_set(self) -> None:
        plist = generate_plist(_BINARY, _VAULT, "personal", {})
        assert plist["EnvironmentVariables"] == {}

    def test_byte_identical_dumps(self) -> None:
        env = {"GIT_AI_SYNC_PUSHGATEWAY_URL": "https://pushgateway.example.com"}
        plist1 = generate_plist(_BINARY, _VAULT, "personal", env)
        plist2 = generate_plist(_BINARY, _VAULT, "personal", env)
        assert plistlib.dumps(plist1, sort_keys=True) == plistlib.dumps(plist2, sort_keys=True)

    def test_write_plist_idempotent(self, tmp_path: Path) -> None:
        plist = generate_plist(_BINARY, _VAULT, "personal", {})
        target = tmp_path / "LaunchAgents" / "com.github.bborbe.git-ai-sync-personal.plist"
        write_plist(target, plist)
        first = target.read_bytes()
        write_plist(target, plist)
        assert target.read_bytes() == first

    def test_write_plist_failure_raises(self, tmp_path: Path) -> None:
        blocker = tmp_path / "blocker"
        blocker.write_text("x", encoding="utf-8")
        target = blocker / "LaunchAgents" / "x.plist"
        with pytest.raises(LaunchdError) as exc_info:
            write_plist(target, {})
        assert str(target) in str(exc_info.value)


def _patch_subprocess_run(
    monkeypatch: pytest.MonkeyPatch,
    calls: list[list[str]],
    *,
    bootstrap: int = 0,
    probe: int = 0,
    kickstart: int = 0,
    bootout: int = 0,
) -> None:
    """Monkeypatch launchd.subprocess.run, recording argv and mapping commands to returncodes."""

    def fake_run(argv: list[str], **kwargs: object) -> _FakeResult:
        calls.append(argv)
        command = argv[1]
        if command == "bootstrap":
            return _FakeResult(bootstrap, stderr="bootstrap failed" if bootstrap else "")
        if command == "print":
            return _FakeResult(probe)
        if command == "kickstart":
            return _FakeResult(kickstart)
        if command == "bootout":
            return _FakeResult(bootout, stderr="no such process" if bootout else "")
        return _FakeResult(0)

    monkeypatch.setattr("git_ai_sync.launchd.subprocess.run", fake_run)


class TestSetupLaunchd:
    def test_happy_path(self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls)

        setup_launchd(vault_dir)

        plist_path = home / "Library" / "LaunchAgents" / "com.github.bborbe.git-ai-sync-vault.plist"
        assert plist_path.exists()
        decoded = plistlib.loads(plist_path.read_bytes())
        assert decoded["Label"] == "com.github.bborbe.git-ai-sync-vault"
        assert calls[0] == ["launchctl", "bootstrap", f"gui/{os.getuid()}", str(plist_path)]
        assert calls[1] == ["launchctl", "kickstart", "-k", f"gui/{os.getuid()}/vault"]
        assert len(calls) == 2

    def test_already_loaded_tolerated(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls, bootstrap=1, probe=0)

        setup_launchd(vault_dir)  # no exception

        assert calls[1] == ["launchctl", "print", f"gui/{os.getuid()}/vault"]
        assert calls[2] == ["launchctl", "kickstart", "-k", f"gui/{os.getuid()}/vault"]

    def test_hard_launchd_failure(self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls, bootstrap=1, probe=1)

        with pytest.raises(LaunchdError) as exc_info:
            setup_launchd(vault_dir)

        message = str(exc_info.value)
        plist_path = home / "Library" / "LaunchAgents" / "com.github.bborbe.git-ai-sync-vault.plist"
        assert f"launchctl bootstrap gui/{os.getuid()} {plist_path}" in message
        assert f"launchctl kickstart -k gui/{os.getuid()}/vault" in message

    def test_missing_dir_validated_first(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls)

        with pytest.raises(LaunchdError) as exc_info:
            setup_launchd(tmp_path / "missing")

        assert "dir validation" in str(exc_info.value)
        assert calls == []
        assert not (home / "Library" / "LaunchAgents").exists()

    def test_plain_file_dir_validated_first(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        plain_file = tmp_path / "notadir"
        plain_file.write_text("x", encoding="utf-8")
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls)

        with pytest.raises(LaunchdError) as exc_info:
            setup_launchd(plain_file)

        assert "dir validation" in str(exc_info.value)
        assert calls == []
        assert not (home / "Library" / "LaunchAgents").exists()

    def test_plist_write_failure_names_step(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        with (
            patch(
                "git_ai_sync.launchd.write_plist",
                side_effect=LaunchdError("boom"),
            ),
            pytest.raises(LaunchdError) as exc_info,
        ):
            setup_launchd(vault_dir)
        assert "plist write" in str(exc_info.value)

    def test_bootstrap_timeout_raises_launchd_error(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))

        def fake_run(argv: list[str], **kwargs: object) -> _FakeResult:
            raise subprocess.TimeoutExpired(cmd=argv, timeout=30)

        monkeypatch.setattr("git_ai_sync.launchd.subprocess.run", fake_run)

        with pytest.raises(LaunchdError) as exc_info:
            setup_launchd(vault_dir)
        assert "launchctl" in str(exc_info.value)

    def test_kickstart_failure_raises_launchd_error(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        vault_dir = tmp_path / "vault"
        vault_dir.mkdir()
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls, kickstart=1)

        with pytest.raises(LaunchdError) as exc_info:
            setup_launchd(vault_dir)
        assert "launchctl kickstart" in str(exc_info.value)


class TestRemoveLaunchd:
    def test_removes_job_and_plist(self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        plist_path = home / "Library" / "LaunchAgents" / "com.github.bborbe.git-ai-sync-vault.plist"
        plist_path.parent.mkdir(parents=True)
        plist_path.write_text("<plist/>", encoding="utf-8")
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls)

        remove_launchd(tmp_path / "vault")

        assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "vault"]]
        assert not plist_path.exists()

    def test_bootout_nonzero_best_effort(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls, bootout=1)

        remove_launchd(tmp_path / "vault")  # no exception

        assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "vault"]]
        assert not (home / "Library" / "LaunchAgents").exists()

    def test_never_setup_no_exception(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls, bootout=1)

        remove_launchd(tmp_path / "vault")  # no exception

        assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "vault"]]

    def test_plist_delete_failure_names_step(
        self, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
    ) -> None:
        home = tmp_path / "home"
        monkeypatch.setenv("HOME", str(home))
        plist_path = home / "Library" / "LaunchAgents" / "com.github.bborbe.git-ai-sync-vault.plist"
        plist_path.mkdir(parents=True)  # a directory cannot be unlinked
        calls: list[list[str]] = []
        _patch_subprocess_run(monkeypatch, calls)

        with pytest.raises(LaunchdError) as exc_info:
            remove_launchd(tmp_path / "vault")

        assert "plist delete" in str(exc_info.value)
        assert calls == [["launchctl", "bootout", f"gui/{os.getuid()}", "vault"]]
