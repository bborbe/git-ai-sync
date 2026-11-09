// Package changetracker records the newest qualifying filesystem change.
package changetracker

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	"github.com/fsnotify/fsnotify"

	"github.com/bborbe/git-ai-sync/internal/clock"
)

// NeverChanged is the duration returned when no qualifying change has been seen.
const NeverChanged time.Duration = math.MaxInt64

// ShouldIgnore reports whether a filesystem event is the watcher's own noise.
// It ignores directories, anything under .git/, *~ backups, *.swp, *.tmp, and
// dot-prefixed names.
func ShouldIgnore(path string, isDir bool) bool {
	if isDir {
		return true
	}
	slashed := filepath.ToSlash(path)
	if strings.Contains(slashed, "/.git/") || strings.HasSuffix(slashed, "/.git") {
		return true
	}
	if strings.HasSuffix(path, "~") || strings.HasSuffix(path, ".swp") || strings.HasSuffix(path, ".tmp") {
		return true
	}
	return strings.HasPrefix(filepath.Base(path), ".")
}

// Tracker records the time of the newest qualifying filesystem change.
type Tracker struct {
	watchPath string
	clk       clock.Clock

	mu         sync.RWMutex
	lastChange time.Time

	watcher  *fsnotify.Watcher
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New returns a Tracker watching watchPath and reading time from clk.
func New(watchPath string, clk clock.Clock) *Tracker {
	return &Tracker{
		watchPath: watchPath,
		clk:       clk,
		done:      make(chan struct{}),
	}
}

// Start begins recursive filesystem watching.
func (t *Tracker) Start(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.Wrapf(ctx, err, "watch %s", t.watchPath)
	}
	if err := t.addRecursive(watcher, t.watchPath); err != nil {
		_ = watcher.Close()
		return errors.Wrapf(ctx, err, "watch %s", t.watchPath)
	}
	t.watcher = watcher
	t.wg.Add(1)
	go t.run()
	return nil
}

func (t *Tracker) addRecursive(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if err := watcher.Add(path); err != nil {
			return err
		}
		return nil
	})
}

func (t *Tracker) run() {
	defer t.wg.Done()
	for {
		select {
		case <-t.done:
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			t.handleWatcherEvent(event)
		case <-t.watcher.Errors:
			// Watcher errors do not stop the tracker.
		}
	}
}

func (t *Tracker) handleWatcherEvent(event fsnotify.Event) {
	isDir := false
	if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
		isDir = true
	}
	if event.Has(fsnotify.Create) && isDir {
		_ = t.addRecursive(t.watcher, event.Name)
	}
	t.HandleEvent(event.Name, isDir)
}

// Stop stops watching. It is safe to call more than once.
func (t *Tracker) Stop() {
	t.stopOnce.Do(func() {
		close(t.done)
		if t.watcher != nil {
			_ = t.watcher.Close()
		}
	})
	t.wg.Wait()
}

// HandleEvent records a change for path unless ShouldIgnore rejects it. It is
// exported so tests can drive the ignore and reset rules deterministically.
func (t *Tracker) HandleEvent(path string, isDir bool) {
	if ShouldIgnore(path, isDir) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastChange = t.clk.Now()
}

// SecondsSinceLastChange reports the time since the newest qualifying change.
// It returns NeverChanged when no qualifying change has been seen.
func (t *Tracker) SecondsSinceLastChange(now time.Time) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastChange.IsZero() {
		return NeverChanged
	}
	return now.Sub(t.lastChange)
}
