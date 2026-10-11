"""Shared test fixtures for git-ai-sync tests."""

import tempfile
import threading
import urllib.request
from collections.abc import Callable, Generator
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest


@pytest.fixture
def temp_dir() -> Generator[Path]:
    """Create temporary directory for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        yield Path(tmpdir)


@dataclass
class StubNotifier:
    """A stub standing in for the supervisor's attention-ask.py."""

    script: Path
    argv_log: Path

    def invocations(self) -> list[list[str]]:
        """Return one argv list per recorded invocation (empty when never invoked)."""
        if not self.argv_log.exists():
            return []
        groups: list[list[str]] = []
        for block in self.argv_log.read_text().split("===\n"):
            elements = [line for line in block.splitlines() if line]
            if elements:
                groups.append(elements)
        return groups


@pytest.fixture
def stub_notifier(tmp_path: Path) -> Callable[..., StubNotifier]:
    """Factory writing a stub attention-ask script, launched via the interpreter.

    The stub is deliberately left non-executable (mode 0644) because the real
    attention-ask.py ships mode 100644 and is always launched through the
    interpreter.
    """

    def make(
        item_id: str = "0" * 32,
        exit_code: int = 0,
        sleep_seconds: float = 0.0,
    ) -> StubNotifier:
        script = tmp_path / "stub-attention-ask.py"
        argv_log = tmp_path / "attention-argv.log"
        body = (
            "import sys\n"
            "\n"
            f'with open({str(argv_log)!r}, "a") as fh:\n'
            '    fh.write(sys.executable + "\\n")\n'
            "    for a in sys.argv:\n"
            '        fh.write(a + "\\n")\n'
            '    fh.write("===\\n")\n'
        )
        if sleep_seconds > 0:
            body += f"import time\ntime.sleep({sleep_seconds})\n"
        body += f'print("ITEM_ID: {item_id}")\n' if exit_code == 0 else 'print("FAILED: stub")\n'
        body += f"sys.exit({exit_code})\n"
        script.write_text(body)
        return StubNotifier(script=script, argv_log=argv_log)

    return make


@dataclass
class RecordingStore:
    """A local HTTP server standing in for the attention store."""

    url: str
    requests: list[tuple[str, str]] = field(default_factory=list)
    status: int = 200


@pytest.fixture
def recording_store(monkeypatch: pytest.MonkeyPatch) -> Generator[RecordingStore]:
    """A `http.server.HTTPServer` on port 0, served from a daemon thread.

    The ambient proxy environment (this container sets ``http_proxy``) is
    bypassed for the duration of the test: otherwise urllib routes even a
    127.0.0.1 request through the proxy, which answers 403 and the local server
    never sees the request.
    """
    store = RecordingStore(url="")
    monkeypatch.setattr(
        urllib.request, "_opener", urllib.request.build_opener(urllib.request.ProxyHandler({}))
    )

    class _Handler(BaseHTTPRequestHandler):
        def _record(self) -> None:
            store.requests.append((self.command, self.path))
            self.send_response(store.status)
            self.end_headers()

        def do_POST(self) -> None:
            self._record()

        def do_GET(self) -> None:
            self._record()

        def log_message(self, format: str, *args: object) -> None:
            """Silence the default stderr access log."""

    server = HTTPServer(("127.0.0.1", 0), _Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    store.url = f"http://127.0.0.1:{server.server_port}"
    try:
        yield store
    finally:
        server.shutdown()
        thread.join()
        server.server_close()
