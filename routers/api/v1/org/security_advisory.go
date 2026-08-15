// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"net/http"

	"gitea.dev/models/db"
	security_model "gitea.dev/models/security"
	"gitea.dev/routers/api/v1/utils"
	"gitea.dev/services/context"
	"gitea.dev/services/convert"
)

// ListSecurityAdvisories lists the security advisories the requester may see
// across every repository owned by the organization - the org-wide triage
// inbox that the plan's research appendix cites as the fix for maintainers
// having to visit "70 different pages" to triage reports one repo at a time.
// Access is still filtered per-advisory via AccessibleAdvisoryCondition, so
// this never leaks an advisory the requester could not also reach through
// its own repository's list endpoint.
func ListSecurityAdvisories(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/security-advisories organization orgListSecurityAdvisories
	// ---
	// summary: List an organization's security advisories
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
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
	//     "$ref": "#/responses/SecurityAdvisoryList"
	//   "404":
	//     "$ref": "#/responses/notFound"

	listOpts := utils.GetListOptions(ctx)
	skip, take := listOpts.GetSkipTake()

	sess := db.GetEngine(ctx).
		Join("INNER", "`repository`", "`repository`.id = security_advisory.repo_id").
		Where("`repository`.owner_id = ?", ctx.Org.Organization.ID).
		And(security_model.AccessibleAdvisoryCondition(ctx.Doer))

	var advisories []*security_model.Advisory
	total, err := sess.Limit(take, skip).FindAndCount(&advisories)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	result, err := convert.ToSecurityAdvisoryList(ctx, ctx.Doer, advisories)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, result)
}
