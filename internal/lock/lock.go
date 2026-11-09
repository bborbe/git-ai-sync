// Package lock provides the single-instance repository lock.
package lock

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bborbe/errors"
)

// FileName is the lock file name in the repository root.
const FileName = ".git-ai-sync.lock"

// ErrLocked reports that another instance already holds the repository lock.
var ErrLocked = stderrors.New("another instance is already running")

// Lock is a held repository lock.
type Lock struct {
	file *os.File
}

// Acquire takes an exclusive non-blocking lock on <repoRoot>/.git-ai-sync.lock,
// corrects the file mode to 0600, and writes the current PID. A second acquisition
// on the same repository returns an error wrapping ErrLocked and, when readable,
// naming the holder's PID.
func Acquire(ctx context.Context, repoRoot string) (*Lock, error) {
	path := filepath.Join(repoRoot, FileName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "open lock file %s", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "chmod lock file %s", path)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if stderrors.Is(err, syscall.EWOULDBLOCK) || stderrors.Is(err, syscall.EAGAIN) {
			pid := readPID(file)
			_ = file.Close()
			if pid != "" {
				return nil, errors.Wrapf(ctx, ErrLocked, "another instance is already running (pid %s)", pid)
			}
			return nil, errors.Wrapf(ctx, ErrLocked, "another instance is already running")
		}
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "flock lock file %s", path)
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "truncate lock file %s", path)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "seek lock file %s", path)
	}
	if _, err := file.WriteString(fmt.Sprintf("%d", os.Getpid())); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "write lock file %s", path)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, errors.Wrapf(ctx, err, "sync lock file %s", path)
	}
	return &Lock{file: file}, nil
}

func readPID(file *os.File) string {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Release unlocks and closes the lock file. It is safe to call more than once.
func (l *Lock) Release(ctx context.Context) error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		firstErr = errors.Wrap(ctx, err, "unlock lock file")
	}
	if err := l.file.Close(); err != nil && firstErr == nil {
		firstErr = errors.Wrap(ctx, err, "close lock file")
	}
	l.file = nil
	return firstErr
}
