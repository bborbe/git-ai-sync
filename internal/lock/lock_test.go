package lock

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireWritesPID(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = l.Release(context.Background()) }()

	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := string(data), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lock contents = %q, want %q", got, want)
	}
}

func TestAcquireCorrectsModeTo0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("pre-create lock file: %v", err)
	}

	l, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = l.Release(context.Background()) }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("lock mode = %o, want 600", got)
	}
}

func TestSecondAcquireRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	_, err = Acquire(context.Background(), dir)
	if err == nil {
		t.Fatal("second Acquire succeeded, want refusal")
	}
	if !stderrors.Is(err, ErrLocked) {
		t.Errorf("error does not wrap ErrLocked: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the holder PID %s", err, want)
	}

	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	third, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	_ = third.Release(context.Background())
}

func TestStaleFileDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("999999"), 0o600); err != nil {
		t.Fatalf("pre-create lock file: %v", err)
	}

	l, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire over stale file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := string(data), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("lock contents = %q, want %q", got, want)
	}
	_ = l.Release(context.Background())
}

func TestReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	_ = again.Release(context.Background())
}

func TestReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}
