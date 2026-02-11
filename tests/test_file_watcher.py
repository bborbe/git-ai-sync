"""Tests for filesystem watcher module."""

import math
from pathlib import Path
from unittest.mock import MagicMock, patch

from watchdog.events import DirModifiedEvent, FileModifiedEvent

from git_ai_sync.file_watcher import ChangeTracker, _ChangeEventHandler


class TestChangeEventHandlerIgnore:
    def test_ignore_directory_event(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = DirModifiedEvent(src_path="/repo/somedir")
        handler.on_modified(event)
        callback.assert_not_called()

    def test_ignore_git_files(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/.git/index")
        handler.on_modified(event)
        callback.assert_not_called()

    def test_ignore_swp_files(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/file.swp")
        handler.on_modified(event)
        callback.assert_not_called()

    def test_ignore_tilde_files(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/file.txt~")
        handler.on_modified(event)
        callback.assert_not_called()

    def test_ignore_tmp_files(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/file.tmp")
        handler.on_modified(event)
        callback.assert_not_called()

    def test_ignore_hidden_files(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/.DS_Store")
        handler.on_modified(event)
        callback.assert_not_called()


class TestChangeEventHandlerAccept:
    def test_handles_normal_file(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/notes.md")
        handler.on_modified(event)
        callback.assert_called_once()

    def test_handles_bytes_path(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path=b"/repo/notes.md")
        handler.on_modified(event)
        callback.assert_called_once()

    def test_on_created_triggers(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/new.md")
        handler.on_created(event)
        callback.assert_called_once()

    def test_on_deleted_triggers(self) -> None:
        callback = MagicMock()
        handler = _ChangeEventHandler(callback)
        event = FileModifiedEvent(src_path="/repo/old.md")
        handler.on_deleted(event)
        callback.assert_called_once()


class TestChangeTracker:
    def test_initial_seconds_since_change_inf(self, temp_dir: Path) -> None:
        tracker = ChangeTracker(temp_dir)
        assert math.isinf(tracker.get_seconds_since_last_change())

    def test_seconds_since_change_after_event(self, temp_dir: Path) -> None:
        tracker = ChangeTracker(temp_dir)
        tracker._on_file_change()
        assert tracker.get_seconds_since_last_change() < 1.0

    def test_start_creates_observer(self, temp_dir: Path) -> None:
        tracker = ChangeTracker(temp_dir)
        with patch("git_ai_sync.file_watcher.Observer") as mock_observer_cls:
            mock_observer = MagicMock()
            mock_observer_cls.return_value = mock_observer
            tracker.start()
            mock_observer.schedule.assert_called_once()
            mock_observer.start.assert_called_once()

    def test_stop_stops_observer(self, temp_dir: Path) -> None:
        tracker = ChangeTracker(temp_dir)
        mock_observer = MagicMock()
        tracker._observer = mock_observer
        tracker.stop()
        mock_observer.stop.assert_called_once()
        mock_observer.join.assert_called_once()

    def test_stop_without_start_does_nothing(self, temp_dir: Path) -> None:
        tracker = ChangeTracker(temp_dir)
        tracker.stop()  # Should not raise
