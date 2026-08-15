// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
)

// AdvisoryComment is a discussion comment on an advisory.
//
// This is a dedicated table rather than a reuse of models/issues.Comment:
// that table's IssueID is non-nullable, and isolating embargoed advisory
// discussion from the issue-comment list (and every query over it) is the
// safer design for content that must never leak pre-publication.
type AdvisoryComment struct {
	ID         int64 `xorm:"pk autoincr"`
	AdvisoryID int64 `xorm:"INDEX NOT NULL"`
	PosterID   int64 `xorm:"INDEX NOT NULL"`

	Content string `xorm:"LONGTEXT"`

	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"INDEX updated"`
}

// TableName sets the table name to security_advisory_comment.
func (AdvisoryComment) TableName() string {
	return "security_advisory_comment"
}

func init() {
	db.RegisterModel(new(AdvisoryComment))
}
