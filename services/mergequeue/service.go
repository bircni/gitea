// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mergequeue

import (
	"context"

	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	pull_model "gitea.dev/models/pull"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/globallock"
	"gitea.dev/modules/util"
	notify_service "gitea.dev/services/notify"
)

var (
	ErrNotRequired = util.NewInvalidArgumentErrorf("the base branch does not require a merge queue")
	ErrNotQueued   = util.NewNotExistErrorf("pull request is not in the merge queue")
)

// IsRequired reports whether pull requests into the branch must be merged through the merge queue
func IsRequired(ctx context.Context, repoID int64, branch string) (bool, *git_model.ProtectedBranch, error) {
	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, repoID, branch)
	if err != nil {
		return false, nil, err
	}
	return pb != nil && pb.RequireMergeQueue, pb, nil
}

// Enqueue adds the pull request to the merge queue, the caller checks that the doer may merge it and that it is mergeable
func Enqueue(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, jump bool) (*pull_model.MergeQueueEntry, error) {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return nil, err
	}
	if required, _, err := IsRequired(ctx, pr.BaseRepoID, pr.BaseBranch); err != nil {
		return nil, err
	} else if !required {
		return nil, ErrNotRequired
	}
	headSHA, err := git.GetFullCommitID(ctx, pr.BaseRepo, pr.GetGitHeadRefName())
	if err != nil {
		return nil, err
	}

	entry := &pull_model.MergeQueueEntry{
		RepoID:     pr.BaseRepoID,
		BaseBranch: pr.BaseBranch,
		PullID:     pr.ID,
		EnqueuerID: doer.ID,
		Jump:       jump,
		HeadSHA:    headSHA,
	}
	err = globallock.LockAndDo(ctx, lockKey(pr.BaseRepoID, pr.BaseBranch), func(ctx context.Context) error {
		if err := pull_model.AddToMergeQueue(ctx, entry); err != nil {
			return err
		}
		_, err := issues_model.CreateMergeQueueComment(ctx, pr, doer, true, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	notify_service.PullRequestMergeQueueChanged(ctx, doer, pr, true)
	addToQueue(pr.BaseRepoID, pr.BaseBranch)
	return entry, nil
}

// Dequeue removes the pull request from the merge queue, the entries after it are rebuilt without it
func Dequeue(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest) error {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return err
	}
	err := globallock.LockAndDo(ctx, lockKey(pr.BaseRepoID, pr.BaseBranch), func(ctx context.Context) error {
		entry, err := pull_model.GetMergeQueueEntryByPullID(ctx, pr.ID)
		if err != nil {
			return err
		} else if entry == nil {
			return ErrNotQueued
		}
		return removeEntry(ctx, pr.BaseRepo, entry, doer, issues_model.MergeQueueRemovedByUser)
	})
	if err != nil {
		return err
	}
	addToQueue(pr.BaseRepoID, pr.BaseBranch)
	return nil
}

// QueueEntry is an entry with its pull request, in merge order
type QueueEntry struct {
	*pull_model.MergeQueueEntry
	Position    int
	PullRequest *issues_model.PullRequest
}

// GetQueue returns the merge queue of a branch
func GetQueue(ctx context.Context, repoID int64, branch string) ([]*QueueEntry, error) {
	entries, err := pull_model.GetMergeQueueEntries(ctx, repoID, branch)
	if err != nil {
		return nil, err
	}
	result := make([]*QueueEntry, 0, len(entries))
	for i, e := range entries {
		pr, err := issues_model.GetPullRequestByID(ctx, e.PullID)
		if err != nil {
			return nil, err
		}
		if err := pr.LoadIssue(ctx); err != nil {
			return nil, err
		}
		result = append(result, &QueueEntry{MergeQueueEntry: e, Position: i + 1, PullRequest: pr})
	}
	return result, nil
}

// GetEntry returns the queue entry of the pull request, nil when it is not queued
func GetEntry(ctx context.Context, pr *issues_model.PullRequest) (*QueueEntry, error) {
	entry, err := pull_model.GetMergeQueueEntryByPullID(ctx, pr.ID)
	if err != nil || entry == nil {
		return nil, err
	}
	entries, err := pull_model.GetMergeQueueEntries(ctx, entry.RepoID, entry.BaseBranch)
	if err != nil {
		return nil, err
	}
	position := 0
	for i, e := range entries {
		if e.ID == entry.ID {
			position = i + 1
		}
	}
	return &QueueEntry{MergeQueueEntry: entry, Position: position, PullRequest: pr}, nil
}
