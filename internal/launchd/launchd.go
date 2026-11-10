// Package launchd renders and loads the single supervisor launchd agent.
package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	stderrors "errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/config"
)

// Label is the single supervisor agent's launchd label.
const Label = "com.github.bborbe.git-ai-sync"

const plistFileName = Label + ".plist"

const plistPathValue = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// Options configures install, uninstall and status.
type Options struct {
	Config     config.FileConfig
	Logger     *slog.Logger
	HomeDir    string // defaults to os.UserHomeDir()
	Launchctl  string // defaults to "launchctl"
	BinaryPath string // defaults to the symlink-resolved os.Executable()
	UID        int    // defaults to os.Getuid()
}

// ResolveOptions fills the defaults for the options install, uninstall and
// status need.
func ResolveOptions(ctx context.Context, opts Options) (Options, error) {
	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Options{}, errors.Wrapf(ctx, err, "resolve home directory")
		}
		opts.HomeDir = home
	}
	if opts.Launchctl == "" {
		opts.Launchctl = "launchctl"
	}
	if opts.UID == 0 {
		opts.UID = os.Getuid()
	}
	if opts.BinaryPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return Options{}, errors.Wrapf(ctx, err, "resolve executable path")
		}
		resolved, err := filepath.EvalSymlinks(executable)
		if err != nil {
			return Options{}, errors.Wrapf(ctx, err, "resolve executable symlinks")
		}
		opts.BinaryPath = resolved
	}
	return opts, nil
}

// PlistPath returns <home>/Library/LaunchAgents/<label>.plist.
func PlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", plistFileName)
}

// LogDir returns <home>/Library/Logs/git-ai-sync.
func LogDir(home string) string {
	return filepath.Join(home, "Library", "Logs", "git-ai-sync")
}

// LogPath returns <home>/Library/Logs/git-ai-sync/git-ai-sync.log.
func LogPath(home string) string {
	return filepath.Join(LogDir(home), "git-ai-sync.log")
}

// RenderPlist returns the deterministic plist XML.
func RenderPlist(opts Options) ([]byte, error) {
	if opts.BinaryPath == "" {
		return nil, stderrors.New("binary path is required")
	}
	if opts.Config.ConfigPath == "" {
		return nil, stderrors.New("config path is required")
	}

	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n")
	b.WriteString("<dict>\n")
	writeKeyValue(&b, "Label", Label)
	b.WriteString("  <key>ProgramArguments</key>\n")
	b.WriteString("  <array>\n")
	for _, argument := range []string{opts.BinaryPath, "supervise", "--config", opts.Config.ConfigPath} {
		fmt.Fprintf(&b, "    <string>%s</string>\n", escape(argument))
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>ThrottleInterval</key>\n  <integer>30</integer>\n")
	b.WriteString("  <key>ProcessType</key>\n  <string>Standard</string>\n")
	b.WriteString("  <key>SoftResourceLimits</key>\n")
	b.WriteString("  <dict>\n")
	b.WriteString("    <key>NumberOfFiles</key>\n")
	b.WriteString("    <integer>4096</integer>\n")
	b.WriteString("  </dict>\n")
	b.WriteString("  <key>HardResourceLimits</key>\n")
	b.WriteString("  <dict>\n")
	b.WriteString("    <key>NumberOfFiles</key>\n")
	b.WriteString("    <integer>8192</integer>\n")
	b.WriteString("  </dict>\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n")
	b.WriteString("  <dict>\n")
	b.WriteString("    <key>PATH</key>\n")
	fmt.Fprintf(&b, "    <string>%s</string>\n", escape(plistPathValue))
	b.WriteString("  </dict>\n")
	writeKeyValue(&b, "StandardOutPath", LogPath(opts.HomeDir))
	writeKeyValue(&b, "StandardErrorPath", LogPath(opts.HomeDir))
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")

	return []byte(b.String()), nil
}

// Install creates the log directory, writes the plist atomically, boots the
// job out, and bootstraps it. A failed bootstrap returns an error naming the
// manual bootstrap command.
func Install(ctx context.Context, opts Options) error {
	resolved, err := ResolveOptions(ctx, opts)
	if err != nil {
		return errors.Wrapf(ctx, err, "resolve launchd options")
	}
	logger := loggerOr(resolved.Logger)

	if err := config.CheckPermissions(ctx, resolved.Config.ConfigPath); err != nil {
		return errors.Wrapf(ctx, err, "check config permissions")
	}

	if err := os.MkdirAll(LogDir(resolved.HomeDir), 0o755); err != nil {
		return errors.Wrapf(ctx, err, "create log directory %s", LogDir(resolved.HomeDir))
	}
	plistPath := PlistPath(resolved.HomeDir)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return errors.Wrapf(ctx, err, "create launch agent directory %s", filepath.Dir(plistPath))
	}

	data, err := RenderPlist(resolved)
	if err != nil {
		return errors.Wrapf(ctx, err, "render plist")
	}
	if err := writePlist(ctx, plistPath, data); err != nil {
		return errors.Wrapf(ctx, err, "write plist %s", plistPath)
	}

	target := fmt.Sprintf("gui/%d/%s", resolved.UID, Label)
	if out, err := runLaunchctl(ctx, resolved, "bootout", target); err != nil {
		logger.Warn("launchctl bootout failed", "target", target, "output", strings.TrimSpace(out), "error", err)
	}

	if out, err := runLaunchctl(ctx, resolved, "bootstrap", fmt.Sprintf("gui/%d", resolved.UID), plistPath); err != nil {
		return errors.Errorf(ctx, "launchctl bootstrap failed: %v\nRun manually:\n  launchctl bootstrap gui/%d %s\n%s", err, resolved.UID, plistPath, strings.TrimSpace(out))
	}
	return nil
}

