// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"errors"
	"net/http"

	issues_model "gitea.dev/models/issues"
	pull_model "gitea.dev/models/pull"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/web"
	"gitea.dev/routers/api/v1/utils"
	"gitea.dev/services/context"
	"gitea.dev/services/convert"
	"gitea.dev/services/mergequeue"
	pull_service "gitea.dev/services/pull"
)

// AddPullRequestToMergeQueue adds a pull request to the merge queue of its base branch
func AddPullRequestToMergeQueue(ctx *context.APIContext) {
	// swagger:operation POST /repos/{owner}/{repo}/pulls/{index}/merge-queue repository repoAddPullRequestToMergeQueue
	// ---
	// summary: Add a pull request to the merge queue of its base branch
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: index
	//   in: path
	//   description: index of the pull request
	//   type: integer
	//   format: int64
	//   required: true
	// - name: body
	//   in: body
	//   schema:
	//     $ref: "#/definitions/AddToMergeQueueOption"
	// responses:
	//   "201":
	//     "$ref": "#/responses/MergeQueueEntry"
	//   default:
	//     "$ref": "#/responses/error"
	form := web.GetForm[*api.AddToMergeQueueOption](ctx)
	pr := getPullRequestForMergeQueue(ctx)
	if ctx.Written() {
		return
	}
	if form.Jump && !ctx.Repo.Permission.IsAdmin() {
		ctx.APIError(http.StatusForbidden, "only repository admins can jump the merge queue")
		return
	}
	if err := pull_service.CheckPullMergeable(ctx, ctx.Doer, &ctx.Repo.Permission, pr, pull_service.MergeCheckTypeGeneral, "", false); err != nil {
		if errors.Is(err, pull_service.ErrNoPermissionToMerge) {
			ctx.APIError(http.StatusForbidden, err.Error())
		} else if errors.Is(err, pull_service.ErrIsClosed) || errors.Is(err, pull_service.ErrHasMerged) ||
			errors.Is(err, pull_service.ErrIsWorkInProgress) || errors.Is(err, pull_service.ErrNotMergeableState) ||
			errors.Is(err, pull_service.ErrNotReadyToMerge) || errors.Is(err, pull_service.ErrHeadCommitsNotAllVerified) {
			ctx.APIError(http.StatusMethodNotAllowed, err.Error())
		} else {
			ctx.APIErrorInternal(err)
		}
		return
	}
	entry, err := enqueuePullRequest(ctx, pr, form.Jump)
	if err != nil {
		return
	}
	queued, err := mergequeue.GetEntry(ctx, pr)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusCreated, convert.ToMergeQueueEntry(ctx, entry, queued.Position, pr.Index, ctx.Doer))
}

// enqueuePullRequest writes the error response itself
func enqueuePullRequest(ctx *context.APIContext, pr *issues_model.PullRequest, jump bool) (*pull_model.MergeQueueEntry, error) {
	entry, err := mergequeue.Enqueue(ctx, ctx.Doer, pr, jump)
	switch {
	case errors.Is(err, mergequeue.ErrNotRequired):
		ctx.APIError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, pull_model.ErrAlreadyInMergeQueue):
		ctx.APIError(http.StatusConflict, err.Error())
	case err != nil:
		ctx.APIErrorInternal(err)
	}
	return entry, err
}

// RemovePullRequestFromMergeQueue removes a pull request from the merge queue
func RemovePullRequestFromMergeQueue(ctx *context.APIContext) {
	// swagger:operation DELETE /repos/{owner}/{repo}/pulls/{index}/merge-queue repository repoRemovePullRequestFromMergeQueue
	// ---
	// summary: Remove a pull request from the merge queue
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: index
	//   in: path
	//   description: index of the pull request
	//   type: integer
	//   format: int64
	//   required: true
	// responses:
	//   "204":
	//     "$ref": "#/responses/empty"
	//   default:
	//     "$ref": "#/responses/error"
	pr := getPullRequestForMergeQueue(ctx)
	if ctx.Written() {
		return
	}
	if allowed, err := pull_service.IsUserAllowedToMerge(ctx, pr, ctx.Repo.Permission, ctx.Doer); err != nil {
		ctx.APIErrorInternal(err)
		return
	} else if !allowed {
		ctx.APIError(http.StatusForbidden, "user is not allowed to merge this pull request")
		return
	}
	if err := mergequeue.Dequeue(ctx, ctx.Doer, pr); err != nil {
		ctx.APIErrorAuto(err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

func getPullRequestForMergeQueue(ctx *context.APIContext) *issues_model.PullRequest {
	pr, err := issues_model.GetPullRequestByIndex(ctx, ctx.Repo.Repository.ID, ctx.PathParamInt64("index"))
	if err != nil {
		ctx.APIErrorAuto(err)
		return nil
	}
	if err := pr.LoadIssue(ctx); err != nil {
		ctx.APIErrorInternal(err)
		return nil
	}
	pr.Issue.Repo = ctx.Repo.Repository
	return pr
}

// ListMergeQueue lists the merge queue of a branch
func ListMergeQueue(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/merge-queue/{branch} repository repoListMergeQueue
	// ---
	// summary: List the merge queue of a branch in merge order
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: branch
	//   in: path
	//   description: base branch of the queue
	//   type: string
	//   required: true
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// responses:
	//   "200":
	//     "$ref": "#/responses/MergeQueueEntryList"
	//   default:
	//     "$ref": "#/responses/error"
	queue, err := mergequeue.GetQueue(ctx, ctx.Repo.Repository.ID, ctx.PathParam("*"))
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	listOptions := utils.GetListOptions(ctx)
	skip, take := listOptions.GetSkipTake()
	page := queue[min(skip, len(queue)):min(skip+take, len(queue))]

	result := make([]*api.MergeQueueEntry, 0, len(page))
	for _, e := range page {
		result = append(result, convert.ToMergeQueueEntry(ctx, e.MergeQueueEntry, e.Position, e.PullRequest.Index, ctx.Doer))
	}
	ctx.SetLinkHeader(int64(len(queue)), listOptions.PageSize)
	ctx.SetTotalCountHeader(int64(len(queue)))
	ctx.JSON(http.StatusOK, result)
}
