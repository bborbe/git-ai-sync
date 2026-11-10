package config

import (
	"bytes"
	"context"
	stderrors "errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bborbe/errors"
	yaml "go.yaml.in/yaml/v3"
)

const (
	defaultStallThreshold    = 10
	defaultAttentionStoreURL = "http://localhost:18080"
)

// Pushgateway holds the metrics endpoint and its credentials.
type Pushgateway struct {
	URL      string
	Username string
	Password string
}

// FileConfig is the resolved configuration for the config-file commands:
// supervise, install, uninstall and status.
type FileConfig struct {
	ConfigPath        string // the resolved path the file was read from
	Vaults            []string
	Interval          time.Duration
	MaxWait           time.Duration
	CommitPrefix      string
	Strategy          string // "merge" or "rebase"
	LogLevel          slog.Level
	StallThreshold    int
	AttentionScript   string
	AttentionStoreURL string
	StateFile         string
	Pushgateway       Pushgateway
}

// FileOptions carries the values the config-file commands were given.
// A nil field means the value was not provided on the command line.
type FileOptions struct {
	ConfigPath *string
	Interval   *int
	Strategy   *string
	LogLevel   *string
}

type fileConfig struct {
	Vaults            []string        `yaml:"vaults"`
	Interval          *int            `yaml:"interval"`
	MaxWait           *int            `yaml:"max_wait"`
	Strategy          *string         `yaml:"strategy"`
	CommitPrefix      *string         `yaml:"commit_prefix"`
	LogLevel          *string         `yaml:"log_level"`
	StallThreshold    *int            `yaml:"stall_threshold"`
	AttentionScript   *string         `yaml:"attention_script"`
	AttentionStoreURL *string         `yaml:"attention_store_url"`
	StateFile         *string         `yaml:"state_file"`
	Pushgateway       *pushgatewayRaw `yaml:"pushgateway"`
}

type pushgatewayRaw struct {
	URL      *string `yaml:"url"`
	Username *string `yaml:"username"`
	Password *string `yaml:"password"`
}

// DefaultConfigPath returns $HOME/.config/git-ai-sync/config.yaml.
func DefaultConfigPath(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home directory")
	}
	return filepath.Join(home, ".config", "git-ai-sync", "config.yaml"), nil
}

// DefaultStateFile returns $HOME/.local/state/git-ai-sync/status.json.
func DefaultStateFile(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home directory")
	}
	return filepath.Join(home, ".local", "state", "git-ai-sync", "status.json"), nil
}

// LoadFile resolves FileConfig from the explicit flags, then the config file,
// then the environment, then the built-in defaults.
func LoadFile(ctx context.Context, opts FileOptions) (FileConfig, error) {
	path, err := resolveConfigPath(ctx, opts)
	if err != nil {
		return FileConfig{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, errors.Wrapf(ctx, err, "read config file %s", path)
	}

	raw, err := decodeFile(ctx, path, data)
	if err != nil {
		return FileConfig{}, err
	}

	if len(raw.Vaults) == 0 {
		return FileConfig{}, errors.Errorf(ctx, "config file %s names no vaults", path)
	}
	for _, vault := range raw.Vaults {
		if !filepath.IsAbs(vault) {
			return FileConfig{}, errors.Errorf(ctx, "config file %s: vault path %q is not absolute", path, vault)
		}
	}

	interval, err := resolveFileInt(ctx, "interval", opts.Interval, raw.Interval, "GIT_AI_SYNC_INTERVAL", defaultInterval)
	if err != nil {
		return FileConfig{}, err
	}
	maxWait, err := resolveFileInt(ctx, "max_wait", nil, raw.MaxWait, "GIT_AI_SYNC_MAX_WAIT", defaultMaxWait)
	if err != nil {
		return FileConfig{}, err
	}
	commitPrefix := resolveFileString(raw.CommitPrefix, "GIT_AI_SYNC_COMMIT_PREFIX", defaultCommitPrefix)

	strategy, err := resolveFileStrategy(ctx, opts.Strategy, raw.Strategy)
	if err != nil {
		return FileConfig{}, err
	}

	logLevel, err := resolveFileLogLevel(ctx, opts.LogLevel, raw.LogLevel)
	if err != nil {
		return FileConfig{}, err
	}

	stallThreshold, err := resolveStallThreshold(ctx, raw.StallThreshold)
	if err != nil {
		return FileConfig{}, err
	}

	stateFile, err := resolveStateFile(ctx, raw.StateFile)
	if err != nil {
		return FileConfig{}, err
	}

	return FileConfig{
		ConfigPath:        path,
		Vaults:            raw.Vaults,
		Interval:          time.Duration(interval) * time.Second,
		MaxWait:           time.Duration(maxWait) * time.Second,
		CommitPrefix:      commitPrefix,
		Strategy:          strategy,
		LogLevel:          logLevel,
		StallThreshold:    stallThreshold,
		AttentionScript:   resolveOptional(raw.AttentionScript, ""),
		AttentionStoreURL: resolveOptional(raw.AttentionStoreURL, defaultAttentionStoreURL),
		StateFile:         stateFile,
		Pushgateway:       resolvePushgateway(raw.Pushgateway),
	}, nil
}

// CheckPermissions reports an error when path is group- or world-accessible.
func CheckPermissions(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrapf(ctx, err, "stat config file %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.Errorf(ctx, "config file %s is group/world-accessible (mode %04o); run chmod 0600", path, info.Mode().Perm())
	}
	return nil
}

func resolveConfigPath(ctx context.Context, opts FileOptions) (string, error) {
	if opts.ConfigPath != nil && *opts.ConfigPath != "" {
		return *opts.ConfigPath, nil
	}
	path, err := DefaultConfigPath(ctx)
	if err != nil {
		return "", err
	}
	return path, nil
}

func decodeFile(ctx context.Context, path string, data []byte) (fileConfig, error) {
	var raw fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if stderrors.Is(err, io.EOF) {
			return fileConfig{}, errors.Errorf(ctx, "config file %s is empty", path)
		}
		return fileConfig{}, errors.Wrapf(ctx, err, "parse config file %s", path)
	}
	return raw, nil
}

