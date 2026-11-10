package supervise

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/bborbe/errors"
)

// VaultState is one vault's persisted state.
type VaultState struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
}

// State is the persisted supervisor state read by status.
type State struct {
	LastMetricsPush time.Time              `json:"last_metrics_push"`
	Vaults          map[string]*VaultState `json:"vaults"`
}

// ReadState reads the state file. A missing file returns an error that wraps
// fs.ErrNotExist.
func ReadState(ctx context.Context, path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, errors.Wrapf(ctx, err, "read state file %s", path)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, errors.Wrapf(ctx, err, "parse state file %s", path)
	}
	if state.Vaults == nil {
		state.Vaults = map[string]*VaultState{}
	}
	return state, nil
}

// stateCollector serialises every vault's state-file update through one writer.
type stateCollector struct {
	path   string
	logger *slog.Logger
	mutex  stdsync.Mutex
	state  State
}

func newStateCollector(ctx context.Context, path string, logger *slog.Logger) (*stateCollector, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, errors.Wrapf(ctx, err, "create state directory %s", filepath.Dir(path))
	}
	return &stateCollector{
		path:   path,
		logger: logger,
		state:  State{Vaults: map[string]*VaultState{}},
	}, nil
}

func (c *stateCollector) RecordSuccess(ctx context.Context, repo string, now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	vault := c.vault(repo)
	vault.ConsecutiveFailures = 0
	vault.LastSuccess = now
	vault.LastError = ""
	c.write(ctx)
}

func (c *stateCollector) RecordFailure(ctx context.Context, repo string, _ time.Time, consecutiveFailures int, lastError string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	vault := c.vault(repo)
	vault.ConsecutiveFailures = consecutiveFailures
	vault.LastError = redactURLs(lastError)
	c.write(ctx)
}

func (c *stateCollector) RecordMetricsPush(ctx context.Context, now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.state.LastMetricsPush = now
	c.write(ctx)
}

func (c *stateCollector) vault(repo string) *VaultState {
	if c.state.Vaults == nil {
		c.state.Vaults = map[string]*VaultState{}
	}
	vault, ok := c.state.Vaults[repo]
	if !ok {
		vault = &VaultState{}
		c.state.Vaults[repo] = vault
	}
	return vault
}

func (c *stateCollector) write(ctx context.Context) {
	data, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".status-*")
	if err != nil {
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	if err := tmp.Close(); err != nil {
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
		return
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		c.logger.Warn("write state file failed", "path", c.path, "error", err)
	}
}
