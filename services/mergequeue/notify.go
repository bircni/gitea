// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mergequeue

import (
	"context"

	"gitea.dev/models/db"
	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/log"
	"gitea.dev/modules/repository"
	notify_service "gitea.dev/services/notify"
	pull_service "gitea.dev/services/pull"
)

type mergeQueueNotifier struct {
	notify_service.NullNotifier
}

var _ notify_service.Notifier = &mergeQueueNotifier{}

// NewNotifier creates a new mergeQueueNotifier
func NewNotifier() notify_service.Notifier {
	return &mergeQueueNotifier{}
}

// CreateCommitStatus reacts to a status posted against a commit that belongs to an in-flight merge
// queue batch (i.e. the synthetic combined commit, not the PR's own head), and finalizes or fails
// the batch once its required contexts all report a result.
func (n *mergeQueueNotifier) CreateCommitStatus(ctx context.Context, repo *repo_model.Repository, _ *repository.PushCommit, _ *user_model.User, status *git_model.CommitStatus) {
	exists, batch, err := pull_model.GetMergeQueueBatchByTempCommitID(ctx, repo.ID, status.SHA)
	if err != nil {
		log.Error("mergequeue: GetMergeQueueBatchByTempCommitID: %v", err)
		return
	}
	if !exists {
		return
	}

	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, repo.ID, batch.BaseBranch)
	if err != nil {
		log.Error("mergequeue: GetFirstMatchProtectedBranchRule: %v", err)
		return
	}
	required, err := pull_service.EffectiveRequiredContexts(ctx, repo, pb)
	if err != nil {
		log.Error("mergequeue: EffectiveRequiredContexts: %v", err)
		return
	}

	statuses, err := git_model.GetLatestCommitStatus(ctx, repo.ID, status.SHA, db.ListOptionsAll)
	if err != nil {
		log.Error("mergequeue: GetLatestCommitStatus: %v", err)
		return
	}

	state := pull_service.MergeRequiredContextsCommitStatus(statuses, required)
	switch {
	case state.IsSuccess():
		scheduleUnderBranchLock(batch.RepoID, batch.BaseBranch, func(ctx context.Context) {
			finalizeBatch(ctx, batch)
		})
	case state.IsFailure() || state.IsError():
		scheduleUnderBranchLock(batch.RepoID, batch.BaseBranch, func(ctx context.Context) {
			failOrBisectBatch(ctx, batch, "required status checks failed on the merge queue's test commit")
		})
	default:
		// still pending, nothing to do yet
	}
}

// PullRequestSynchronized evicts a queued pull request as soon as its head branch is updated, since
// the queue entry pinned the old head commit id - matching GitHub's behavior of dropping a PR from
// the queue on new pushes rather than silently testing/merging a commit the author didn't queue.
func (n *mergeQueueNotifier) PullRequestSynchronized(ctx context.Context, _ *user_model.User, pr *issues_model.PullRequest, _, _ string) {
	if exists, _, err := pull_model.GetActiveMergeQueueEntryByPullID(ctx, pr.ID); err != nil {
		log.Error("mergequeue: GetActiveMergeQueueEntryByPullID: %v", err)
	} else if exists {
		removeFromMergeQueueSystem(ctx, pr, "pull request was updated while queued")
	}
}

// IssueChangeStatus evicts a queued pull request when its issue is closed.
func (n *mergeQueueNotifier) IssueChangeStatus(ctx context.Context, _ *user_model.User, _ string, issue *issues_model.Issue, _ *issues_model.Comment, closeOrReopen bool) {
	if !closeOrReopen || !issue.IsPull {
		return
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		log.Error("mergequeue: LoadPullRequest: %v", err)
		return
	}
	if exists, _, err := pull_model.GetActiveMergeQueueEntryByPullID(ctx, issue.PullRequest.ID); err != nil {
		log.Error("mergequeue: GetActiveMergeQueueEntryByPullID: %v", err)
	} else if exists {
		removeFromMergeQueueSystem(ctx, issue.PullRequest, "pull request was closed while queued")
	}
}

// PullRequestChangeTargetBranch evicts a queued pull request when it is retargeted: its queue entry
// belongs to the old base branch's queue and no longer applies.
func (n *mergeQueueNotifier) PullRequestChangeTargetBranch(ctx context.Context, _ *user_model.User, pr *issues_model.PullRequest, _ string) {
	if exists, _, err := pull_model.GetActiveMergeQueueEntryByPullID(ctx, pr.ID); err != nil {
		log.Error("mergequeue: GetActiveMergeQueueEntryByPullID: %v", err)
	} else if exists {
		removeFromMergeQueueSystem(ctx, pr, "pull request's target branch was changed while queued")
	}
}
