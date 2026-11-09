package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)

	logger.Info("visible message")
	logger.Debug("hidden message")

	out := buf.String()
	if !strings.Contains(out, "visible message") {
		t.Fatalf("info record missing from output: %q", out)
	}
	if strings.Contains(out, "hidden message") {
		t.Fatalf("debug record should be suppressed: %q", out)
	}
}
