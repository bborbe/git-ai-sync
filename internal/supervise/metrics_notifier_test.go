package supervise

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/bborbe/git-ai-sync/internal/config"
)

// TestMetricsHeartbeatIsPOST covers Acceptance Criterion 5: every pushgateway
// request is a POST carrying both gauges, never a PUT, and Basic auth is sent.
func TestMetricsHeartbeatIsPOST(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	fixture := newSuperviseFixture(t)
	superviseWriteFile(t, fixture.clone, "note.md", "hello\nfirst edit\n")

	type pushRequest struct {
		method string
		path   string
		body   string
		auth   string
	}
	var mutex stdsync.Mutex
	var requests []pushRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mutex.Lock()
		requests = append(requests, pushRequest{
			method: r.Method,
			path:   r.URL.Path,
			body:   string(body),
			auth:   r.Header.Get("Authorization"),
		})
		mutex.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := superviseConfig([]string{fixture.clone})
	cfg.StateFile = filepath.Join(t.TempDir(), "status.json")
	cfg.Pushgateway = config.Pushgateway{URL: server.URL, Username: "u", Password: "p"}

	cancel, done, buffer := startSupervise(t, cfg)
	waitForOccurrences(t, buffer, "push path="+fixture.clone, 1, supervisePushTimeout)
	superviseWriteFile(t, fixture.clone, "note.md", "hello\nsecond edit\n")
	waitForOccurrences(t, buffer, "push path="+fixture.clone, 2, supervisePushTimeout)
	cancel()
	requireRunReturns(t, done)

	mutex.Lock()
	snapshot := append([]pushRequest(nil), requests...)
	mutex.Unlock()

	if len(snapshot) == 0 {
		t.Fatal("the pushgateway received no requests")
	}
	var bodies strings.Builder
	basicAuth := false
	for _, request := range snapshot {
		if request.method != http.MethodPost {
			t.Errorf("pushgateway request method = %s, want POST", request.method)
		}
		if request.method == http.MethodPut {
			t.Errorf("pushgateway received a PUT")
		}
		if !strings.HasPrefix(request.path, "/metrics/job/"+metricsJob+"/instance/") {
			t.Errorf("pushgateway path = %q, want the job/instance grouping key", request.path)
		}
		if strings.HasPrefix(request.auth, "Basic ") {
			basicAuth = true
		}
		bodies.WriteString(request.body)
	}

	all := bodies.String()
	if !strings.Contains(all, heartbeatMetric) {
		t.Errorf("no request body carried %s:\n%s", heartbeatMetric, all)
	}
	if !strings.Contains(all, lastSuccessMetric) {
		t.Errorf("no request body carried %s:\n%s", lastSuccessMetric, all)
	}
	if !basicAuth {
		t.Error("no request carried a Basic Authorization header")
	}
}

type metricsNotifierInvocation struct {
	Argv  []string `json:"argv"`
	Store string   `json:"store"`
}

