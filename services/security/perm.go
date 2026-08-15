// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package security computes access control and service-layer operations for
// repository security advisories.
package security

import (
	"context"

	"gitea.dev/models/perm"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
)

// CanAdminAdvisory reports whether doer has advisory-admin rights on repo:
// repo owner, org owner, or a collaborator with admin-level repo access.
// This is repo-scoped and does not require an existing advisory, since it is
// also used to decide who may create an advisory or manage its collaborators.
//
// This deliberately does not go through repo permission's unit-based
// Permission.IsAdmin(): GetIndividualUserRepoPermission short-circuits admin
// checks in ways that don't map onto a per-advisory ACL (see the plan's
// "Access control" section), and an org admin team sets its unitsMode to nil
// entirely. access_model.IsUserRepoAdmin resolves admin rights directly
// against the access table and org admin teams, without ever consulting
// per-unit permissions, so it isn't subject to either issue - and per-unit
// consultation is exactly what would be wrong here, since advisories have no
// unit of their own (models/unit/unit.go:36 blocks adding one).
func CanAdminAdvisory(ctx context.Context, doer *user_model.User, repo *repo_model.Repository) (bool, error) {
	if doer == nil {
		return false, nil
	}
	return access_model.IsUserRepoAdmin(ctx, repo, doer)
}

// CanWriteAdvisory reports whether doer may edit advisory: AdvisoryAdmin, or
// an explicit security_advisory_collaborator row for this advisory.
func CanWriteAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory) (bool, error) {
	if doer == nil {
		return false, nil
	}

	repo, err := repo_model.GetRepositoryByID(ctx, advisory.RepoID)
	if err != nil {
		return false, err
	}
	if isAdmin, err := CanAdminAdvisory(ctx, doer, repo); err != nil || isAdmin {
		return isAdmin, err
	}

	return security_model.IsAdvisoryCollaborator(ctx, advisory.ID, doer.ID)
}

// CanReadAdvisory reports whether doer may view advisory: AdvisoryWrite, or
// doer authored it, or (for a published advisory only) doer can read the repo.
func CanReadAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory) (bool, error) {
	if advisory.State == security_model.StatePublished {
		repo, err := repo_model.GetRepositoryByID(ctx, advisory.RepoID)
		if err != nil {
			return false, err
		}
		mode, err := access_model.AccessLevel(ctx, doer, repo)
		if err != nil {
			return false, err
		}
		if mode >= perm.AccessModeRead {
			return true, nil
		}
	}

	if doer == nil {
		return false, nil
	}
	if advisory.AuthorID == doer.ID {
		return true, nil
	}

	return CanWriteAdvisory(ctx, doer, advisory)
}
