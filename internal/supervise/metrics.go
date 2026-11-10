package supervise

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bborbe/git-ai-sync/internal/config"
)

const (
	metricsJob        = "git_ai_sync"
	heartbeatMetric   = "git_ai_sync_heartbeat_timestamp"
	lastSuccessMetric = "git_ai_sync_last_success_timestamp"
	metricsTimeout    = 5 * time.Second

	redactedErrorMaxChars = 200
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

type metricsClient struct {
	pushgateway config.Pushgateway
	http        *http.Client
	logger      *slog.Logger
}

func newMetrics(pushgateway config.Pushgateway, logger *slog.Logger) *metricsClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &metricsClient{
		pushgateway: pushgateway,
		http:        &http.Client{},
		logger:      logger,
	}
}

func (m *metricsClient) enabled() bool {
	return m.pushgateway.URL != ""
}

// push sends one POST carrying the heartbeat always and the last-success metric
// when success is true. It returns true only on a 2xx response.
func (m *metricsClient) push(ctx context.Context, label string, now time.Time, success bool) bool {
	base := strings.TrimRight(m.pushgateway.URL, "/")
	target := base + "/metrics/job/" + metricsJob + "/instance/" + url.PathEscape(label)

	var body strings.Builder
	fmt.Fprintf(&body, "# TYPE %s gauge\n%s{vault=\"%s\"} %d\n", heartbeatMetric, heartbeatMetric, escapeLabel(label), now.Unix())
	if success {
		fmt.Fprintf(&body, "# TYPE %s gauge\n%s{vault=\"%s\"} %d\n", lastSuccessMetric, lastSuccessMetric, escapeLabel(label), now.Unix())
	}

	requestCtx, cancel := context.WithTimeout(ctx, metricsTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, strings.NewReader(body.String()))
	if err != nil {
		m.logger.Warn("metric push failed", "vault", label, "error", redactURLs(err.Error()))
		return false
	}
	request.Header.Set("Content-Type", "text/plain; version=0.0.4")
	if m.pushgateway.Username != "" {
		token := base64.StdEncoding.EncodeToString([]byte(m.pushgateway.Username + ":" + m.pushgateway.Password))
		request.Header.Set("Authorization", "Basic "+token)
	}

	response, err := m.http.Do(request)
	if err != nil {
		m.logger.Warn("metric push failed", "vault", label, "error", redactURLs(err.Error()))
		return false
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		m.logger.Warn("metric push failed", "vault", label, "error", redactURLs(response.Status))
		return false
	}
	return true
}

func escapeLabel(label string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(label)
}