// TestStallCardPostsOnceAndClosesOnRecovery covers Acceptance Criterion 6: one
// card on the second failed cycle with the required flags, and one close on the
// next success.
func TestStallCardPostsOnceAndClosesOnRecovery(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	fixture := newSuperviseFixture(t)
	superviseRunGit(t, fixture.clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	stubDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "attention.log")
	script := filepath.Join(stubDir, "attention-ask.py")
	const stub = `import json, os, sys
with open(os.environ["ATTENTION_STUB_LOG"], "a") as f:
    f.write(json.dumps({"argv": sys.argv[1:], "store": os.environ.get("ATTENTION_STORE_URL")}) + "\n")
print("ITEM_ID: " + "a" * 32)
`
	if err := os.WriteFile(script, []byte(stub), 0o644); err != nil {
		t.Fatalf("write attention stub: %v", err)
	}
	t.Setenv("ATTENTION_STUB_LOG", stubLog)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store := &storeRecorder{}
	storeServer := httptest.NewServer(store)
	defer storeServer.Close()

	cfg := superviseConfig([]string{fixture.clone})
	cfg.StateFile = filepath.Join(t.TempDir(), "status.json")
	cfg.AttentionScript = script
	cfg.AttentionStoreURL = storeServer.URL
	cfg.StallThreshold = 2

	cancel, done, _ := startSupervise(t, cfg)
	invocations := waitForInvocations(t, stubLog, 1, 10*time.Second)
	if len(invocations) != 1 {
		t.Fatalf("attention stub invoked %d times, want exactly 1", len(invocations))
	}

	invocation := invocations[0]
	label := Label(fixture.clone)
	dedupKey, ok := flagValue(invocation.Argv, "--dedup-key")
	if !ok || dedupKey != "git-ai-sync-"+label+"-stalled" {
		t.Errorf("--dedup-key = %q (present %t), want %q", dedupKey, ok, "git-ai-sync-"+label+"-stalled")
	}
	producerID, ok := flagValue(invocation.Argv, "--producer-id")
	if !ok || producerID != "git-ai-sync-"+label {
		t.Errorf("--producer-id = %q (present %t), want %q", producerID, ok, "git-ai-sync-"+label)
	}
	producerKindValue, ok := flagValue(invocation.Argv, "--producer-kind")
	if !ok || producerKindValue != producerKind {
		t.Errorf("--producer-kind = %q (present %t), want %q", producerKindValue, ok, producerKind)
	}
	options := flagValues(invocation.Argv, "--option")
	if len(options) != 2 {
		t.Errorf("--option count = %d, want exactly 2: %v", len(options), options)
	}
	recommend, ok := flagValue(invocation.Argv, "--recommend")
	if !ok {
		t.Error("--recommend is missing")
	} else if recommend != options[0] && recommend != options[1] {
		t.Errorf("--recommend = %q does not name one of the options %v", recommend, options)
	}
	payload, ok := flagValue(invocation.Argv, "--payload")
	if !ok {
		t.Error("--payload is missing")
	} else if !strings.Contains(payload, "2") {
		t.Errorf("--payload = %q, want the second failed cycle", payload)
	}
	contextText, ok := flagValue(invocation.Argv, "--context")
	if !ok {
		t.Error("--context is missing")
	} else if !strings.Contains(contextText, fixture.clone) {
		t.Errorf("--context = %q, want the repository path %s", contextText, fixture.clone)
	}
	if invocation.Store != storeServer.URL {
		t.Errorf("child ATTENTION_STORE_URL = %q, want %q", invocation.Store, storeServer.URL)
	}

	superviseRunGit(t, fixture.clone, "remote", "set-url", "origin", fixture.remote)
	closePath := "/api/1.0/attention/" + strings.Repeat("a", 32) + "/close"
	waitForStorePath(t, store, closePath, 10*time.Second)

	cancel()
	requireRunReturns(t, done)

	paths := store.snapshot()
	closes := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "/close") {
			closes++
		}
	}
	if closes != 1 {
		t.Errorf("store received %d close requests, want exactly 1: %v", closes, paths)
	}
	if got := len(readInvocations(t, stubLog)); got != 1 {
		t.Errorf("attention stub invoked %d times in total, want exactly 1", got)
	}
}

