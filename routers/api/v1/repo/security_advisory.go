// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"errors"
	"net/http"
	"strings"

	"gitea.dev/models/db"
	security_model "gitea.dev/models/security"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/util"
	"gitea.dev/modules/web"
	"gitea.dev/routers/api/v1/utils"
	"gitea.dev/services/context"
	"gitea.dev/services/convert"
	security_service "gitea.dev/services/security"
)

// handleAdvisoryServiceError maps the sentinel/typed errors returned by
// services/security and models/security onto API responses, without leaking
// whether a hidden advisory exists: not-found and permission-denied both
// collapse to 404 unless the caller has already established (by calling
// this only after a successful read-permission check) that some visibility
// already exists, in which case use handleAdvisoryWriteError instead.
func handleAdvisoryServiceError(ctx *context.APIContext, err error) {
	var checklistErr security_service.PublishChecklistError
	switch {
	case errors.Is(err, util.ErrNotExist):
		ctx.APIErrorNotFound()
	case errors.As(err, &checklistErr):
		ctx.APIError(http.StatusUnprocessableEntity, strings.Join(checklistErr.Reasons, "; "))
	case errors.Is(err, util.ErrInvalidArgument):
		ctx.APIError(http.StatusUnprocessableEntity, err.Error())
	default:
		var permErr security_service.ErrPermissionDenied
		if errors.As(err, &permErr) {
			ctx.APIErrorNotFound()
			return
		}
		ctx.APIErrorInternal(err)
	}
}

// getAdvisoryForRepo loads the advisory identified by the "gtsa_id" path
// param, verifying it belongs to ctx.Repo.Repository, and returns nil (with
// the response already written) if it doesn't exist, doesn't belong to this
// repo, or doer may not read it. GTSA IDs are globally unique but scoped in
// the URL to a specific repo, so a mismatch must 404 exactly like a
// nonexistent ID: it must not confirm the ID exists under a different repo.
func getAdvisoryForRepo(ctx *context.APIContext) *security_model.Advisory {
	gtsaID := ctx.PathParam("gtsa_id")
	advisory, err := security_model.GetAdvisoryByGTSAID(ctx, gtsaID)
	if err != nil {
		if security_model.IsErrAdvisoryNotExist(err) {
			ctx.APIErrorNotFound()
		} else {
			ctx.APIErrorInternal(err)
		}
		return nil
	}
	if advisory.RepoID != ctx.Repo.Repository.ID {
		ctx.APIErrorNotFound()
		return nil
	}

	canRead, err := security_service.CanReadAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil
	}
	if !canRead {
		ctx.APIErrorNotFound()
		return nil
	}

	return advisory
}

// ListSecurityAdvisories lists a repository's security advisories that the
// requester may see.
func ListSecurityAdvisories(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/security-advisories repository repoListSecurityAdvisories
	// ---
	// summary: List a repository's security advisories
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

	listOpts := utils.GetListOptions(ctx)
	skip, take := listOpts.GetSkipTake()

	sess := db.GetEngine(ctx).
		Where("security_advisory.repo_id = ?", ctx.Repo.Repository.ID).
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

// CreateSecurityAdvisory creates a maintainer-authored draft advisory.
func CreateSecurityAdvisory(ctx *context.APIContext) {
	// swagger:operation POST /repos/{owner}/{repo}/security-advisories repository repoCreateSecurityAdvisory
	// ---
	// summary: Create a security advisory
	// consumes:
	// - application/json
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
	// - name: body
	//   in: body
	//   schema:
	//     "$ref": "#/definitions/CreateSecurityAdvisoryOption"
	// responses:
	//   "201":
	//     "$ref": "#/responses/SecurityAdvisory"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	form := web.GetForm[*api.CreateSecurityAdvisoryOption](ctx)

	isAdmin, err := security_service.CanAdminAdvisory(ctx, ctx.Doer, ctx.Repo.Repository)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !isAdmin {
		ctx.APIErrorNotFound()
		return
	}

	if len(form.Vulnerabilities) == 0 {
		ctx.APIError(http.StatusUnprocessableEntity, "at least one vulnerability is required")
		return
	}

	advisory := &security_model.Advisory{
		Summary:      form.Summary,
		Description:  form.Description,
		Severity:     security_model.Severity(form.Severity),
		CVEID:        form.CVEID,
		CVSSv3Vector: form.CVSSv3Vector,
		CVSSv4Vector: form.CVSSv4Vector,
		CWEIDs:       form.CWEIDs,
	}
	if err := security_service.CreateAdvisory(ctx, ctx.Doer, ctx.Repo.Repository, advisory); err != nil {
		handleAdvisoryServiceError(ctx, err)
		return
	}

	for _, v := range form.Vulnerabilities {
		vuln := &security_model.AdvisoryVulnerability{
			VulnerableVersionRange: v.VulnerableVersionRange,
			PatchedVersions:        v.PatchedVersions,
			VulnerableFunctions:    v.VulnerableFunctions,
		}
		if v.Package != nil {
			vuln.Ecosystem = v.Package.Ecosystem
			vuln.PackageName = v.Package.Name
		}
		if err := security_service.AddVulnerability(ctx, ctx.Doer, advisory, vuln); err != nil {
			handleAdvisoryServiceError(ctx, err)
			return
		}
	}

	result, err := convert.ToSecurityAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusCreated, result)
}

// GetSecurityAdvisory returns a single security advisory.
func GetSecurityAdvisory(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/security-advisories/{gtsa_id} repository repoGetSecurityAdvisory
	// ---
	// summary: Get a security advisory
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
	// - name: gtsa_id
	//   in: path
	//   description: id of the advisory
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/SecurityAdvisory"
	//   "404":
	//     "$ref": "#/responses/notFound"

	advisory := getAdvisoryForRepo(ctx)
	if advisory == nil {
		return
	}

	result, err := convert.ToSecurityAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, result)
}

