package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func TestLoadFilePrecedence(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	vaultA := filepath.Join(dir, "vault-a")
	vaultB := filepath.Join(dir, "vault-b")
	path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\n  - %s\ninterval: 45\nstrategy: rebase\n", vaultA, vaultB))

	cfg, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Interval != 45*time.Second {
		t.Errorf("Interval = %v, want 45s", cfg.Interval)
	}
	if cfg.Strategy != "rebase" {
		t.Errorf("Strategy = %q, want rebase", cfg.Strategy)
	}
	if len(cfg.Vaults) != 2 {
		t.Errorf("len(Vaults) = %d, want 2", len(cfg.Vaults))
	}

	t.Setenv("GIT_AI_SYNC_INTERVAL", "99")
	t.Setenv("GIT_AI_SYNC_STRATEGY", "merge")

	cfg, err = LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err != nil {
		t.Fatalf("LoadFile with env: %v", err)
	}
	if cfg.Interval != 45*time.Second {
		t.Errorf("Interval = %v, want 45s (file must beat environment)", cfg.Interval)
	}
	if cfg.Strategy != "rebase" {
		t.Errorf("Strategy = %q, want rebase (file must beat environment)", cfg.Strategy)
	}

	cfg, err = LoadFile(context.Background(), FileOptions{ConfigPath: &path, Interval: intPtr(7)})
	if err != nil {
		t.Fatalf("LoadFile with flag: %v", err)
	}
	if cfg.Interval != 7*time.Second {
		t.Errorf("Interval = %v, want 7s (flag must beat file)", cfg.Interval)
	}
}

func TestLoadFileEnvironmentFallbacks(t *testing.T) {
	clearEnv(t)

	vault := filepath.Join(t.TempDir(), "vault")
	path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\n", vault))

	t.Setenv("GIT_AI_SYNC_INTERVAL", "7")
	t.Setenv("GIT_AI_SYNC_MAX_WAIT", "11")
	t.Setenv("GIT_AI_SYNC_COMMIT_PREFIX", "sync")
	t.Setenv("GIT_AI_SYNC_STRATEGY", "rebase")
	t.Setenv("LOG_LEVEL", "DEBUG")

	cfg, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
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

func TestLoadFileMissingFile(t *testing.T) {
	clearEnv(t)

	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err == nil {
		t.Fatal("expected an error for a missing config file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestLoadFileUnknownKey(t *testing.T) {
	clearEnv(t)

	vault := filepath.Join(t.TempDir(), "vault")
	path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\nbogus: 1\n", vault))

	_, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestLoadFileMalformedInterval(t *testing.T) {
	clearEnv(t)

	vault := filepath.Join(t.TempDir(), "vault")
	path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\ninterval: not-a-number\n", vault))

	_, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err == nil {
		t.Fatal("expected an error for a malformed interval")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestLoadFileStallThreshold(t *testing.T) {
	clearEnv(t)

	vault := filepath.Join(t.TempDir(), "vault")

	t.Run("below minimum is rejected", func(t *testing.T) {
		path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\nstall_threshold: 1\n", vault))
		if _, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path}); err == nil {
			t.Fatal("expected stall_threshold 1 to be rejected")
		}
	})

	t.Run("minimum is accepted", func(t *testing.T) {
		path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\nstall_threshold: 2\n", vault))
		cfg, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
		if err != nil {
			t.Fatalf("LoadFile: %v", err)
		}
		if cfg.StallThreshold != 2 {
			t.Errorf("StallThreshold = %d, want 2", cfg.StallThreshold)
		}
	})

	t.Run("absent uses the default", func(t *testing.T) {
		path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\n", vault))
		cfg, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
		if err != nil {
			t.Fatalf("LoadFile: %v", err)
		}
		if cfg.StallThreshold != 10 {
			t.Errorf("StallThreshold = %d, want 10", cfg.StallThreshold)
		}
	})
}

func TestLoadFileDefaults(t *testing.T) {
	clearEnv(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	vault := filepath.Join(t.TempDir(), "vault")
	path := writeConfigFile(t, fmt.Sprintf("vaults:\n  - %s\n", vault))

	cfg, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	wantStateFile := filepath.Join(home, ".local", "state", "git-ai-sync", "status.json")
	if cfg.StateFile != wantStateFile {
		t.Errorf("StateFile = %q, want %q", cfg.StateFile, wantStateFile)
	}
	if cfg.AttentionStoreURL != "http://localhost:18080" {
		t.Errorf("AttentionStoreURL = %q, want http://localhost:18080", cfg.AttentionStoreURL)
	}

	wantConfigPath := filepath.Join(home, ".config", "git-ai-sync", "config.yaml")
	gotConfigPath, err := DefaultConfigPath(context.Background())
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if gotConfigPath != wantConfigPath {
		t.Errorf("DefaultConfigPath = %q, want %q", gotConfigPath, wantConfigPath)
	}

	gotStateFile, err := DefaultStateFile(context.Background())
	if err != nil {
		t.Fatalf("DefaultStateFile: %v", err)
	}
	if gotStateFile != wantStateFile {
		t.Errorf("DefaultStateFile = %q, want %q", gotStateFile, wantStateFile)
	}
}

func TestLoadFileRejectsRelativeVault(t *testing.T) {
	clearEnv(t)

	path := writeConfigFile(t, "vaults:\n  - relative/path\n")

	_, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err == nil {
		t.Fatal("expected a relative vault path to be rejected")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestLoadFileRejectsEmptyVaults(t *testing.T) {
	clearEnv(t)

	path := writeConfigFile(t, "vaults: []\n")

	_, err := LoadFile(context.Background(), FileOptions{ConfigPath: &path})
	if err == nil {
		t.Fatal("expected an empty vault set to be rejected")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err, path)
	}
}

func TestCheckPermissions(t *testing.T) {
	clearEnv(t)

	t.Run("0600 is accepted", func(t *testing.T) {
		path := writeConfigFile(t, "vaults: []\n")
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := CheckPermissions(context.Background(), path); err != nil {
			t.Errorf("CheckPermissions(0600) = %v, want nil", err)
		}
	})

	t.Run("0644 is refused", func(t *testing.T) {
		path := writeConfigFile(t, "vaults: []\n")
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		err := CheckPermissions(context.Background(), path)
		if err == nil {
			t.Fatal("expected 0644 to be refused")
		}
		if !strings.Contains(err.Error(), "mode") || !strings.Contains(err.Error(), "0600") {
			t.Errorf("error %q does not name the mode and the fix", err)
		}
	})

	t.Run("0640 is refused", func(t *testing.T) {
		path := writeConfigFile(t, "vaults: []\n")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := CheckPermissions(context.Background(), path); err == nil {
			t.Fatal("expected 0640 to be refused")
		}
	})
}
