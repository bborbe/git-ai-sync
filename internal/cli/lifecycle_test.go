package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/launchd"
)

func writeConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "git-ai-sync", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func stubLaunchctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "launchctl.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("create launchctl log: %v", err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create launchctl bin directory: %v", err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create launchctl state directory: %v", err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$LAUNCHCTL_LOG\"\n" +
		"if [ \"$1\" = \"bootout\" ]; then\n" +
		"  : > \"$LAUNCHCTL_STATE/unloaded\"\n" +
		"fi\n" +
		"if [ \"$1\" = \"bootstrap\" ]; then\n" +
		"  rm -f \"$LAUNCHCTL_STATE/unloaded\"\n" +
		"fi\n" +
		"if [ \"$1\" = \"print\" ]; then\n" +
		"  if [ -n \"$LAUNCHCTL_PRINT_FAIL\" ]; then\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  if [ -f \"$LAUNCHCTL_STATE/unloaded\" ]; then\n" +
		"    exit 1\n" +
		"  fi\n" +
		"fi\n" +
		"exit 0\n"
	scriptPath := filepath.Join(binDir, "launchctl")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write launchctl stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LAUNCHCTL_LOG", logPath)
	t.Setenv("LAUNCHCTL_STATE", stateDir)
	return logPath
}

func runCLI(t *testing.T, home string, args ...string) error {
	t.Helper()
	clearEnv(t)
	t.Setenv("HOME", home)
	return Run(context.Background(), args)
}