// TestMissingNotifierDisablesAlerting covers Acceptance Criterion 7: a missing
// script logs one startup warning, invokes nothing, and does not stop the loop.
func TestMissingNotifierDisablesAlerting(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	fixture := newSuperviseFixture(t)
	superviseRunGit(t, fixture.clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	cfg := superviseConfig([]string{fixture.clone})
	cfg.StateFile = filepath.Join(t.TempDir(), "status.json")
	cfg.AttentionScript = filepath.Join(t.TempDir(), "does-not-exist", "attention-ask.py")
	cfg.AttentionStoreURL = "http://127.0.0.1:1"
	cfg.StallThreshold = 2

	cancel, done, buffer := startSupervise(t, cfg)
	waitForConsecutiveFailures(t, cfg.StateFile, fixture.clone, 3, 10*time.Second)
	cancel()
	requireRunReturns(t, done)

	disabled := 0
	for _, line := range strings.Split(buffer.String(), "\n") {
		if strings.Contains(line, "attention stall alert disabled") && strings.Contains(line, cfg.AttentionScript) {
			disabled++
		}
	}
	if disabled != 1 {
		t.Errorf("found %d disabled warnings naming %s, want exactly 1:\n%s", disabled, cfg.AttentionScript, buffer.String())
	}
	if strings.Contains(buffer.String(), "attention post") {
		t.Errorf("a disabled notifier still attempted a post:\n%s", buffer.String())
	}

	state, err := ReadState(context.Background(), cfg.StateFile)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	vault, ok := state.Vaults[fixture.clone]
	if !ok {
		t.Fatalf("state file has no entry for %s: %+v", fixture.clone, state.Vaults)
	}
	if vault.ConsecutiveFailures != 3 {
		t.Errorf("consecutive_failures = %d, want 3", vault.ConsecutiveFailures)
	}
}

// TestHangingNotifierIsBounded covers Acceptance Criterion 8: a 10 s script is
// abandoned at 5 s with one warning, one invocation, and the loop continues.
func TestHangingNotifierIsBounded(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	fixture := newSuperviseFixture(t)
	superviseRunGit(t, fixture.clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	stubDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "hanging.log")
	script := filepath.Join(stubDir, "attention-ask.py")
	const stub = `import os, time
with open(os.environ["ATTENTION_STUB_LOG"], "a") as f:
    f.write("invoked\n")
time.sleep(10)
`
	if err := os.WriteFile(script, []byte(stub), 0o644); err != nil {
		t.Fatalf("write hanging stub: %v", err)
	}
	t.Setenv("ATTENTION_STUB_LOG", stubLog)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := superviseConfig([]string{fixture.clone})
	cfg.StateFile = filepath.Join(t.TempDir(), "status.json")
	cfg.AttentionScript = script
	cfg.AttentionStoreURL = "http://127.0.0.1:1"
	cfg.StallThreshold = 2

	cancel, done, buffer := startSupervise(t, cfg)
	waitForInvocationLines(t, stubLog, 1, 10*time.Second)
	started := time.Now()

	waitForLine(t, buffer, "timed out", 8*time.Second)
	if elapsed := time.Since(started); elapsed >= 7*time.Second {
		t.Errorf("the hanging call was abandoned after %s, want under 7s", elapsed)
	}
	if got := len(readLines(t, stubLog)); got != 1 {
		t.Errorf("stub invocation count = %d at timeout, want exactly 1", got)
	}

	checking := strings.Count(buffer.String(), "Checking...")
	waitForOccurrences(t, buffer, "Checking...", checking+1, 10*time.Second)

	cancel()
	requireRunReturns(t, done)
}

type storeRecorder struct {
	mutex stdsync.Mutex
	paths []string
}

func (r *storeRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.mutex.Lock()
	r.paths = append(r.paths, request.URL.Path)
	r.mutex.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (r *storeRecorder) snapshot() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]string(nil), r.paths...)
}

func waitForStorePath(t *testing.T, recorder *storeRecorder, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, path := range recorder.snapshot() {
			if path == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the store to receive %s; it received %v", want, recorder.snapshot())
}

func waitForOccurrences(t *testing.T, buffer *superviseLogBuffer, substr string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Count(buffer.String(), substr) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d occurrences of %q:\n%s", want, substr, buffer.String())
}

func waitForConsecutiveFailures(t *testing.T, path, repo string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := ReadState(context.Background(), path)
		if err == nil {
			if vault, ok := state.Vaults[repo]; ok && vault.ConsecutiveFailures >= want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d consecutive failures for %s", want, repo)
}

func waitForInvocations(t *testing.T, path string, want int, timeout time.Duration) []metricsNotifierInvocation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		invocations := readInvocations(t, path)
		if len(invocations) >= want {
			return invocations
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d attention invocations in %s", want, path)
	return nil
}

func waitForInvocationLines(t *testing.T, path string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(readLines(t, path)) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lines in %s", want, path)
}

func readInvocations(t *testing.T, path string) []metricsNotifierInvocation {
	t.Helper()
	var invocations []metricsNotifierInvocation
	for _, line := range readLines(t, path) {
		var invocation metricsNotifierInvocation
		if err := json.Unmarshal([]byte(line), &invocation); err != nil {
			// A partial final line appears while the child is still writing.
			continue
		}
		invocations = append(invocations, invocation)
	}
	return invocations
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if stderrors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func flagValue(argv []string, flag string) (string, bool) {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == flag {
			return argv[index+1], true
		}
	}
	return "", false
}

func flagValues(argv []string, flag string) []string {
	var values []string
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == flag {
			values = append(values, argv[index+1])
		}
	}
	return values
}
