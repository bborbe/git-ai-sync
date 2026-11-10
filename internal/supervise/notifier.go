package supervise

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	stdsync "sync"
	"time"

	"github.com/bborbe/errors"
)

const (
	producerKind     = "cron"
	itemIDHexLen     = 32
	notifierTimeout  = 5 * time.Second
	optionActionable = "Investigate the vault's sync failure now"
	optionDeferring  = "Leave it — it will retry automatically"

	redactedErrorMaxChars = 200
	outputTailMaxChars    = 200
	itemIDPrefix          = "ITEM_ID: "
)

var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)

// redactURLs replaces URL-shaped runs with <url> and truncates the result. The
// redaction happens before truncation so a URL cannot survive as a fragment.
func redactURLs(value string) string {
	redacted := urlPattern.ReplaceAllString(value, "<url>")
	runes := []rune(redacted)
	if len(runes) > redactedErrorMaxChars {
		return string(runes[:redactedErrorMaxChars])
	}
	return redacted
}

// notifier posts and closes the stall card through attention-ask.py. It is
// best-effort: every failure is logged at WARNING and never returned.
type notifier struct {
	active      bool // true only when the script and its interpreter are usable
	script      string
	interpreter string
	storeURL    string
	logger      *slog.Logger
}

func newNotifier(ctx context.Context, scriptPath, storeURL string, logger *slog.Logger) *notifier {
	if logger == nil {
		logger = slog.Default()
	}
	n := &notifier{storeURL: storeURL, logger: logger}
	if scriptPath == "" {
		return n
	}

	info, err := os.Stat(scriptPath)
	if err != nil {
		n.disable(scriptPath, err.Error())
		return n
	}
	if !info.Mode().IsRegular() {
		n.disable(scriptPath, "not a regular file")
		return n
	}
	file, err := os.Open(scriptPath)
	if err != nil {
		n.disable(scriptPath, err.Error())
		return n
	}
	_ = file.Close()

	interpreter, err := resolveInterpreter(ctx)
	if err != nil {
		n.disable(scriptPath, err.Error())
		return n
	}
	n.active = true
	n.script = scriptPath
	n.interpreter = interpreter
	return n
}

func (n *notifier) disable(scriptPath, reason string) {
	n.logger.Warn("attention stall alert disabled", "script", scriptPath, "reason", reason)
}

func resolveInterpreter(ctx context.Context) (string, error) {
	if path, err := exec.LookPath("python3"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("python"); err == nil {
		return path, nil
	}
	return "", errors.Errorf(ctx, "no python interpreter found on PATH")
}

func (n *notifier) enabled() bool {
	return n.active
}

// post invokes the script once and returns the 32-hex item id on success. A
// timeout or any other failure returns ("", false) after one WARNING.
func (n *notifier) post(ctx context.Context, label, repo string, count int, firstFailureAt time.Time, lastError string) (string, bool) {
	payload := fmt.Sprintf("%s: sync has failed %d consecutive cycles", label, count)
	contextText := fmt.Sprintf(
		"Repository %s has not synced since %s. Last error: %s",
		repo, firstFailureAt.UTC().Format(time.RFC3339), redactURLs(lastError),
	)
	argv := []string{
		n.interpreter, n.script, "post",
		"--dedup-key", "git-ai-sync-" + label + "-stalled",
		"--producer-id", "git-ai-sync-" + label,
		"--producer-kind", producerKind,
		"--payload", payload,
		"--context", contextText,
		"--option", optionActionable,
		"--option", optionDeferring,
		"--recommend", optionActionable,
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, notifierTimeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "ATTENTION_STORE_URL="+n.storeURL)
	output, err := cmd.CombinedOutput()
	if timeoutCtx.Err() == context.DeadlineExceeded {
		n.logger.Warn("attention post timed out", "vault", label, "timeout", "5s")
		return "", false
	}
	if err != nil {
		n.logger.Warn("attention post failed", "vault", label, "error", redactURLs(fmt.Sprintf("%v: %s", err, outputTail(output))))
		return "", false
	}

	itemID, ok := parseItemID(string(output))
	if !ok {
		n.logger.Warn("attention post failed", "vault", label, "error", "unreadable item id")
		return "", false
	}
	return itemID, true
}

// close posts one close for itemID. It is bounded and never retried.
func (n *notifier) close(ctx context.Context, itemID string) {
	if !validItemID(itemID) {
		n.logger.Warn("attention close skipped", "item_id", itemID)
		return
	}
	target := strings.TrimRight(n.storeURL, "/") + "/api/1.0/attention/" + itemID + "/close"

	timeoutCtx, cancel := context.WithTimeout(ctx, notifierTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, target, nil)
	if err != nil {
		n.logger.Warn("attention close failed", "item_id", itemID, "error", redactURLs(err.Error()))
		return
	}
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		n.logger.Warn("attention close failed", "item_id", itemID, "error", redactURLs(err.Error()))
		return
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		n.logger.Warn("attention close failed", "item_id", itemID, "error", redactURLs(response.Status))
	}
}

func parseItemID(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, itemIDPrefix) {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, itemIDPrefix))
		if validItemID(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func validItemID(candidate string) bool {
	if len(candidate) != itemIDHexLen {
		return false
	}
	for _, r := range candidate {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func outputTail(output []byte) string {
	text := strings.TrimSpace(string(output))
	runes := []rune(text)
	if len(runes) > outputTailMaxChars {
		return string(runes[len(runes)-outputTailMaxChars:])
	}
	return text
}

// stallTracker owns the per-vault consecutive-failure counters and outstanding
// card ids. It is mutex-protected because the vault goroutines share it.
type stallTracker struct {
	mutex  stdsync.Mutex
	vaults map[string]*stallState
}

type stallState struct {
	consecutiveFailures int
	outstandingItemID   string
	firstFailureAt      time.Time
}

func newStallTracker() *stallTracker {
	return &stallTracker{vaults: map[string]*stallState{}}
}

func (t *stallTracker) recordFailure(repo string, now time.Time) (int, time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	state := t.state(repo)
	if state.consecutiveFailures == 0 {
		state.firstFailureAt = now
	}
	state.consecutiveFailures++
	return state.consecutiveFailures, state.firstFailureAt
}

func (t *stallTracker) recordSuccess(repo string) string {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	state := t.state(repo)
	outstanding := state.outstandingItemID
	state.outstandingItemID = ""
	state.consecutiveFailures = 0
	state.firstFailureAt = time.Time{}
	return outstanding
}

func (t *stallTracker) outstanding(repo string) string {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	return t.state(repo).outstandingItemID
}

func (t *stallTracker) setOutstanding(repo, itemID string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.state(repo).outstandingItemID = itemID
}

func (t *stallTracker) state(repo string) *stallState {
	state, ok := t.vaults[repo]
	if !ok {
		state = &stallState{}
		t.vaults[repo] = state
	}
	return state
}
