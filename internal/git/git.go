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

// PullWithAutostash integrates remote commits with the given strategy, stashing
// and restoring local changes around the merge (git pull --autostash). It lets a
// pull proceed while a blocked path is still staged.
func PullWithAutostash(ctx context.Context, repo string, strategy Strategy) error {
	args := []string{"pull", "--no-rebase", "--autostash"}
	if strategy == StrategyRebase {
		args = []string{"pull", "--rebase", "--autostash"}
	}
	out, err := runGit(ctx, repo, args...)
	if err == nil {
		// git pull --autostash exits 0 even when reapplying the stash conflicts:
		// the path is left unmerged in the worktree and no MERGE_HEAD exists, so
		// inConflictState cannot see it. Detect it here or the failure is silent.
		return autostashConflict(ctx, repo)
	}
	if inConflictState(repo, strategy) {
		return errors.Wrapf(ctx, ErrConflict, "pull conflict: %s", out)
	}
	return err
}

// autostashConflict reports an error wrapping ErrConflict when the pull
// succeeded but left unmerged paths behind, which happens when the stashed
// changes conflict with the merged result.
func autostashConflict(ctx context.Context, repo string) error {
	files, err := ConflictedFiles(ctx, repo)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrConflict,
		"autostash pop conflict: unmerged paths: %s; the autostash is retained — resolve the conflict and run git stash drop, because git-ai-sync resolve requires MERGE_HEAD or a rebase directory and cannot clear this state",
		strings.Join(files, ", "),
	)
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

// IsInMerge reports whether repo is in a merge state (.git/MERGE_HEAD exists).
func IsInMerge(repo string) bool {
	_, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	return err == nil
}

// IsInRebase reports whether repo is in a rebase state
// (.git/rebase-merge or .git/rebase-apply exists).
func IsInRebase(repo string) bool {
	if _, err := os.Stat(filepath.Join(repo, ".git", "rebase-merge")); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(repo, ".git", "rebase-apply"))
	return err == nil
}

// ConflictedFiles returns the paths with unresolved conflicts
// (git diff --name-only --diff-filter=U), relative to repo.
func ConflictedFiles(ctx context.Context, repo string) ([]string, error) {
	out, err := runGit(ctx, repo, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "list conflicted files")
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// HasUnmergedPaths reports whether the worktree has unresolved merge entries.
func HasUnmergedPaths(ctx context.Context, repo string) (bool, error) {
	files, err := ConflictedFiles(ctx, repo)
	if err != nil {
		return false, err
	}
	return len(files) > 0, nil
}

// HasAutostash reports whether a retained autostash entry exists. git pull
// --autostash stores its entry with the subject "autostash", which stash list
// renders as "stash@{0}: autostash"; a plain stash renders as
// "stash@{0}: WIP on master: <sha> <subject>". Matching the last colon-separated
// segment therefore finds the autostash and not the operator's own stashes. An
// operator stash created with -m autostash matches too, which is intended: both
// are autostash-shaped and dropping is the right recovery for either.
func HasAutostash(ctx context.Context, repo string) (bool, error) {
	out, err := runGit(ctx, repo, "stash", "list")
	if err != nil {
		return false, errors.Wrapf(ctx, err, "list stashes")
	}
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.LastIndex(line, ": "); idx >= 0 && line[idx+2:] == "autostash" {
			return true, nil
		}
	}
	return false, nil
}

// StagedFiles returns the staged changes as commit units
// (git diff --cached --name-status -z). Each unit holds one path for an add,
// modify, or delete, and both paths for a rename, so a partial commit cannot
// split a rename into an add plus a stale original. -z keeps records
// NUL-separated and unquoted, so a non-ASCII path reaches CommitPaths verbatim
// instead of as a C-quoted literal that git rejects as a pathspec.
func StagedFiles(ctx context.Context, repo string) ([][]string, error) {
	out, err := runGit(ctx, repo, "diff", "--cached", "--name-status", "-z")
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "list staged files")
	}
	fields := strings.Split(out, "\x00")
	var units [][]string
	for i := 0; i < len(fields); i++ {
		status := fields[i]
		if status == "" {
			continue
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+2 >= len(fields) {
				break
			}
			units = append(units, []string{fields[i+1], fields[i+2]})
			i += 2
			continue
		}
		if i+1 >= len(fields) {
			break
		}
		units = append(units, []string{fields[i+1]})
		i++
	}
	return units, nil
}

// CommitPaths commits only the named paths
// (git commit --only -m <message> -- <paths>), leaving every other staged path
// in the index.
func CommitPaths(ctx context.Context, repo, message string, paths []string) error {
	args := append([]string{"commit", "--only", "-m", message, "--"}, paths...)
	_, err := runGit(ctx, repo, args...)
	if err != nil {
		return errors.Wrapf(ctx, err, "commit %s", strings.Join(paths, ", "))
	}
	return nil
}

// StageFile stages one path (git add -- <path>).
func StageFile(ctx context.Context, repo string, path string) error {
	_, err := runGit(ctx, repo, "add", "--", path)
	if err != nil {
		return errors.Wrapf(ctx, err, "stage %s", path)
	}
	return nil
}

// ContinueMerge completes a merge after its conflicts are resolved
// (git commit --no-edit).
func ContinueMerge(ctx context.Context, repo string) error {
	_, err := runGit(ctx, repo, "commit", "--no-edit")
	if err != nil {
		return errors.Wrapf(ctx, err, "continue merge")
	}
	return nil
}

// ContinueRebase continues a rebase after its conflicts are resolved
// (git rebase --continue).
func ContinueRebase(ctx context.Context, repo string) error {
	_, err := runGit(ctx, repo, "rebase", "--continue")
	if err != nil {
		return errors.Wrapf(ctx, err, "continue rebase")
	}
	return nil
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

// RevParse resolves ref to its commit sha (git rev-parse <ref>).
func RevParse(ctx context.Context, repo string, ref string) (string, error) {
	out, err := runGit(ctx, repo, "rev-parse", ref)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "rev-parse %s", ref)
	}
	return strings.TrimSpace(out), nil
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
