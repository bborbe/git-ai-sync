package launchd

import (
	"bytes"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bborbe/git-ai-sync/internal/config"
)

func testOptions(home string) Options {
	return Options{
		HomeDir:    home,
		BinaryPath: "/tmp/bin/git-ai-sync",
		UID:        501,
		Config: config.FileConfig{
			ConfigPath: "/tmp/config.yaml",
			Pushgateway: config.Pushgateway{
				URL:      "https://pg",
				Username: "u",
				Password: "super-secret",
			},
		},
	}
}

func TestRenderPlistShape(t *testing.T) {
	home := t.TempDir()
	data, err := RenderPlist(testOptions(home))
	if err != nil {
		t.Fatalf("RenderPlist: %v", err)
	}
	document := string(data)

	assertContains(t, document, Label)
	assertInOrder(t, document,
		"<string>/tmp/bin/git-ai-sync</string>",
		"<string>supervise</string>",
		"<string>--config</string>",
		"<string>/tmp/config.yaml</string>",
	)
	assertContains(t, document, "<key>KeepAlive</key>")
	assertContains(t, document, "<true/>")
	assertContains(t, document, "<key>RunAtLoad</key>")
	assertContains(t, document, "<key>ThrottleInterval</key>")
	assertContains(t, document, "<integer>30</integer>")
	assertContains(t, document, "<key>ProcessType</key>")
	assertContains(t, document, "<string>Standard</string>")
	assertContains(t, document, "<key>NumberOfFiles</key>")
	assertContains(t, document, "<integer>4096</integer>")
	assertContains(t, document, "<integer>8192</integer>")
	assertContains(t, document, "<key>PATH</key>")
	assertContains(t, document, "/usr/local/bin")
	assertContains(t, document, "<key>StandardOutPath</key>")
	assertContains(t, document, "<key>StandardErrorPath</key>")
	logPath := filepath.Join(home, "Library", "Logs", "git-ai-sync", "git-ai-sync.log")
	if got := strings.Count(document, logPath); got != 2 {
		t.Errorf("log path appears %d times, want 2 (stdout and stderr): %q", got, document)
	}

	for _, forbidden := range []string{"NumberOfProcesses", "ResidentSetSize", "super-secret"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("plist contains forbidden %q:\n%s", forbidden, document)
		}
	}

	assertWellFormedXML(t, data)
}

func TestRenderPlistDeterministic(t *testing.T) {
	home := t.TempDir()
	first, err := RenderPlist(testOptions(home))
	if err != nil {
		t.Fatalf("RenderPlist first: %v", err)
	}
	second, err := RenderPlist(testOptions(home))
	if err != nil {
		t.Fatalf("RenderPlist second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("plist renders differ:\n%s\n---\n%s", first, second)
	}
}

func TestPaths(t *testing.T) {
	home := t.TempDir()
	if got, want := PlistPath(home), filepath.Join(home, "Library", "LaunchAgents", Label+".plist"); got != want {
		t.Errorf("PlistPath = %q, want %q", got, want)
	}
	if got, want := LogDir(home), filepath.Join(home, "Library", "Logs", "git-ai-sync"); got != want {
		t.Errorf("LogDir = %q, want %q", got, want)
	}
	if got, want := LogPath(home), filepath.Join(home, "Library", "Logs", "git-ai-sync", "git-ai-sync.log"); got != want {
		t.Errorf("LogPath = %q, want %q", got, want)
	}
}

func TestRenderPlistEscapesValues(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.Config.ConfigPath = "/tmp/a&b/config.yaml"
	data, err := RenderPlist(opts)
	if err != nil {
		t.Fatalf("RenderPlist: %v", err)
	}
	if !strings.Contains(string(data), "a&amp;b/config.yaml") {
		t.Errorf("plist does not escape the config path:\n%s", data)
	}
	assertWellFormedXML(t, data)
}

func TestRenderPlistRequiresPaths(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.BinaryPath = ""
	if _, err := RenderPlist(opts); err == nil {
		t.Error("RenderPlist accepted an empty binary path")
	}
	opts = testOptions(t.TempDir())
	opts.Config.ConfigPath = ""
	if _, err := RenderPlist(opts); err == nil {
		t.Error("RenderPlist accepted an empty config path")
	}
}

func assertContains(t *testing.T, document string, needle string) {
	t.Helper()
	if !strings.Contains(document, needle) {
		t.Errorf("plist does not contain %q:\n%s", needle, document)
	}
}

func assertInOrder(t *testing.T, document string, needles ...string) {
	t.Helper()
	previous := -1
	for _, needle := range needles {
		index := strings.Index(document, needle)
		if index < 0 {
			t.Fatalf("plist does not contain %q:\n%s", needle, document)
		}
		if index <= previous {
			t.Errorf("%q does not follow the previous ProgramArguments entry", needle)
		}
		previous = index
	}
}

func assertWellFormedXML(t *testing.T, data []byte) {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				return
			}
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
	}
}
