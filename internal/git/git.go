// Package git runs the git commands a sync cycle needs.
package git

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// Strategy selects how a pull integrates remote commits.
type Strategy string

const (
	// StrategyMerge integrates with a merge commit (git pull --no-rebase).
	StrategyMerge Strategy = "merge"
	// StrategyRebase replays local commits on top of the remote (git pull --rebase).
	StrategyRebase Strategy = "rebase"
)

// ErrConflict reports that a pull stopped in a merge or rebase conflict state.
var ErrConflict = stderrors.New("pull conflict")

// FindRepo resolves path and walks up until it finds a directory containing .git.
func FindRepo(ctx context.Context, path string) (string, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve path %s", path)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.Errorf(ctx, "not a git repository: %s", path)
}

// HasChanges reports whether the worktree or index has uncommitted changes.
func HasChanges(ctx context.Context, repo string) (bool, error) {
	out, err := runGit(ctx, repo, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(out)) > 0, nil
}

// StageAll stages every change in the worktree.
func StageAll(ctx context.Context, repo string) error {
	_, err := runGit(ctx, repo, "add", ".")
	return err
}

// Commit creates a commit with the given message. Any non-zero exit is returned
// as an error; there are no special refusal classes.
func Commit(ctx context.Context, repo string, message string) error {
	_, err := runGit(ctx, repo, "commit", "-m", message)
	return err
}

// Pull integrates remote commits with the given strategy. A pull that stops in a
// merge or rebase conflict state returns an error wrapping ErrConflict.
func Pull(ctx context.Context, repo string, strategy Strategy) error {
	args := []string{"pull", "--no-rebase"}
	if strategy == StrategyRebase {
		args = []string{"pull", "--rebase"}
	}
	out, err := runGit(ctx, repo, args...)
	if err == nil {
		return nil
	}
	if inConflictState(repo, strategy) {
		return errors.Wrapf(ctx, ErrConflict, "pull conflict: %s", out)
	}
	return err
}

func inConflictState(repo string, strategy Strategy) bool {
	if strategy == StrategyRebase {
		if _, err := os.Stat(filepath.Join(repo, ".git", "rebase-merge")); err == nil {
			return true
		}
		_, err := os.Stat(filepath.Join(repo, ".git", "rebase-apply"))
		return err == nil
	}
	_, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	return err == nil
}

// Push pushes the current branch to its upstream.
func Push(ctx context.Context, repo string) error {
	_, err := runGit(ctx, repo, "push")
	return err
}

// IsAheadOfRemote reports whether HEAD has commits not on its upstream. A missing
// upstream or a failed command is reported as (false, nil), matching the reference
// tool's "not ahead" behaviour.
func IsAheadOfRemote(ctx context.Context, repo string) (bool, error) {
	out, err := runGit(ctx, repo, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return false, nil
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, nil
	}
	return count > 0, nil
}

// GenerateCommitMessage formats "<prefix>: <RFC3339 timestamp>".
func GenerateCommitMessage(prefix string, now time.Time) string {
	return fmt.Sprintf("%s: %s", prefix, now.Format(time.RFC3339))
}

func runGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), errors.Wrapf(ctx, err, "git %s: %s", strings.Join(args, " "), string(out))
	}
	return string(out), nil
}
