"""Filesystem watcher for git repositories."""

import logging
import threading
import time
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING

from watchdog.events import FileSystemEvent, FileSystemEventHandler
from watchdog.observers import Observer

if TYPE_CHECKING:
    from watchdog.observers.api import BaseObserver

logger = logging.getLogger(__name__)


class ChangeTracker:
    """Tracks last file change time to gate sync operations.

    Watches filesystem and records when files change. Used to skip sync
    during active editing (if changes happened recently).
    """

    def __init__(self, watch_path: Path):
        """Initialize change tracker.

        Args:
            watch_path: Directory to watch
        """
        self.watch_path = watch_path
        self._observer: BaseObserver | None = None
        self._last_change_time: float = 0.0
        self._lock = threading.Lock()

    def _on_file_change(self) -> None:
        """Handle file change event - update last change time."""
        with self._lock:
            self._last_change_time = time.time()
            logger.debug("File change detected")

    def get_seconds_since_last_change(self) -> float:
        """Get seconds since last file change.

        Returns:
            Seconds since last change, or infinity if no changes detected
        """
        with self._lock:
            if self._last_change_time == 0.0:
                return float("inf")
            return time.time() - self._last_change_time

    def start(self) -> None:
        """Start watching filesystem."""
        handler = _ChangeEventHandler(self._on_file_change)
        self._observer = Observer()
        self._observer.schedule(handler, str(self.watch_path), recursive=True)
        self._observer.start()
        logger.info(f"Started watching: {self.watch_path}")

    def stop(self) -> None:
        """Stop watching."""
        if self._observer:
            self._observer.stop()
            self._observer.join()
            logger.info("Stopped watching")


class _ChangeEventHandler(FileSystemEventHandler):
    """Filesystem event handler that triggers callback on any change."""

    def __init__(self, on_change: Callable[[], None]):
        self.on_change = on_change
        # Track last event to avoid duplicate events
        self._last_event: tuple[str, float] | None = None

    def _should_ignore(self, event: FileSystemEvent) -> bool:
        """Check if event should be ignored."""
        # Handle both str and bytes from watchdog
        src_path = event.src_path
        path = src_path.decode("utf-8") if isinstance(src_path, bytes) else src_path

        # Ignore directories
        if event.is_directory:
            return True

        # Ignore git internal files
        if "/.git/" in path or path.endswith("/.git"):
            return True

        # Ignore common editor temp files
        if path.endswith("~") or path.endswith(".swp") or path.endswith(".tmp"):
            return True

        # Ignore hidden files (like .DS_Store)
        return Path(path).name.startswith(".")

    def _handle_event(self, event: FileSystemEvent) -> None:
        """Handle any filesystem event."""
        if self._should_ignore(event):
            return

        # Trigger callback
        src_path = event.src_path
        path_str = src_path.decode("utf-8") if isinstance(src_path, bytes) else src_path
        logger.debug(f"File changed: {path_str}")
        self.on_change()

    def on_modified(self, event: FileSystemEvent) -> None:
        """File modified."""
        self._handle_event(event)

    def on_created(self, event: FileSystemEvent) -> None:
        """File created."""
        self._handle_event(event)

    def on_deleted(self, event: FileSystemEvent) -> None:
        """File deleted."""
        self._handle_event(event)

    def on_moved(self, event: FileSystemEvent) -> None:
        """File moved/renamed."""
        self._handle_event(event)