func runStatus(t *testing.T, home string) (string, error) {
	t.Helper()
	clearEnv(t)
	t.Setenv("HOME", home)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newRootCommand(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"status"})
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestInstallWritesPlistAndBootstraps(t *testing.T) {
	home := t.TempDir()
	logPath := stubLaunchctl(t)
	vault := t.TempDir()
	configPath := writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n", vault))

	if err := runCLI(t, home, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}

	data, err := os.ReadFile(launchd.PlistPath(home))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	document := string(data)
	for _, needle := range []string{
		launchd.Label,
		"<key>KeepAlive</key>",
		"<true/>",
		"<key>RunAtLoad</key>",
		"<integer>30</integer>",
		"<string>Standard</string>",
		"<integer>65536</integer>",
		"<integer>245760</integer>",
		"/usr/local/bin",
		launchd.LogPath(home),
	} {
		requireContains(t, document, needle)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve executable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("resolve executable symlinks: %v", err)
	}
	requireContains(t, document, "<string>"+resolved+"</string>")
	requireOrdered(t, document,
		"<string>"+resolved+"</string>",
		"<string>supervise</string>",
		"<string>--config</string>",
		"<string>"+configPath+"</string>",
	)
	for _, forbidden := range []string{"NumberOfProcesses", "ResidentSetSize"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("plist contains forbidden %q", forbidden)
		}
	}

	if _, err := os.Stat(launchd.LogDir(home)); err != nil {
		t.Errorf("log directory was not created: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}
	bootoutIndex, bootstrapIndex := -1, -1
	for index, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		if strings.HasPrefix(line, "bootout ") && bootoutIndex < 0 {
			bootoutIndex = index
		}
		if strings.HasPrefix(line, "bootstrap ") && bootstrapIndex < 0 {
			bootstrapIndex = index
		}
		if strings.Contains(line, "kickstart") {
			t.Errorf("install called kickstart: %q", line)
		}
	}
	if bootoutIndex < 0 {
		t.Fatalf("launchctl log has no bootout: %q", string(logData))
	}
	if bootstrapIndex < 0 {
		t.Fatalf("launchctl log has no bootstrap: %q", string(logData))
	}
	if bootoutIndex > bootstrapIndex {
		t.Errorf("bootstrap ran before bootout: %q", string(logData))
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	stubLaunchctl(t)
	vault := t.TempDir()
	writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n", vault))

	if err := runCLI(t, home, "install"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(launchd.PlistPath(home))
	if err != nil {
		t.Fatalf("read first plist: %v", err)
	}

	if err := runCLI(t, home, "install"); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(launchd.PlistPath(home))
	if err != nil {
		t.Fatalf("read second plist: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("second install wrote a different plist:\n%s\n---\n%s", first, second)
	}
}

func TestUninstallRemovesPlist(t *testing.T) {
	home := t.TempDir()
	logPath := stubLaunchctl(t)
	vault := t.TempDir()
	writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n", vault))

	if err := runCLI(t, home, "install"); err != nil {
		t.Fatalf("install: %v", err)
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}

	if err := runCLI(t, home, "uninstall"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(launchd.PlistPath(home)); !os.IsNotExist(err) {
		t.Errorf("plist still exists after uninstall: %v", err)
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}
	if got, want := strings.Count(string(after), "bootout "), strings.Count(string(before), "bootout ")+1; got != want {
		t.Errorf("bootout count = %d, want %d", got, want)
	}

	if err := runCLI(t, home, "uninstall"); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	afterSecond, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}
	if got, want := strings.Count(string(afterSecond), "bootout "), strings.Count(string(after), "bootout "); got != want {
		t.Errorf("uninstall with no plist appended a bootout: got %d, want %d", got, want)
	}
}

func TestInstallRefusesWorldReadableConfig(t *testing.T) {
	home := t.TempDir()
	stubLaunchctl(t)
	vault := t.TempDir()
	configPath := writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n", vault))
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatalf("chmod config: %v", err)
	}

	err := runCLI(t, home, "install")
	if err == nil {
		t.Fatal("install accepted a world-readable config")
	}
	if !strings.Contains(err.Error(), "mode") || !strings.Contains(err.Error(), "0600") {
		t.Errorf("error does not name the permission problem: %v", err)
	}

	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatalf("chmod config back: %v", err)
	}
	if err := runCLI(t, home, "install"); err != nil {
		t.Fatalf("install with 0600 config: %v", err)
	}
}

func TestStatusReportsVaultsAndFreshness(t *testing.T) {
	home := t.TempDir()
	stubLaunchctl(t)
	vaultA := t.TempDir()
	vaultB := t.TempDir()
	writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n  - %s\n", vaultA, vaultB))

	statePath := filepath.Join(home, ".local", "state", "git-ai-sync", "status.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	stamp := time.Now().Add(-10 * time.Second).Format(time.RFC3339Nano)
	state := fmt.Sprintf(`{"last_metrics_push":%q,"vaults":{}}`, stamp)
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	out, err := runStatus(t, home)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	requireContains(t, out, vaultA)
	requireContains(t, out, vaultB)
	if age, ok := parseMetricsAge(t, out); !ok {
		t.Fatalf("status has no last metrics push line: %q", out)
	} else if age <= 0 {
		t.Errorf("last metrics push age = %v, want a positive duration", age)
	} else if age >= 60*time.Second {
		t.Errorf("last metrics push age = %v, want under 60s", age)
	}

	vaultC := t.TempDir()
	writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n  - %s\n", vaultB, vaultC))
	out, err = runStatus(t, home)
	if err != nil {
		t.Fatalf("status after config change: %v", err)
	}
	requireContains(t, out, vaultB)
	requireContains(t, out, vaultC)
	if strings.Contains(out, vaultA) {
		t.Errorf("status still names the dropped vault %s: %q", vaultA, out)
	}

	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state file: %v", err)
	}
	out, err = runStatus(t, home)
	if err != nil {
		t.Fatalf("status without state file: %v", err)
	}
	requireContains(t, out, "last metrics push: never")

	t.Setenv("LAUNCHCTL_PRINT_FAIL", "1")
	_, err = runStatus(t, home)
	if err == nil {
		t.Fatal("status succeeded while the agent is not loaded")
	}
	if !strings.Contains(err.Error(), launchd.Label) {
		t.Errorf("status error does not name the label: %v", err)
	}
}

func TestStatusReportsNeverForZeroPush(t *testing.T) {
	home := t.TempDir()
	stubLaunchctl(t)
	vault := t.TempDir()
	writeConfig(t, home, fmt.Sprintf("vaults:\n  - %s\n", vault))

	statePath := filepath.Join(home, ".local", "state", "git-ai-sync", "status.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	state := `{"last_metrics_push":"0001-01-01T00:00:00Z","vaults":{}}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	out, err := runStatus(t, home)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if line == "last metrics push: never" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("status has no exact %q line: %q", "last metrics push: never", out)
	}
}

func TestConfigRequiringCommandsRequireConfig(t *testing.T) {
	home := t.TempDir()
	expected := filepath.Join(home, ".config", "git-ai-sync", "config.yaml")

	for _, name := range []string{"status", "install", "uninstall", "supervise"} {
		err := runCLI(t, home, name)
		if err == nil {
			t.Errorf("%s succeeded without a config file", name)
			continue
		}
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("%s error does not name %s: %v", name, expected, err)
		}
	}

	t.Setenv("GIT_EDITOR", "true")
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	clone := filepath.Join(root, "clone")
	gitOutput(t, root, "init", "--bare", remote)
	gitOutput(t, remote, "symbolic-ref", "HEAD", "refs/heads/master")
	configureRepo(t, remote)
	gitOutput(t, root, "clone", remote, clone)
	configureRepo(t, clone)
	writeRepoFile(t, clone, "note.md", "hello\n")
	writeRepoFile(t, clone, ".gitignore", "/.git-ai-sync.lock\n")
	gitOutput(t, clone, "add", ".")
	gitOutput(t, clone, "commit", "-m", "init")
	gitOutput(t, clone, "push", "-u", "origin", "master")

	before := gitOutput(t, remote, "rev-parse", "master")
	writeRepoFile(t, clone, "note.md", "hello\nlocal edit\n")

	if err := runCLI(t, home, "sync", clone); err != nil {
		t.Fatalf("sync without a config file: %v", err)
	}
	after := gitOutput(t, remote, "rev-parse", "master")
	if after == before {
		t.Errorf("remote master did not advance: %s", after)
	}
	if _, err := os.Stat(expected); !os.IsNotExist(err) {
		t.Errorf("a config file was created at %s: %v", expected, err)
	}
}

func parseMetricsAge(t *testing.T, out string) (time.Duration, bool) {
	t.Helper()
	const prefix = "last metrics push: "
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, " ago") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(line, prefix), " ago")
		age, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("parse duration %q: %v", value, err)
		}
		return age, true
	}
	return 0, false
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func configureRepo(t *testing.T, dir string) {
	t.Helper()
	gitOutput(t, dir, "config", "user.email", "test@example.com")
	gitOutput(t, dir, "config", "user.name", "Test User")
	gitOutput(t, dir, "config", "commit.gpgsign", "false")
	hooksPath := filepath.Join(gitOutput(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	gitOutput(t, dir, "config", "--local", "core.hooksPath", hooksPath)
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func requireContains(t *testing.T, document string, needle string) {
	t.Helper()
	if !strings.Contains(document, needle) {
		t.Errorf("output does not contain %q:\n%s", needle, document)
	}
}

func requireOrdered(t *testing.T, document string, needles ...string) {
	t.Helper()
	previous := -1
	for _, needle := range needles {
		index := strings.Index(document, needle)
		if index < 0 {
			t.Fatalf("output does not contain %q:\n%s", needle, document)
		}
		if index <= previous {
			t.Errorf("%q does not follow the previous entry", needle)
		}
		previous = index
	}
}
