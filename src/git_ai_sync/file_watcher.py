"""Debounced filesystem watcher for git repositories."""

import logging
import threading
from collections.abc import Callable
from pathlib import Path
from typing import TYPE_CHECKING

from watchdog.events import FileSystemEvent, FileSystemEventHandler
from watchdog.observers import Observer

if TYPE_CHECKING:
    from watchdog.observers.api import BaseObserver

logger = logging.getLogger(__name__)


class DebouncedWatcher:
    """Watches filesystem and triggers callback after debounce period of no changes.

    When a file changes, starts/resets a timer. Only calls the callback when
    the timer expires without any new changes (debounce period elapsed).

    This prevents rapid callbacks during active editing sessions.
    """

    def __init__(
        self,
        watch_path: Path,
        callback: Callable[[], None],
        debounce_seconds: float = 30.0,
    ):
        """Initialize debounced watcher.

        Args:
            watch_path: Directory to watch
            callback: Function to call after debounce period
            debounce_seconds: Seconds of no changes before triggering callback
        """
        self.watch_path = watch_path
        self.callback = callback
        self.debounce_seconds = debounce_seconds

        self._observer: BaseObserver | None = None
        self._timer: threading.Timer | None = None
        self._timer_lock = threading.Lock()
        self._stopped = False

    def _on_file_change(self) -> None:
        """Handle file change event - reset debounce timer."""
        with self._timer_lock:
            # Cancel existing timer
            if self._timer and self._timer.is_alive():
                self._timer.cancel()

            if self._stopped:
                return

            # Start new timer
            self._timer = threading.Timer(self.debounce_seconds, self._on_timer_expired)
            self._timer.daemon = True
            self._timer.start()
            logger.debug(f"Timer reset: {self.debounce_seconds}s until sync")

    def _on_timer_expired(self) -> None:
        """Timer expired - trigger callback."""
        if not self._stopped:
            logger.info("Debounce period elapsed, triggering sync")
            try:
                self.callback()
            except Exception as e:
                logger.error(f"Callback failed: {e}", exc_info=True)

    def start(self) -> None:
        """Start watching filesystem."""
        handler = _ChangeEventHandler(self._on_file_change)
        self._observer = Observer()
        self._observer.schedule(handler, str(self.watch_path), recursive=True)
        self._observer.start()
        logger.info(f"Started watching: {self.watch_path}")

        # Initial trigger after debounce period (if files exist)
        self._on_file_change()

    def stop(self) -> None:
        """Stop watching and cancel pending timer."""
        self._stopped = True

        with self._timer_lock:
            if self._timer and self._timer.is_alive():
                self._timer.cancel()

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