// Uninstall boots the job out (only when a plist exists) and removes the plist.
func Uninstall(ctx context.Context, opts Options) error {
	resolved, err := ResolveOptions(ctx, opts)
	if err != nil {
		return errors.Wrapf(ctx, err, "resolve launchd options")
	}
	logger := loggerOr(resolved.Logger)
	plistPath := PlistPath(resolved.HomeDir)

	if _, err := os.Stat(plistPath); err != nil {
		if stderrors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return errors.Wrapf(ctx, err, "stat plist %s", plistPath)
	}

	target := fmt.Sprintf("gui/%d/%s", resolved.UID, Label)
	if out, err := runLaunchctl(ctx, resolved, "bootout", target); err != nil {
		logger.Warn("launchctl bootout failed", "target", target, "output", strings.TrimSpace(out), "error", err)
	}

	if err := os.Remove(plistPath); err != nil && !stderrors.Is(err, fs.ErrNotExist) {
		return errors.Wrapf(ctx, err, "remove plist %s", plistPath)
	}
	return nil
}

// IsLoaded reports whether `launchctl print gui/<uid>/<label>` exits 0.
func IsLoaded(ctx context.Context, opts Options) (bool, error) {
	resolved, err := ResolveOptions(ctx, opts)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "resolve launchd options")
	}
	_, err = runLaunchctl(ctx, resolved, "print", fmt.Sprintf("gui/%d/%s", resolved.UID, Label))
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return false, nil
	}
	return false, errors.Wrapf(ctx, err, "launchctl print")
}

func runLaunchctl(ctx context.Context, opts Options, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, opts.Launchctl, args...).CombinedOutput()
	return string(out), err
}

func writePlist(ctx context.Context, plistPath string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(plistPath), ".plist-*")
	if err != nil {
		return errors.Wrapf(ctx, err, "create temporary plist")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(ctx, err, "write temporary plist")
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(ctx, err, "chmod temporary plist")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(ctx, err, "sync temporary plist")
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(ctx, err, "close temporary plist")
	}
	if err := os.Rename(tmpName, plistPath); err != nil {
		return errors.Wrapf(ctx, err, "rename plist into place")
	}
	return nil
}

func loggerOr(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func writeKeyValue(b *strings.Builder, key string, value string) {
	fmt.Fprintf(b, "  <key>%s</key>\n", escape(key))
	fmt.Fprintf(b, "  <string>%s</string>\n", escape(value))
}

func escape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}