func resolveFileInt(ctx context.Context, name string, flagValue *int, fileValue *int, envName string, defaultValue int) (int, error) {
	if flagValue != nil {
		return validateNonNegative(ctx, name, *flagValue)
	}
	if fileValue != nil {
		return validateNonNegative(ctx, name, *fileValue)
	}
	if value, ok := lookupNonEmpty(envName); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, errors.Wrapf(ctx, err, "parse %s=%q", envName, value)
		}
		return validateNonNegative(ctx, name, parsed)
	}
	return defaultValue, nil
}

func validateNonNegative(ctx context.Context, name string, value int) (int, error) {
	if value < 0 {
		return 0, errors.Errorf(ctx, "%s must not be negative: %d", name, value)
	}
	return value, nil
}

func resolveFileString(fileValue *string, envName string, defaultValue string) string {
	if fileValue != nil {
		return *fileValue
	}
	if value, ok := lookupNonEmpty(envName); ok {
		return value
	}
	return defaultValue
}

func resolveFileStrategy(ctx context.Context, flagValue *string, fileValue *string) (string, error) {
	strategy := defaultStrategy
	if flagValue != nil {
		strategy = *flagValue
	} else if fileValue != nil {
		strategy = *fileValue
	} else if value, ok := lookupNonEmpty("GIT_AI_SYNC_STRATEGY"); ok {
		strategy = value
	}
	if strategy != "merge" && strategy != "rebase" {
		return "", errors.Errorf(ctx, "invalid strategy %q: must be merge or rebase", strategy)
	}
	return strategy, nil
}

func resolveFileLogLevel(ctx context.Context, flagValue *string, fileValue *string) (slog.Level, error) {
	value := defaultLogLevel
	if flagValue != nil {
		value = *flagValue
	} else if fileValue != nil {
		value = *fileValue
	} else if envValue, ok := lookupNonEmpty("LOG_LEVEL"); ok {
		value = envValue
	}
	return parseLogLevel(ctx, value)
}

func resolveStallThreshold(ctx context.Context, fileValue *int) (int, error) {
	if fileValue == nil {
		return defaultStallThreshold, nil
	}
	if *fileValue < 2 {
		return 0, errors.Errorf(ctx, "stall_threshold must be at least 2: %d", *fileValue)
	}
	return *fileValue, nil
}

func resolveStateFile(ctx context.Context, fileValue *string) (string, error) {
	if fileValue != nil {
		return *fileValue, nil
	}
	return DefaultStateFile(ctx)
}

func resolveOptional(fileValue *string, defaultValue string) string {
	if fileValue != nil {
		return *fileValue
	}
	return defaultValue
}

func resolvePushgateway(raw *pushgatewayRaw) Pushgateway {
	if raw == nil {
		return Pushgateway{}
	}
	return Pushgateway{
		URL:      resolveOptional(raw.URL, ""),
		Username: resolveOptional(raw.Username, ""),
		Password: resolveOptional(raw.Password, ""),
	}
}
