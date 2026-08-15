// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"errors"
	"net/http"

	"gitea.dev/models/db"
	"gitea.dev/models/renderhelper"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/markup/markdown"
	"gitea.dev/modules/util"
	"gitea.dev/services/context"
	security_service "gitea.dev/services/security"
)

// loadAdvisory loads the advisory named by the {gtsa_id} path param,
// confirms it belongs to ctx.Repo.Repository, and checks CanReadAdvisory.
// Every failure path - not found, wrong repo, or read denied - renders the
// identical ctx.NotFound(nil), so an unpublished advisory's existence is
// never leaked to a caller who lacks access to it (services/context/permission.go's
// idiom, and the same leak-prevention property AccessibleAdvisoryCondition
// gives every list query).
func loadAdvisory(ctx *context.Context) (*security_model.Advisory, bool) {
	advisory, err := security_model.GetAdvisoryByGTSAID(ctx, ctx.PathParam("gtsa_id"))
	if err != nil {
		if !security_model.IsErrAdvisoryNotExist(err) {
			ctx.ServerError("GetAdvisoryByGTSAID", err)
		} else {
			ctx.NotFound(nil)
		}
		return nil, false
	}
	if advisory.RepoID != ctx.Repo.Repository.ID {
		ctx.NotFound(nil)
		return nil, false
	}

	canRead, err := security_service.CanReadAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.ServerError("CanReadAdvisory", err)
		return nil, false
	}
	if !canRead {
		ctx.NotFound(nil)
		return nil, false
	}

	return advisory, true
}

// ViewAdvisory renders an advisory's detail page: summary, description,
// severity/CVSS, affected packages, and collaborators. Discussion/comments
// are left for a follow-up - AdvisoryComment already exists in
// models/security, but the service-layer posting/permission path for it is
// out of scope for this pass.
func ViewAdvisory(ctx *context.Context) {
	advisory, ok := loadAdvisory(ctx)
	if !ok {
		return
	}

	var vulns []*security_model.AdvisoryVulnerability
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).Find(&vulns); err != nil {
		ctx.ServerError("FindAdvisoryVulnerabilities", err)
		return
	}

	var collaboratorRows []*security_model.AdvisoryCollaborator
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).Find(&collaboratorRows); err != nil {
		ctx.ServerError("FindAdvisoryCollaborators", err)
		return
	}
	collaboratorIDs := make([]int64, len(collaboratorRows))
	for i, c := range collaboratorRows {
		collaboratorIDs[i] = c.UserID
	}
	collaborators, err := user_model.GetUsersByIDs(ctx, collaboratorIDs)
	if err != nil {
		ctx.ServerError("GetUsersByIDs", err)
		return
	}

	var credits []*security_model.AdvisoryCredit
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).Find(&credits); err != nil {
		ctx.ServerError("FindAdvisoryCredits", err)
		return
	}

	canWrite, err := security_service.CanWriteAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.ServerError("CanWriteAdvisory", err)
		return
	}

	rctx := renderhelper.NewRenderContextRepoComment(ctx, ctx.Repo.Repository)
	renderedDescription, err := markdown.RenderString(rctx, advisory.Description)
	if err != nil {
		ctx.ServerError("RenderString", err)
		return
	}

	ctx.Data["Title"] = advisory.Summary
	ctx.Data["PageIsSecurityAdvisories"] = true
	ctx.Data["Advisory"] = advisory
	ctx.Data["RenderedDescription"] = renderedDescription
	ctx.Data["Vulnerabilities"] = vulns
	ctx.Data["Collaborators"] = collaborators
	ctx.Data["Credits"] = credits
	ctx.Data["CanWriteAdvisory"] = canWrite
	ctx.Data["CanTransitionToPublished"] = advisory.State.CanTransitionTo(security_model.StatePublished)
	ctx.Data["CanTransitionToClosed"] = advisory.State.CanTransitionTo(security_model.StateClosed)
	ctx.Data["CanTransitionToWithdrawn"] = advisory.State.CanTransitionTo(security_model.StateWithdrawn)

	ctx.HTML(http.StatusOK, tplSecurityView)
}

// PublishAdvisoryPost runs the publish-time checklist and, if it passes,
// publishes the advisory. This (like TransitionAdvisoryPost below) is
// triggered by a ".link-action" anchor (templates/repo/security/view.tmpl),
// Gitea's generic fetch-action frontend (web_src/js/features/common-fetch-action.ts)
// - already wired up globally, so no new TS file is needed here - which
// expects a JSON {"redirect": ...} response rather than a plain HTTP
// redirect, hence ctx.JSONRedirect throughout instead of ctx.Redirect.
func PublishAdvisoryPost(ctx *context.Context) {
	advisory, ok := loadAdvisory(ctx)
	if !ok {
		return
	}

	warnings, err := security_service.PublishAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		handlePublishError(ctx, advisory, err)
		return
	}
	for _, w := range warnings {
		ctx.Flash.Warning(w)
	}

	ctx.JSONRedirect(ctx.Repo.RepoLink + "/security/advisories/" + advisory.GTSAID)
}

func handlePublishError(ctx *context.Context, advisory *security_model.Advisory, err error) {
	var checklistErr security_service.PublishChecklistError
	if errors.As(err, &checklistErr) {
		for _, reason := range checklistErr.Reasons {
			ctx.Flash.Error(reason)
		}
		ctx.JSONRedirect(ctx.Repo.RepoLink + "/security/advisories/" + advisory.GTSAID)
		return
	}

	var permErr security_service.ErrPermissionDenied
	if errors.As(err, &permErr) {
		ctx.NotFound(nil)
		return
	}

	ctx.ServerError("PublishAdvisory", err)
}

// TransitionAdvisoryPost moves an advisory to "closed" or "withdrawn",
// mirroring routers/web/repo/projects.go's ChangeProjectStatus action-param
// shape. Publishing has its own PublishAdvisoryPost above since it carries a
// checklist the other transitions do not.
func TransitionAdvisoryPost(ctx *context.Context) {
	advisory, ok := loadAdvisory(ctx)
	if !ok {
		return
	}

	var next security_model.State
	switch ctx.PathParam("action") {
	case "close":
		next = security_model.StateClosed
	case "withdraw":
		next = security_model.StateWithdrawn
	default:
		ctx.NotFound(nil)
		return
	}

	if err := security_service.TransitionAdvisory(ctx, ctx.Doer, advisory, next); err != nil {
		var permErr security_service.ErrPermissionDenied
		if errors.As(err, &permErr) {
			ctx.NotFound(nil)
			return
		}
		if errors.Is(err, util.ErrInvalidArgument) {
			ctx.Flash.Error(err.Error())
			ctx.JSONRedirect(ctx.Repo.RepoLink + "/security/advisories/" + advisory.GTSAID)
			return
		}
		ctx.ServerError("TransitionAdvisory", err)
		return
	}

	ctx.JSONRedirect(ctx.Repo.RepoLink + "/security/advisories/" + advisory.GTSAID)
}
