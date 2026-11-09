package config

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

var envNames = []string{
	"GIT_AI_SYNC_INTERVAL",
	"GIT_AI_SYNC_MAX_WAIT",
	"GIT_AI_SYNC_COMMIT_PREFIX",
	"GIT_AI_SYNC_STRATEGY",
	"LOG_LEVEL",
	"GIT_AI_SYNC_LOG_LEVEL",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range envNames {
		t.Setenv(name, "")
	}
}

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

func TestDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(context.Background(), Flags{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", cfg.Interval)
	}
	if cfg.MaxWait != 300*time.Second {
		t.Errorf("MaxWait = %v, want 300s", cfg.MaxWait)
	}
	if cfg.CommitPrefix != "auto" {
		t.Errorf("CommitPrefix = %q, want auto", cfg.CommitPrefix)
	}
	if cfg.Strategy != "merge" {
		t.Errorf("Strategy = %q, want merge", cfg.Strategy)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want INFO", cfg.LogLevel)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("GIT_AI_SYNC_INTERVAL", "7")
	t.Setenv("GIT_AI_SYNC_MAX_WAIT", "11")
	t.Setenv("GIT_AI_SYNC_COMMIT_PREFIX", "sync")
	t.Setenv("GIT_AI_SYNC_STRATEGY", "rebase")
	t.Setenv("LOG_LEVEL", "DEBUG")

	cfg, err := Load(context.Background(), Flags{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != 7*time.Second || cfg.MaxWait != 11*time.Second {
		t.Errorf("durations = %v/%v, want 7s/11s", cfg.Interval, cfg.MaxWait)
	}
	if cfg.CommitPrefix != "sync" || cfg.Strategy != "rebase" {
		t.Errorf("strings = %q/%q, want sync/rebase", cfg.CommitPrefix, cfg.Strategy)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want DEBUG", cfg.LogLevel)
	}
}

func TestFlagsOverrideEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("GIT_AI_SYNC_INTERVAL", "7")
	t.Setenv("GIT_AI_SYNC_STRATEGY", "rebase")
	t.Setenv("LOG_LEVEL", "DEBUG")

	cfg, err := Load(context.Background(), Flags{
		Interval: intPtr(3),
		Strategy: strPtr("merge"),
		LogLevel: strPtr("ERROR"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != 3*time.Second {
		t.Errorf("Interval = %v, want 3s", cfg.Interval)
	}
	if cfg.Strategy != "merge" {
		t.Errorf("Strategy = %q, want merge", cfg.Strategy)
	}
	if cfg.LogLevel != slog.LevelError {
		t.Errorf("LogLevel = %v, want ERROR", cfg.LogLevel)
	}
}

func TestUnprefixedLogLevelOnly(t *testing.T) {
	clearEnv(t)
	t.Setenv("GIT_AI_SYNC_LOG_LEVEL", "ERROR")

	cfg, err := Load(context.Background(), Flags{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want INFO (prefixed alias must be ignored)", cfg.LogLevel)
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "invalid interval", env: "GIT_AI_SYNC_INTERVAL", value: "soon"},
		{name: "invalid strategy", env: "GIT_AI_SYNC_STRATEGY", value: "squash"},
		{name: "invalid log level", env: "LOG_LEVEL", value: "LOUD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := Load(context.Background(), Flags{}); err == nil {
				t.Fatalf("expected an error for %s=%q", tc.env, tc.value)
			}
		})
	}
}

func TestZeroMaxWaitAllowed(t *testing.T) {
	clearEnv(t)
	t.Setenv("GIT_AI_SYNC_MAX_WAIT", "0")

	cfg, err := Load(context.Background(), Flags{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxWait != 0 {
		t.Errorf("MaxWait = %v, want 0", cfg.MaxWait)
	}
}

func TestNegativeIntervalRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("GIT_AI_SYNC_INTERVAL", "-1")

	if _, err := Load(context.Background(), Flags{}); err == nil {
		t.Fatal("expected a negative interval to be rejected")
	}
}