// EditSecurityAdvisory updates a security advisory's metadata, and/or
// transitions its lifecycle state.
func EditSecurityAdvisory(ctx *context.APIContext) {
	// swagger:operation PATCH /repos/{owner}/{repo}/security-advisories/{gtsa_id} repository repoEditSecurityAdvisory
	// ---
	// summary: Update a security advisory
	// consumes:
	// - application/json
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
	// - name: gtsa_id
	//   in: path
	//   description: id of the advisory
	//   type: string
	//   required: true
	// - name: body
	//   in: body
	//   schema:
	//     "$ref": "#/definitions/EditSecurityAdvisoryOption"
	// responses:
	//   "200":
	//     "$ref": "#/responses/SecurityAdvisory"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	advisory := getAdvisoryForRepo(ctx)
	if advisory == nil {
		return
	}

	canWrite, err := security_service.CanWriteAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !canWrite {
		// The requester can already read this advisory (getAdvisoryForRepo
		// checked that), so a plain 403 here does not leak anything new.
		ctx.APIError(http.StatusForbidden, "you do not have write access to this advisory")
		return
	}

	form := web.GetForm[*api.EditSecurityAdvisoryOption](ctx)

	if form.State != nil && security_model.State(*form.State) != advisory.State {
		next := security_model.State(*form.State)
		if next == security_model.StatePublished {
			if _, err := security_service.PublishAdvisory(ctx, ctx.Doer, advisory); err != nil {
				handleAdvisoryServiceError(ctx, err)
				return
			}
		} else if err := security_service.TransitionAdvisory(ctx, ctx.Doer, advisory, next); err != nil {
			handleAdvisoryServiceError(ctx, err)
			return
		}
	}

	if form.Summary != nil || form.Description != nil || form.Severity != nil ||
		form.CVEID != nil || form.CVSSv3Vector != nil || form.CVSSv4Vector != nil || form.CWEIDs != nil {
		update := &security_service.AdvisoryUpdate{
			Summary:      advisory.Summary,
			Description:  advisory.Description,
			Severity:     advisory.Severity,
			CVSSv3Vector: advisory.CVSSv3Vector,
			CVSSv4Vector: advisory.CVSSv4Vector,
			CWEIDs:       advisory.CWEIDs,
			CVEID:        advisory.CVEID,
		}
		if form.Summary != nil {
			update.Summary = *form.Summary
		}
		if form.Description != nil {
			update.Description = *form.Description
		}
		if form.Severity != nil {
			update.Severity = security_model.Severity(*form.Severity)
		}
		if form.CVEID != nil {
			update.CVEID = *form.CVEID
		}
		if form.CVSSv3Vector != nil {
			update.CVSSv3Vector = *form.CVSSv3Vector
		}
		if form.CVSSv4Vector != nil {
			update.CVSSv4Vector = *form.CVSSv4Vector
		}
		if form.CWEIDs != nil {
			update.CWEIDs = *form.CWEIDs
		}

		if err := security_service.UpdateAdvisory(ctx, ctx.Doer, advisory, update); err != nil {
			handleAdvisoryServiceError(ctx, err)
			return
		}
	}

	result, err := convert.ToSecurityAdvisory(ctx, ctx.Doer, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, result)
}

// GetSecurityAdvisoryOSV returns a single published advisory in OSV format.
// Only published advisories are ever served here - draft, triage, and closed
// content must never reach this endpoint, so this is checked explicitly
// before any permission check, rather than relying on CanReadAdvisory's
// published-is-public behaviour to do it implicitly.
func GetSecurityAdvisoryOSV(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/security-advisories/{gtsa_id}.osv.json repository repoGetSecurityAdvisoryOSV
	// ---
	// summary: Get a published security advisory in OSV format
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
	// - name: gtsa_id
	//   in: path
	//   description: id of the advisory
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     description: OSV-schema advisory document
	//   "404":
	//     "$ref": "#/responses/notFound"

	gtsaID := ctx.PathParam("gtsa_id")
	advisory, err := security_model.GetAdvisoryByGTSAID(ctx, gtsaID)
	if err != nil {
		if security_model.IsErrAdvisoryNotExist(err) {
			ctx.APIErrorNotFound()
		} else {
			ctx.APIErrorInternal(err)
		}
		return
	}
	if advisory.RepoID != ctx.Repo.Repository.ID || advisory.State != security_model.StatePublished {
		ctx.APIErrorNotFound()
		return
	}

	osv, err := convert.ToOSV(ctx, advisory)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, osv)
}

// ListInstanceSecurityAdvisoriesOSV returns every published advisory across
// the instance in the OSV bulk-export document shape - a Gitea addition
// GitHub does not offer (only its curated global database is exported
// there). This is the payoff for validating version ranges on save.
func ListInstanceSecurityAdvisoriesOSV(ctx *context.APIContext) {
	// swagger:operation GET /-/advisories.osv.json miscellaneous listInstanceSecurityAdvisoriesOSV
	// ---
	// summary: List every published security advisory on the instance in OSV format
	// produces:
	// - application/json
	// responses:
	//   "200":
	//     description: OSV-schema bulk export document

	var advisories []*security_model.Advisory
	err := db.GetEngine(ctx).
		Where(security_model.AccessibleAdvisoryCondition(ctx.Doer)).
		And("security_advisory.state = ?", string(security_model.StatePublished)).
		Find(&advisories)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	list, err := convert.ToOSVList(ctx, advisories)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, list)
}
