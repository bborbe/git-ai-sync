package sync

import (
	"context"

	"github.com/bborbe/errors"

	"github.com/bborbe/git-ai-sync/internal/conflict"
	"github.com/bborbe/git-ai-sync/internal/git"
)

// ResolveConflicts resolves the repository's conflicted files through the
// Claude CLI, continues the merge or rebase, and pushes. It stops at the first
// failure. The caller must already hold the repository lock.
func ResolveConflicts(ctx context.Context, repo string) error {
	files, err := git.ConflictedFiles(ctx, repo)
	if err != nil {
		return errors.Wrapf(ctx, err, "list conflicted files")
	}
	if len(files) == 0 {
		return errors.Errorf(ctx, "no conflicted files found in %s", repo)
	}

	if err := conflict.ResolveMarkerFlaggedFiles(ctx, repo, files); err != nil {
		return errors.Wrapf(ctx, err, "resolve conflicted files")
	}

	if git.IsInRebase(repo) {
		if err := git.ContinueRebase(ctx, repo); err != nil {
			return errors.Wrapf(ctx, err, "continue rebase")
		}
	} else {
		if err := git.ContinueMerge(ctx, repo); err != nil {
			return errors.Wrapf(ctx, err, "continue merge")
		}
	}

	if err := git.Push(ctx, repo); err != nil {
		return errors.Wrapf(ctx, err, "push after resolving conflicts")
	}
	return nil
}
