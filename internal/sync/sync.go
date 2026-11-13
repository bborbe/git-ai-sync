// Package sync performs one stage, commit, pull, push cycle.
package sync

import (
	"context"
	"strings"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/clock"
	"github.com/bborbe/git-ai-sync/internal/git"
)

// Options controls one sync cycle.
type Options struct {
	Strategy      git.Strategy
	CommitPrefix  string
	PullWhenClean bool
	Clock         clock.Clock
}

// Result reports what a cycle did. CommitErr is set when the commit was refused;
// the cycle still ran the pull, and the caller logs CommitErr.
type Result struct {
	HadChanges bool
	Committed  bool
	CommitErr  error
	// Blocked names the paths that could not be committed because the
	// pre-commit hook refused them. They stay staged for the next cycle.
	Blocked   []string
	Pulled    bool
	Pushed    bool
	NoChanges bool
}

// Run performs one sync cycle.
func Run(ctx context.Context, repo string, opts Options) (Result, error) {
	// A repository that is still mid-merge or mid-rebase is handed back to the
	// conflict resolver. Staging here would commit conflict markers, and a repo
	// whose conflicts are all resolved and staged still needs its continue step.
	if git.IsInMerge(repo) || git.IsInRebase(repo) {
		return Result{}, errors.Wrapf(ctx, git.ErrConflict, "repository is still in a conflict state")
	}
	dirty, err := git.HasChanges(ctx, repo)
	if err != nil {
		return Result{}, err
	}
	if !dirty {
		return runClean(ctx, repo, opts)
	}
	return runDirty(ctx, repo, opts)
}

func runClean(ctx context.Context, repo string, opts Options) (Result, error) {
	if !opts.PullWhenClean {
		return Result{NoChanges: true}, nil
	}
	if err := git.Pull(ctx, repo, opts.Strategy); err != nil {
		return Result{}, err
	}
	ahead, err := git.IsAheadOfRemote(ctx, repo)
	if err != nil {
		return Result{Pulled: true}, err
	}
	pushed := false
	if ahead {
		if err := git.Push(ctx, repo); err != nil {
			return Result{Pulled: true}, err
		}
		pushed = true
	}
	return Result{Pulled: true, Pushed: pushed}, nil
}

func runDirty(ctx context.Context, repo string, opts Options) (Result, error) {
	// Check before StageAll: a conflicted autostash leaves unmerged entries in
	// the worktree, and git add . would sweep their conflict markers into the
	// next commit. Stop and name the state instead.
	unmerged, err := git.HasUnmergedPaths(ctx, repo)
	if err != nil {
		return Result{HadChanges: true}, err
	}
	if unmerged {
		return Result{HadChanges: true}, unmergedError(ctx, repo)
	}
	if err := git.StageAll(ctx, repo); err != nil {
		return Result{HadChanges: true}, err
	}
	message := git.GenerateCommitMessage(opts.CommitPrefix, opts.Clock.Now())
	committed, blocked, commitErr := commitDirty(ctx, repo, message)
	pull := git.Pull
	if len(blocked) > 0 {
		pull = git.PullWithAutostash
	}
	if err := pull(ctx, repo, opts.Strategy); err != nil {
		return Result{
			HadChanges: true,
			Committed:  committed,
			CommitErr:  commitErr,
			Blocked:    blocked,
		}, err
	}
	if err := git.Push(ctx, repo); err != nil {
		return Result{
			HadChanges: true,
			Committed:  committed,
			CommitErr:  commitErr,
			Blocked:    blocked,
			Pulled:     true,
		}, err
	}
	return Result{
		HadChanges: true,
		Committed:  committed,
		CommitErr:  commitErr,
		Blocked:    blocked,
		Pulled:     true,
		Pushed:     true,
	}, nil
}

