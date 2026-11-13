// Package sync performs one stage, commit, pull, push cycle.
package sync

import (
	"context"

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
	Pulled     bool
	Pushed     bool
	NoChanges  bool
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
	if err := git.StageAll(ctx, repo); err != nil {
		return Result{HadChanges: true}, err
	}
	message := git.GenerateCommitMessage(opts.CommitPrefix, opts.Clock.Now())
	commitErr := git.Commit(ctx, repo, message)
	if err := git.Pull(ctx, repo, opts.Strategy); err != nil {
		return Result{HadChanges: true, Committed: commitErr == nil, CommitErr: commitErr}, err
	}
	if err := git.Push(ctx, repo); err != nil {
		return Result{
			HadChanges: true,
			Committed:  commitErr == nil,
			CommitErr:  commitErr,
			Pulled:     true,
		}, err
	}
	return Result{
		HadChanges: true,
		Committed:  commitErr == nil,
		CommitErr:  commitErr,
		Pulled:     true,
		Pushed:     true,
	}, nil
}
