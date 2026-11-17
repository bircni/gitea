// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"errors"
	"net/http"

	pull_model "gitea.dev/models/pull"
	"gitea.dev/modules/templates"
	"gitea.dev/modules/util"
	"gitea.dev/services/context"
	"gitea.dev/services/mergequeue"
	pull_service "gitea.dev/services/pull"
)

const tplMergeQueue templates.TplName = "repo/pulls/merge_queue"

type pullMergeQueueData struct {
	BaseBranch string
	QueueLink  string
	Entry      *mergequeue.QueueEntry
	CanDequeue bool
}

func mergeQueueLink(repoLink, branch string) string {
	return repoLink + "/queue/" + util.PathEscapeSegments(branch)
}

// prepareMergeBoxMergeQueue shows the queue position of a queued pull request, which can't be merged in another way meanwhile
func (prInfo *pullRequestViewInfo) prepareMergeBoxMergeQueue(ctx *context.Context) {
	pull := prInfo.issue.PullRequest
	pb := prInfo.ProtectedBranchRule
	if pb == nil || !pb.RequireMergeQueue || pull.HasMerged || prInfo.issue.IsClosed {
		return
	}
	entry, err := mergequeue.GetEntry(ctx, pull)
	if err != nil {
		ctx.ServerErrorAuto(err)
		return
	}
	data := prInfo.MergeBoxData
	data.MergeQueue = &pullMergeQueueData{
		BaseBranch: pull.BaseBranch,
		QueueLink:  mergeQueueLink(ctx.Repo.RepoLink, pull.BaseBranch),
		Entry:      entry,
		CanDequeue: entry != nil && ctx.IsSigned && (data.hasPermToMerge || ctx.Doer.ID == entry.EnqueuerID),
	}
	if entry != nil {
		data.ReloadingInterval = util.IfZero(data.ReloadingInterval, 5000)
	}
}

// RemoveFromMergeQueue removes the pull request from the merge queue
func RemoveFromMergeQueue(ctx *context.Context) {
	issue, ok := getPullInfo(ctx)
	if !ok {
		return
	}
	if !ctx.IsSigned {
		ctx.NotFound()
		return
	}
	entry, err := pull_model.GetMergeQueueEntryByPullID(ctx, issue.PullRequest.ID)
	if err != nil {
		ctx.ServerErrorAuto(err)
		return
	} else if entry == nil {
		ctx.NotFound()
		return
	}
	if ctx.Doer.ID != entry.EnqueuerID {
		allowed, err := pull_service.IsUserAllowedToMerge(ctx, issue.PullRequest, ctx.Repo.Permission, ctx.Doer)
		if err != nil {
			ctx.ServerErrorAuto(err)
			return
		} else if !allowed {
			ctx.HTTPError(http.StatusForbidden, "user has no permission to remove the pull request from the merge queue")
			return
		}
	}
	if err := mergequeue.Dequeue(ctx, ctx.Doer, issue.PullRequest); err != nil && !errors.Is(err, mergequeue.ErrNotQueued) {
		ctx.ServerErrorAuto(err)
		return
	}
	ctx.Flash.Success(ctx.Tr("repo.pulls.merge_queue.removed"))
	ctx.JSONRedirect(issue.Link())
}

// MergeQueue shows the merge queue of a branch
func MergeQueue(ctx *context.Context) {
	branch := ctx.PathParam("*")
	queue, err := mergequeue.GetQueue(ctx, ctx.Repo.Repository.ID, branch)
	if err != nil {
		ctx.ServerErrorAuto(err)
		return
	}
	for _, e := range queue {
		e.PullRequest.Issue.Repo = ctx.Repo.Repository
	}
	required, _, err := mergequeue.IsRequired(ctx, ctx.Repo.Repository.ID, branch)
	if err != nil {
		ctx.ServerErrorAuto(err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("repo.pulls.merge_queue.title", branch)
	ctx.Data["PageIsPullList"] = true
	ctx.Data["BaseBranch"] = branch
	ctx.Data["QueueRequired"] = required
	ctx.Data["Queue"] = queue
	ctx.HTML(http.StatusOK, tplMergeQueue)
}
