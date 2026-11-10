package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	stderrors "errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	assertContains(t, document, "<integer>65536</integer>")
	assertContains(t, document, "<integer>245760</integer>")
	assertInOrder(t, document,
		"<key>SoftResourceLimits</key>",
		"<integer>65536</integer>",
		"<key>HardResourceLimits</key>",
		"<integer>245760</integer>",
	)
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

func TestResolveBinaryPath(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatalf("create real directory: %v", err)
		}
		realFile := filepath.Join(realDir, "x")
		if err := os.WriteFile(realFile, nil, 0o755); err != nil {
			t.Fatalf("create real file: %v", err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatalf("create symlink: %v", err)
		}

		launchPath := filepath.Join(link, "x")
		if got := resolveBinaryPath(launchPath); got != launchPath {
			t.Errorf("resolveBinaryPath(%q) = %q, want the launch path %q", launchPath, got, launchPath)
		}
		if got := resolveBinaryPath(launchPath); got == realFile {
			t.Errorf("resolveBinaryPath(%q) = %q, want the link, not its target", launchPath, got)
		}
	})

	t.Run("plain-path", func(t *testing.T) {
		// /tmp is a symlink to /private/tmp on macOS, so this case
		// discriminates there; on Linux (CI runs on ubuntu-latest, where
		// /tmp is a real directory) it only proves the value is returned
		// unchanged.
		file, err := os.CreateTemp("/tmp", "git-ai-sync-launch-path-*")
		if err != nil {
			t.Fatalf("create temp file under /tmp: %v", err)
		}
		name := file.Name()
		if err := file.Close(); err != nil {
			t.Fatalf("close temp file: %v", err)
		}
		defer func() { _ = os.Remove(name) }()

		if got := resolveBinaryPath(name); !strings.HasPrefix(got, "/tmp/") {
			t.Errorf("resolveBinaryPath(%q) = %q, want it to still begin /tmp/", name, got)
		}
	})
}

func TestRenderPlistKeepsLaunchPath(t *testing.T) {
	opts := testOptions(t.TempDir())
	launchPath := "/opt/homebrew/bin/git-ai-sync"
	target := "/opt/homebrew/Caskroom/git-ai-sync/0.18.0/git-ai-sync"
	opts.BinaryPath = launchPath

	data, err := RenderPlist(opts)
	if err != nil {
		t.Fatalf("RenderPlist: %v", err)
	}
	document := string(data)

	assertContains(t, document, "<string>"+launchPath+"</string>")
	if strings.Contains(document, target) {
		t.Errorf("plist contains the link target %q instead of only the launch path:\n%s", target, document)
	}
	assertInOrder(t, document,
		"<key>ProgramArguments</key>",
		"<string>"+launchPath+"</string>",
		"<string>supervise</string>",
		"<string>--config</string>",
		"<string>"+opts.Config.ConfigPath+"</string>",
	)
}

func TestResolveOptions(t *testing.T) {
	t.Run("binarypath-override", func(t *testing.T) {
		linkPath := "/opt/homebrew/bin/git-ai-sync"
		opts, err := ResolveOptions(context.Background(), Options{
			HomeDir:    t.TempDir(),
			UID:        501,
			BinaryPath: linkPath,
		})
		if err != nil {
			t.Fatalf("ResolveOptions: %v", err)
		}
		if opts.BinaryPath != linkPath {
			t.Errorf("BinaryPath = %q, want the caller-supplied %q", opts.BinaryPath, linkPath)
		}
	})
}

func writeLaunchctlStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create launchctl bin directory: %v", err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create launchctl state directory: %v", err)
	}
	logPath := filepath.Join(dir, "launchctl.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("create launchctl log: %v", err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logPath + "\"\n" +
		body
	if err := os.WriteFile(filepath.Join(binDir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write launchctl stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LAUNCHCTL_STATE", stateDir)
	return logPath
}

func launchctlCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}
	var calls []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

// installOptions returns Options pointing at a real 0600 config file so
// config.CheckPermissions passes, with a discard logger.
func installOptions(t *testing.T, home string) Options {
	t.Helper()
	configPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(configPath, []byte("vaults: []\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatalf("chmod config file: %v", err)
	}
	opts := testOptions(home)
	opts.Config.ConfigPath = configPath
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return opts
}

// print exits non-zero from its third invocation onward; bootstrap succeeds.
const raceBody = "" +
	"if [ \"$1\" = \"print\" ]; then\n" +
	"  n=0\n" +
	"  if [ -f \"$LAUNCHCTL_STATE/print-count\" ]; then n=$(cat \"$LAUNCHCTL_STATE/print-count\"); fi\n" +
	"  n=$((n+1))\n" +
	"  echo \"$n\" > \"$LAUNCHCTL_STATE/print-count\"\n" +
	"  if [ \"$n\" -ge 3 ]; then exit 1; fi\n" +
	"fi\n" +
	"exit 0\n"

// print reports absent; the first bootstrap exits 5, the second exits 0.
const bootstrapFailOnceBody = "" +
	"if [ \"$1\" = \"print\" ]; then exit 1; fi\n" +
	"if [ \"$1\" = \"bootstrap\" ]; then\n" +
	"  n=0\n" +
	"  if [ -f \"$LAUNCHCTL_STATE/bootstrap-count\" ]; then n=$(cat \"$LAUNCHCTL_STATE/bootstrap-count\"); fi\n" +
	"  n=$((n+1))\n" +
	"  echo \"$n\" > \"$LAUNCHCTL_STATE/bootstrap-count\"\n" +
	"  if [ \"$n\" -eq 1 ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
	"fi\n" +
	"exit 0\n"

// print reports absent; every bootstrap exits 5.
const bootstrapAlwaysFailsBody = "" +
	"if [ \"$1\" = \"print\" ]; then exit 1; fi\n" +
	"if [ \"$1\" = \"bootstrap\" ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
	"exit 0\n"

// print always reports loaded; bootstrap always exits 5 (never reached).
const printLoadedBootstrapFailsBody = "" +
	"if [ \"$1\" = \"print\" ]; then exit 0; fi\n" +
	"if [ \"$1\" = \"bootstrap\" ]; then echo \"Bootstrap failed: 5: Input/output error\"; exit 5; fi\n" +
	"exit 0\n"

func TestInstallWaitsForTeardownThenBootstraps(t *testing.T) {
	home := t.TempDir()
	logPath := writeLaunchctlStub(t, raceBody)

	if err := Install(context.Background(), installOptions(t, home)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	calls := launchctlCalls(t, logPath)
	bootoutIndex, firstPrint, lastPrint, bootstrapIndex := -1, -1, -1, -1
	printCount := 0
	for index, call := range calls {
		if strings.HasPrefix(call, "bootout ") && bootoutIndex < 0 {
			bootoutIndex = index
		}
		if strings.HasPrefix(call, "print ") {
			printCount++
			if firstPrint < 0 {
				firstPrint = index
			}
			lastPrint = index
		}
		if strings.HasPrefix(call, "bootstrap ") && bootstrapIndex < 0 {
			bootstrapIndex = index
		}
	}
	if bootoutIndex < 0 {
		t.Fatalf("no bootout call: %q", calls)
	}
	if bootstrapIndex < 0 {
		t.Fatalf("no bootstrap call: %q", calls)
	}
	if printCount < 3 {
		t.Errorf("print call count = %d, want at least 3: %q", printCount, calls)
	}
	if !(bootoutIndex < firstPrint && firstPrint <= lastPrint && lastPrint < bootstrapIndex) {
		t.Errorf("call order is not bootout < print... < bootstrap: %q", calls)
	}
}

func TestUninstallThenInstallConverges(t *testing.T) {
	home := t.TempDir()
	logPath := writeLaunchctlStub(t, raceBody)
	opts := installOptions(t, home)

	plistPath := PlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("create launch agent directory: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed stale plist: %v", err)
	}

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	before := len(launchctlCalls(t, logPath))

	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("Install: %v", err)
	}

	installLeg := launchctlCalls(t, logPath)[before:]
	if len(installLeg) == 0 {
		t.Fatal("install leg recorded no launchctl calls")
	}
	if !strings.HasPrefix(installLeg[0], "bootout ") {
		t.Errorf("install leg does not start with bootout: %q", installLeg)
	}
	if !strings.HasPrefix(installLeg[len(installLeg)-1], "bootstrap ") {
		t.Errorf("install leg does not end with bootstrap: %q", installLeg)
	}
	printCount, bootstrapCount := 0, 0
	for _, call := range installLeg {
		if strings.HasPrefix(call, "print ") {
			printCount++
		}
		if strings.HasPrefix(call, "bootstrap ") {
			bootstrapCount++
		}
	}
	if printCount < 1 {
		t.Errorf("install leg recorded no print call: %q", installLeg)
	}
	if bootstrapCount != 1 {
		t.Errorf("install leg bootstrap count = %d, want 1: %q", bootstrapCount, installLeg)
	}
}

func TestInstallRetriesBootstrapOnce(t *testing.T) {
	home := t.TempDir()
	logPath := writeLaunchctlStub(t, bootstrapFailOnceBody)

	if err := Install(context.Background(), installOptions(t, home)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	bootstrapCount := 0
	for _, call := range launchctlCalls(t, logPath) {
		if strings.HasPrefix(call, "bootstrap ") {
			bootstrapCount++
		}
	}
	if bootstrapCount != 2 {
		t.Errorf("bootstrap count = %d, want 2", bootstrapCount)
	}
}

func TestInstallFailsWhenBootstrapKeepsFailing(t *testing.T) {
	home := t.TempDir()
	writeLaunchctlStub(t, bootstrapAlwaysFailsBody)

	err := Install(context.Background(), installOptions(t, home))
	if err == nil {
		t.Fatal("Install succeeded while every bootstrap failed")
	}
	if !strings.Contains(err.Error(), "launchctl bootstrap gui/") {
		t.Errorf("error does not name the manual command: %v", err)
	}
}

func TestInstallBoundsTeardownWait(t *testing.T) {
	home := t.TempDir()
	writeLaunchctlStub(t, printLoadedBootstrapFailsBody)
	opts := installOptions(t, home)
	opts.TeardownTimeout = 200 * time.Millisecond

	start := time.Now()
	err := Install(context.Background(), opts)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Install succeeded while the job never unloaded")
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Install took %v, want under 2s", elapsed)
	}
	if !strings.Contains(err.Error(), "launchctl bootstrap gui/") {
		t.Errorf("error does not name the manual command: %v", err)
	}
}

func TestInstallReturnsPromptlyWhenContextCancelled(t *testing.T) {
	home := t.TempDir()
	writeLaunchctlStub(t, printLoadedBootstrapFailsBody)
	opts := installOptions(t, home)
	opts.TeardownTimeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := Install(ctx, opts)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Install succeeded with a cancelled context")
	}
	if !stderrors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Install took %v, want under 2s", elapsed)
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
