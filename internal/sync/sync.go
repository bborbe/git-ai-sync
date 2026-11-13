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

// unmergedError reports a pre-existing unmerged state and how to clear it. A
// retained autostash is recovered by dropping the stash; a merge or rebase
// conflict is handed to git-ai-sync resolve, which stages the conflicted files
// itself, so the advice must not tell the operator to stage them first.
func unmergedError(ctx context.Context, repo string) error {
	files, err := git.ConflictedFiles(ctx, repo)
	if err != nil {
		return err
	}
	autostash, err := git.HasAutostash(ctx, repo)
	if err != nil {
		return errors.Wrapf(
			ctx,
			git.ErrConflict,
			"unmerged paths: %s; could not check for a retained autostash: %v; this is a merge or rebase conflict — run git-ai-sync resolve, which resolves and stages the conflicted files, commits the merge and pushes",
			strings.Join(files, ", "), err,
		)
	}
	if autostash {
		return errors.Wrapf(
			ctx,
			git.ErrConflict,
			"unmerged paths: %s; the autostash is retained — resolve the conflict and run git stash drop",
			strings.Join(files, ", "),
		)
	}
	return errors.Wrapf(
		ctx,
		git.ErrConflict,
		"unmerged paths: %s; this is a merge or rebase conflict — run git-ai-sync resolve, which resolves and stages the conflicted files, commits the merge and pushes",
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

// maxCommitAttempts bounds the subprocesses a single cycle spends isolating
// refused paths. A realistic vault stays far under it; a hook that refuses
// everything stops here instead of bisecting every path on every interval.
const maxCommitAttempts = 256

// commitUnits bisects the full unit set. It never splits a unit, so a rename's
// two paths always travel in the same call, and no unit is skipped: a refused
// path at any position is isolated and the rest commit.
func commitUnits(ctx context.Context, repo, message string, units [][]string) (int, []string) {
	if len(units) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, flattenUnits(units)
	}
	attempts := 0
	return commitBisect(ctx, repo, message, units, &attempts)
}

// commitBisect splits the pending units at unit boundaries, tries each half as
// one commit, and recurses into a half that fails, treating a single failing
// unit as blocked. Units that commit are absent from the returned blocked list;
// blocked paths stay staged. When the attempt budget is exhausted it stops
// recursing and reports every unit still pending in that subtree as blocked, so
// an uncommitted path is never dropped from the blocked list.
func commitBisect(ctx context.Context, repo, message string, units [][]string, attempts *int) (int, []string) {
	if len(units) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, flattenUnits(units)
	}
	if *attempts >= maxCommitAttempts {
		return 0, flattenUnits(units)
	}
	if len(units) == 1 {
		*attempts++
		if err := git.CommitPaths(ctx, repo, message, units[0]); err != nil {
			return 0, units[0]
		}
		return 1, nil
	}
	mid := len(units) / 2
	committedLeft, blockedLeft := commitHalf(ctx, repo, message, units[:mid], attempts)
	committedRight, blockedRight := commitHalf(ctx, repo, message, units[mid:], attempts)
	return committedLeft + committedRight, append(blockedLeft, blockedRight...)
}

// commitHalf tries a half's units as one commit and recurses into the half when
// the commit fails, so a refused unit is isolated without splitting a unit.
func commitHalf(ctx context.Context, repo, message string, units [][]string, attempts *int) (int, []string) {
	if *attempts >= maxCommitAttempts {
		return 0, flattenUnits(units)
	}
	*attempts++
	if err := git.CommitPaths(ctx, repo, message, flattenUnits(units)); err == nil {
		return len(units), nil
	}
	return commitBisect(ctx, repo, message, units, attempts)
}

func flattenUnits(units [][]string) []string {
	var paths []string
	for _, unit := range units {
		paths = append(paths, unit...)
	}
	return paths
}
