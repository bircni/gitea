// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"

	"gitea.dev/models/db"

	"xorm.io/builder"
)

// AdvisoryCollaborator grants a user write access to one advisory's draft
// content. This is the per-advisory ACL: repo write access alone does not
// grant it, mirroring GitHub's model where advisory access is an explicit
// per-object collaborator list.
type AdvisoryCollaborator struct {
	ID         int64 `xorm:"pk autoincr"`
	AdvisoryID int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	UserID     int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
}

// TableName sets the table name to security_advisory_collaborator.
func (AdvisoryCollaborator) TableName() string {
	return "security_advisory_collaborator"
}

func init() {
	db.RegisterModel(new(AdvisoryCollaborator))
}

// IsAdvisoryCollaborator reports whether userID is a collaborator on advisoryID.
func IsAdvisoryCollaborator(ctx context.Context, advisoryID, userID int64) (bool, error) {
	return db.Exist[AdvisoryCollaborator](ctx, builder.Eq{
		"advisory_id": advisoryID,
		"user_id":     userID,
	})
}