// unmergedError reports a pre-existing unmerged state and how to clear it.
func unmergedError(ctx context.Context, repo string) error {
	files, err := git.ConflictedFiles(ctx, repo)
	if err != nil {
		return err
	}
	return errors.Wrapf(
		ctx,
		git.ErrConflict,
		"unmerged paths: %s; a leftover autostash from a previous pull left conflict markers in the worktree — resolve the conflict and run git stash drop, because git-ai-sync resolve requires MERGE_HEAD or a rebase directory and cannot clear this state",
		strings.Join(files, ", "),
	)
}

// commitDirty commits the staged changes. When the single commit is refused, it
// isolates the refused units and commits the rest, leaving the refused paths
// staged. It returns whether anything committed, the paths that could not be
// committed, and the error the caller should report.
func commitDirty(ctx context.Context, repo, message string) (bool, []string, error) {
	commitErr := git.Commit(ctx, repo, message)
	if commitErr == nil {
		return true, nil, nil
	}
	units, err := git.StagedFiles(ctx, repo)
	if err != nil {
		return false, nil, commitErr
	}
	committed, blocked := commitUnits(ctx, repo, message, units)
	if committed == 0 {
		return false, nil, commitErr
	}
	if len(blocked) == 0 {
		return true, nil, nil
	}
	return true, blocked, errors.Wrapf(
		ctx, commitErr, "blocked paths: %s", strings.Join(blocked, ", "),
	)
}

// commitUnits probes three spread units (first, middle, last) before bisecting.
// When every probe fails the refusal is global — a hook rejecting everything,
// index.lock, a signing failure — so it returns nothing committed and no
// blocked paths, letting the caller report the original commit error without
// bisecting hundreds of units. One surviving probe proves the set is a mix and
// the rest is bisected. Three probes, not two: two blocked files cannot fail
// all three, so a vault with exactly two refused paths is not misread as global.
// It never splits a unit, so a rename's two paths always travel in the same call.
func commitUnits(ctx context.Context, repo, message string, units [][]string) (int, []string) {
	if len(units) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, flattenUnits(units)
	}
	if len(units) < 3 {
		return commitBisect(ctx, repo, message, units)
	}
	probes := []int{0, len(units) / 2, len(units) - 1}
	committed := 0
	committedProbe := make(map[int]bool, len(probes))
	for _, idx := range probes {
		if err := git.CommitPaths(ctx, repo, message, units[idx]); err != nil {
			continue
		}
		committed++
		committedProbe[idx] = true
	}
	if committed == 0 {
		return 0, nil
	}
	var remaining [][]string
	for i, unit := range units {
		if !committedProbe[i] {
			remaining = append(remaining, unit)
		}
	}
	restCommitted, restBlocked := commitBisect(ctx, repo, message, remaining)
	return committed + restCommitted, restBlocked
}

// commitBisect splits the pending units at unit boundaries, tries each half as
// one commit, and recurses into a half that fails, treating a single failing
// unit as blocked. Units that commit are absent from the returned blocked list;
// blocked paths stay staged.
func commitBisect(ctx context.Context, repo, message string, units [][]string) (int, []string) {
	if len(units) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, flattenUnits(units)
	}
	if len(units) == 1 {
		if err := git.CommitPaths(ctx, repo, message, units[0]); err != nil {
			return 0, units[0]
		}
		return 1, nil
	}
	mid := len(units) / 2
	committedLeft, blockedLeft := commitHalf(ctx, repo, message, units[:mid])
	committedRight, blockedRight := commitHalf(ctx, repo, message, units[mid:])
	return committedLeft + committedRight, append(blockedLeft, blockedRight...)
}

// commitHalf tries a half's units as one commit and recurses into the half when
// the commit fails, so a refused unit is isolated without splitting a unit.
func commitHalf(ctx context.Context, repo, message string, units [][]string) (int, []string) {
	if err := git.CommitPaths(ctx, repo, message, flattenUnits(units)); err == nil {
		return len(units), nil
	}
	return commitBisect(ctx, repo, message, units)
}

func flattenUnits(units [][]string) []string {
	var paths []string
	for _, unit := range units {
		paths = append(paths, unit...)
	}
	return paths
}
