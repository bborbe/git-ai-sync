// Package config resolves the runtime configuration from flags and the environment.
package config

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

const (
	defaultInterval     = 30
	defaultMaxWait      = 300
	defaultCommitPrefix = "auto"
	defaultStrategy     = "merge"
	defaultLogLevel     = "INFO"
)

// Config is the resolved runtime configuration.
type Config struct {
	Interval     time.Duration
	MaxWait      time.Duration
	CommitPrefix string
	Strategy     string // "merge" or "rebase"
	LogLevel     slog.Level
}

// Flags carries the command-line values that were explicitly set. A nil field
// means the flag was not provided and the environment or default applies.
type Flags struct {
	Interval *int
	Strategy *string
	LogLevel *string
}

// Load resolves Config from the explicitly set flags, then the environment, then
// the defaults. An environment variable set to the empty string is treated as unset.
func Load(ctx context.Context, flags Flags) (Config, error) {
	intervalSeconds, err := resolveInt(ctx, "interval", flags.Interval, "GIT_AI_SYNC_INTERVAL", defaultInterval)
	if err != nil {
		return Config{}, err
	}
	if intervalSeconds < 0 {
		return Config{}, errors.Errorf(ctx, "interval must not be negative: %d", intervalSeconds)
	}

	maxWaitSeconds, err := resolveInt(ctx, "max_wait", nil, "GIT_AI_SYNC_MAX_WAIT", defaultMaxWait)
	if err != nil {
		return Config{}, err
	}
	if maxWaitSeconds < 0 {
		return Config{}, errors.Errorf(ctx, "max_wait must not be negative: %d", maxWaitSeconds)
	}

	commitPrefix := resolveString(nil, "GIT_AI_SYNC_COMMIT_PREFIX", defaultCommitPrefix)

	strategy, err := resolveStrategy(ctx, flags.Strategy)
	if err != nil {
		return Config{}, err
	}

	logLevel, err := resolveLogLevel(ctx, flags.LogLevel)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Interval:     time.Duration(intervalSeconds) * time.Second,
		MaxWait:      time.Duration(maxWaitSeconds) * time.Second,
		CommitPrefix: commitPrefix,
		Strategy:     strategy,
		LogLevel:     logLevel,
	}, nil
}

func resolveInt(ctx context.Context, name string, flagValue *int, envName string, defaultValue int) (int, error) {
	if flagValue != nil {
		return *flagValue, nil
	}
	if value, ok := lookupNonEmpty(envName); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, errors.Wrapf(ctx, err, "parse %s=%q", envName, value)
		}
		return parsed, nil
	}
	return defaultValue, nil
}

func resolveString(flagValue *string, envName string, defaultValue string) string {
	if flagValue != nil {
		return *flagValue
	}
	if value, ok := lookupNonEmpty(envName); ok {
		return value
	}
	return defaultValue
}

func resolveStrategy(ctx context.Context, flagValue *string) (string, error) {
	strategy := resolveString(flagValue, "GIT_AI_SYNC_STRATEGY", defaultStrategy)
	if strategy != "merge" && strategy != "rebase" {
		return "", errors.Errorf(ctx, "invalid strategy %q: must be merge or rebase", strategy)
	}
	return strategy, nil
}

func resolveLogLevel(ctx context.Context, flagValue *string) (slog.Level, error) {
	value := resolveString(flagValue, "LOG_LEVEL", defaultLogLevel)
	return parseLogLevel(ctx, value)
}

func parseLogLevel(ctx context.Context, value string) (slog.Level, error) {
	switch strings.ToUpper(value) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	case "CRITICAL":
		return slog.LevelError + 4, nil
	default:
		return 0, errors.Errorf(ctx, "invalid log level %q", value)
	}
}

func lookupNonEmpty(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}
