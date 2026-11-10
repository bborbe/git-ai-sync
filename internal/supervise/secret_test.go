package supervise

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/config"
	"github.com/bborbe/git-ai-sync/internal/launchd"
	"github.com/bborbe/git-ai-sync/internal/logging"
)

// TestCredentialsNeverLeaveTheConfigFile installs the agent, runs two supervisor
// cycles against a stub pushgateway, and proves the configured password appears
// only in the 0600 config file: not in the plist, the state file, the captured
// log, or any file under $HOME/Library or $HOME/.local/state. The pushgateway
// must still receive the Basic credential, so the absence is meaningful.
func TestCredentialsNeverLeaveTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_EDITOR", "true")
	clearSecretEnv(t)

	password := "s3cr3t-do-not-log-" + randomHex(t)

	first := newSecretFixture(t)
	second := newSecretFixture(t)
	secretWriteFile(t, first.clone, "note.md", "hello\nlocal edit one\n")
	secretWriteFile(t, second.clone, "note.md", "hello\nlocal edit two\n")

	recorder := &authRecorder{}
	pushgateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer pushgateway.Close()

	configPath := filepath.Join(home, ".config", "git-ai-sync", "config.yaml")
	configBody := fmt.Sprintf(`vaults:
  - %s
  - %s
interval: 1
max_wait: 60
strategy: merge
commit_prefix: auto
pushgateway:
  url: %s
  username: u
  password: %s
`, first.clone, second.clone, pushgateway.URL, password)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatalf("chmod config file: %v", err)
	}

	installLaunchctlStub(t)

	cfg, err := config.LoadFile(context.Background(), config.FileOptions{})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	logBuffer := &lockedBuffer{}
	logger := logging.New(logBuffer, slog.LevelDebug)

	if err := launchd.Install(context.Background(), launchd.Options{Config: cfg, Logger: logger}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(runCtx, Options{Config: cfg, Logger: logger, Clock: clock.New()})
	}()

	waitForLogLine(t, logBuffer, "msg=push path=", 2, 20*time.Second)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:"+password))
	waitForAuthorization(t, recorder, wantAuth, 10*time.Second)

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run returned an error: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancellation")
	}

	authorizations := recorder.snapshot()
	if len(authorizations) == 0 {
		t.Fatal("the pushgateway received no requests")
	}
	found := false
	for _, got := range authorizations {
		if got == wantAuth {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the pushgateway never received %q; it received %q", wantAuth, authorizations)
	}

	assertNoSecretUnder(t, filepath.Join(home, "Library"), password)
	assertNoSecretUnder(t, filepath.Join(home, ".local", "state"), password)

	if bytes.Contains(logBuffer.Bytes(), []byte(password)) {
		t.Fatalf("the password leaked into the captured log:\n%s", logBuffer.String())
	}

	plistData, err := os.ReadFile(launchd.PlistPath(home))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if bytes.Contains(plistData, []byte(password)) {
		t.Fatalf("the password leaked into the plist %s", launchd.PlistPath(home))
	}

	stateData, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatalf("read state file %s: %v", cfg.StateFile, err)
	}
	if bytes.Contains(stateData, []byte(password)) {
		t.Fatalf("the password leaked into the state file %s:\n%s", cfg.StateFile, stateData)
	}
}

func clearSecretEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GIT_AI_SYNC_INTERVAL",
		"GIT_AI_SYNC_MAX_WAIT",
		"GIT_AI_SYNC_COMMIT_PREFIX",
		"GIT_AI_SYNC_STRATEGY",
		"LOG_LEVEL",
		"GIT_AI_SYNC_LOG_LEVEL",
	} {
		t.Setenv(name, "")
	}
}

func randomHex(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return hex.EncodeToString(buffer)
}

func installLaunchctlStub(t *testing.T) {
	t.Helper()
	stubDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "launchctl.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LAUNCHCTL_LOG\"\nif [ \"$1\" = \"print\" ]; then\n  exit 1\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "launchctl"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write launchctl stub: %v", err)
	}
	t.Setenv("LAUNCHCTL_LOG", logPath)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type authRecorder struct {
	mutex  sync.Mutex
	values []string
}

func (r *authRecorder) add(value string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.values = append(r.values, value)
}

func (r *authRecorder) snapshot() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]string(nil), r.values...)
}

func (r *authRecorder) contains(want string) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, value := range r.values {
		if value == want {
			return true
		}
	}
	return false
}

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

func (b *lockedBuffer) Bytes() []byte {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func waitForLogLine(t *testing.T, buffer *lockedBuffer, substring string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Count(buffer.String(), substring) >= count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d occurrences of %q in the log:\n%s", count, substring, buffer.String())
}

func waitForAuthorization(t *testing.T, recorder *authRecorder, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if recorder.contains(want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the pushgateway to receive %q; it received %q", want, recorder.snapshot())
}

func assertNoSecretUnder(t *testing.T, root string, secret string) {
	t.Helper()
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("the password leaked into %s", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
}

type secretFixture struct {
	root   string
	remote string
	clone  string
}

func newSecretFixture(t *testing.T) secretFixture {
	t.Helper()
	root := t.TempDir()
	fixture := secretFixture{
		root:   root,
		remote: filepath.Join(root, "remote.git"),
		clone:  filepath.Join(root, "clone"),
	}
	secretRunGit(t, root, "init", "--bare", fixture.remote)
	secretRunGit(t, fixture.remote, "symbolic-ref", "HEAD", "refs/heads/master")
	secretConfigure(t, fixture.remote)
	secretRunGit(t, root, "clone", fixture.remote, fixture.clone)
	secretConfigure(t, fixture.clone)
	secretWriteFile(t, fixture.clone, "note.md", "hello\n")
	secretWriteFile(t, fixture.clone, ".gitignore", "/.git-ai-sync.lock\n")
	secretRunGit(t, fixture.clone, "add", ".")
	secretRunGit(t, fixture.clone, "commit", "-m", "init")
	secretRunGit(t, fixture.clone, "push", "-u", "origin", "master")
	return fixture
}

func secretConfigure(t *testing.T, dir string) {
	t.Helper()
	secretRunGit(t, dir, "config", "user.email", "test@example.com")
	secretRunGit(t, dir, "config", "user.name", "Test User")
	secretRunGit(t, dir, "config", "commit.gpgsign", "false")
	hooksPath := filepath.Join(secretRunGit(t, dir, "rev-parse", "--absolute-git-dir"), "hooks")
	secretRunGit(t, dir, "config", "--local", "core.hooksPath", hooksPath)
}

func secretRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func secretWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
