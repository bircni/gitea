// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package security renders the web UI for repository security advisories:
// the per-repo overview/list, the maintainer "new advisory" draft form, the
// advisory detail page, plus the org triage inbox and instance-wide
// published list. The structured reporter intake form is a later milestone
// step (services/security/report.go) and has no route here yet.
package security

import (
	"errors"
	"net/http"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	"gitea.dev/modules/templates"
	"gitea.dev/modules/util"
	"gitea.dev/modules/web"
	"gitea.dev/services/context"
	"gitea.dev/services/forms"
	security_service "gitea.dev/services/security"
)

const (
	tplSecurityList       templates.TplName = "repo/security/list"
	tplSecurityNew        templates.TplName = "repo/security/new"
	tplSecurityView       templates.TplName = "repo/security/view"
	tplSecurityOrgInbox   templates.TplName = "repo/security/org_inbox"
	tplSecurityGlobalList templates.TplName = "repo/security/global_list"
)

// MustEnableSecurityAdvisories guards every route under
// {owner}/{repo}/security behind the repo's advisory feature-surface
// visibility, computed once per request in services/context.RepoAssignment
// and stashed in ctx.Data["ShowSecurityAdvisories"]. There is no unit.Type
// for advisories (see the milestone plan's locked decisions), so this
// mirrors routers/web/repo/projects.go's MustEnableRepoProjects shape
// without a RequireUnitReader check. Individual advisory pages still apply
// their own per-advisory ACL (services/security.CanReadAdvisory /
// CanWriteAdvisory) on top of this coarse check.
func MustEnableSecurityAdvisories(ctx *context.Context) {
	show, _ := ctx.Data["ShowSecurityAdvisories"].(bool)
	if !show {
		ctx.NotFound(nil)
	}
}

// List renders the repo's advisory overview + list page.
func List(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.security_advisories")
	ctx.Data["PageIsSecurityAdvisories"] = true

	advisories, err := security_service.ListAdvisories(ctx, ctx.Doer, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("ListAdvisories", err)
		return
	}
	ctx.Data["Advisories"] = advisories

	canAdmin, err := security_service.CanAdminAdvisory(ctx, ctx.Doer, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("CanAdminAdvisory", err)
		return
	}
	ctx.Data["CanAdminAdvisory"] = canAdmin

	ctx.HTML(http.StatusOK, tplSecurityList)
}

// NewAdvisory renders the maintainer-authored "draft a new advisory" form.
// AdvisoryAdmin only - a 404, not a 403, so a repo writer who is not an
// advisory admin gets the same response whether or not advisories exist.
func NewAdvisory(ctx *context.Context) {
	canAdmin, err := security_service.CanAdminAdvisory(ctx, ctx.Doer, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("CanAdminAdvisory", err)
		return
	}
	if !canAdmin {
		ctx.NotFound(nil)
		return
	}

	ctx.Data["Title"] = ctx.Tr("repo.security_advisories.new")
	ctx.Data["PageIsSecurityAdvisories"] = true
	ctx.Data["Severities"] = []security_model.Severity{
		security_model.SeverityLow,
		security_model.SeverityMedium,
		security_model.SeverityHigh,
		security_model.SeverityCritical,
	}
	ctx.HTML(http.StatusOK, tplSecurityNew)
}

// NewAdvisoryPost creates a maintainer-authored draft advisory via
// services/security.CreateAdvisory, which itself enforces AdvisoryAdmin.
func NewAdvisoryPost(ctx *context.Context) {
	form := web.GetForm[*forms.NewAdvisoryForm](ctx)
	if ctx.HasError() {
		NewAdvisory(ctx)
		return
	}

	advisory := &security_model.Advisory{
		Summary:      form.Summary,
		Description:  form.Description,
		Severity:     security_model.Severity(form.Severity),
		CVSSv3Vector: form.CVSSv3Vector,
	}

	if err := security_service.CreateAdvisory(ctx, ctx.Doer, ctx.Repo.Repository, advisory); err != nil {
		handleAdvisoryFormError(ctx, err, NewAdvisory)
		return
	}

	ctx.Redirect(ctx.Repo.RepoLink + "/security/advisories/" + advisory.GTSAID)
}

// handleAdvisoryFormError renders the invalid-argument case as a flashed
// form error and re-renders render, and treats a permission error the same
// as MustEnableSecurityAdvisories does: a 404, never a 403, so an advisory's
// existence (or the doer's lack of access to act on it) is never leaked.
func handleAdvisoryFormError(ctx *context.Context, err error, render func(*context.Context)) {
	var permErr security_service.ErrPermissionDenied
	if errors.As(err, &permErr) {
		ctx.NotFound(nil)
		return
	}
	if errors.Is(err, util.ErrInvalidArgument) {
		ctx.Flash.Error(err.Error())
		render(ctx)
		return
	}
	ctx.ServerError("advisory operation", err)
}

// OrgAdvisories renders the org (or user) triage inbox: every advisory the
// doer may see across every repository owned by ctx.ContextUser, fixing the
// "70 different pages" cross-repo triage complaint the milestone plan cites.
func OrgAdvisories(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.security_advisories.org_inbox")

	advisories, err := security_service.ListAdvisoriesByOwner(ctx, ctx.Doer, ctx.ContextUser.ID)
	if err != nil {
		ctx.ServerError("ListAdvisoriesByOwner", err)
		return
	}

	repos, err := loadAdvisoryRepos(ctx, advisories)
	if err != nil {
		ctx.ServerError("loadAdvisoryRepos", err)
		return
	}
	ctx.Data["Advisories"] = advisories
	ctx.Data["AdvisoryRepos"] = repos

	ctx.HTML(http.StatusOK, tplSecurityOrgInbox)
}

// GlobalAdvisories renders the instance-wide list of every published
// advisory the doer may see, across every repository.
func GlobalAdvisories(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.security_advisories.global_list")

	const pageSize = 20
	page := max(ctx.FormInt("page"), 1)
	advisories, count, err := security_service.ListPublishedAdvisories(ctx, ctx.Doer, db.ListOptions{Page: page, PageSize: pageSize})
	if err != nil {
		ctx.ServerError("ListPublishedAdvisories", err)
		return
	}

	repos, err := loadAdvisoryRepos(ctx, advisories)
	if err != nil {
		ctx.ServerError("loadAdvisoryRepos", err)
		return
	}
	ctx.Data["Advisories"] = advisories
	ctx.Data["AdvisoryRepos"] = repos

	pager := context.NewPagination(count, pageSize, page, 5)
	pager.AddParamFromRequest(ctx.Req)
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplSecurityGlobalList)
}

// loadAdvisoryRepos loads the distinct repositories referenced by advisories,
// keyed by RepoID, so list templates can print "owner/repo" next to each row
// without a query per row.
func loadAdvisoryRepos(ctx *context.Context, advisories []*security_model.Advisory) (map[int64]*repo_model.Repository, error) {
	repoIDs := make([]int64, 0, len(advisories))
	seen := make(map[int64]bool, len(advisories))
	for _, a := range advisories {
		if !seen[a.RepoID] {
			seen[a.RepoID] = true
			repoIDs = append(repoIDs, a.RepoID)
		}
	}
	if len(repoIDs) == 0 {
		return map[int64]*repo_model.Repository{}, nil
	}

	repos, err := repo_model.GetRepositoriesMapByIDs(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	return repos, nil
}
