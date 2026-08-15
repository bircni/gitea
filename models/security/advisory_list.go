// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"gitea.dev/models/perm"
	user_model "gitea.dev/models/user"

	"xorm.io/builder"
)

// AccessibleAdvisoryCondition returns a condition selecting security_advisory
// rows that doer may see: any published advisory, plus (for a signed-in
// doer) any advisory doer authored, is an explicit collaborator on, or has
// admin access to via repo ownership, direct admin-level repo access, or an
// admin/owner team.
//
// This must be used in every list, count, and search path over advisories:
// existence of an unpublished advisory must never leak. Filtering happens
// entirely in SQL, never in Go after fetching, so it composes into any
// query builder.Cond can be added to.
//
// This does not derive from models/perm/access.Permission: that computation
// short-circuits admin/owner checks in ways that don't map onto a
// per-advisory ACL. Keep this in sync with services/security/perm.go, which
// computes the same access levels for single-advisory checks in Go.
func AccessibleAdvisoryCondition(doer *user_model.User) builder.Cond {
	cond := builder.Eq{"security_advisory.state": StatePublished}
	if doer == nil {
		return cond
	}
	if doer.IsAdmin {
		// Site admins see every private repository already; advisories are no exception.
		return builder.Expr("1=1")
	}

	return builder.Or(
		cond,
		builder.Eq{"security_advisory.author_id": doer.ID},
		builder.In("security_advisory.id", builder.Select("advisory_id").
			From("security_advisory_collaborator").
			Where(builder.Eq{"user_id": doer.ID})),
		builder.In("security_advisory.repo_id", builder.Select("id").
			From("`repository`").
			Where(builder.Eq{"owner_id": doer.ID})),
		builder.In("security_advisory.repo_id", builder.Select("`access`.repo_id").
			From("`access`").
			Where(builder.And(
				builder.Eq{"`access`.user_id": doer.ID},
				builder.Gte{"`access`.mode": int(perm.AccessModeAdmin)},
			))),
		builder.In("security_advisory.repo_id", builder.Select("`team_repo`.repo_id").
			From("team_repo").
			Join("INNER", "team_user", "`team_user`.team_id = `team_repo`.team_id").
			Join("INNER", "team", "`team`.id = `team_repo`.team_id").
			Where(builder.And(
				builder.Eq{"`team_user`.uid": doer.ID},
				builder.Gte{"`team`.authorize": int(perm.AccessModeAdmin)},
			))),
	)
}
