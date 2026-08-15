// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"

	"gitea.dev/models/db"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
)

// ListAdvisoriesByOwner returns the advisories doer may see across every
// repository owned by ownerID - the org (or user) triage inbox. Like
// ListAdvisories, filtering happens entirely in SQL via
// security_model.AccessibleAdvisoryCondition, so an advisory doer may not see
// never appears in the result.
func ListAdvisoriesByOwner(ctx context.Context, doer *user_model.User, ownerID int64) ([]*security_model.Advisory, error) {
	var advisories []*security_model.Advisory
	err := db.GetEngine(ctx).
		Table("security_advisory").
		Join("INNER", "`repository`", "`repository`.id = `security_advisory`.repo_id").
		Where("`repository`.owner_id = ?", ownerID).
		And(security_model.AccessibleAdvisoryCondition(doer)).
		Find(&advisories)
	return advisories, err
}

// ListPublishedAdvisories returns every published advisory doer may see,
// instance-wide, most recently published first. This never returns an
// unpublished advisory: AccessibleAdvisoryCondition still applies on top of
// the explicit published filter, so a site admin or advisory collaborator
// browsing this list does not get a preview of embargoed rows.
func ListPublishedAdvisories(ctx context.Context, doer *user_model.User, listOptions db.ListOptions) ([]*security_model.Advisory, int64, error) {
	sess := db.GetEngine(ctx).
		Table("security_advisory").
		Where("security_advisory.state = ?", security_model.StatePublished).
		And(security_model.AccessibleAdvisoryCondition(doer)).
		Desc("security_advisory.published_unix")

	db.SetSessionPagination(sess, &listOptions)

	var advisories []*security_model.Advisory
	count, err := sess.FindAndCount(&advisories)
	return advisories, count, err
}
