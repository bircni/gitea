// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import "gitea.dev/models/db"

// CreditType reuses OSV's ten credit roles.
type CreditType string

const (
	CreditTypeFinder               CreditType = "finder"
	CreditTypeReporter             CreditType = "reporter"
	CreditTypeAnalyst              CreditType = "analyst"
	CreditTypeCoordinator          CreditType = "coordinator"
	CreditTypeRemediationDeveloper CreditType = "remediation_developer"
	CreditTypeReviewer             CreditType = "reviewer"
	CreditTypeVerifier             CreditType = "verifier"
	CreditTypeTool                 CreditType = "tool"
	CreditTypeSponsor              CreditType = "sponsor"
	CreditTypeOther                CreditType = "other"
)

// CreditState mirrors GitHub's credit acceptance workflow.
type CreditState string

const (
	CreditStatePending  CreditState = "pending"
	CreditStateAccepted CreditState = "accepted"
	CreditStateDeclined CreditState = "declined"
)

// AdvisoryCredit attributes a person's contribution to an advisory.
//
// This follows the non-user attribution convention used by
// models/issues/comment.go (the (UserID, OriginalAuthor, OriginalAuthorID)
// triple): UserID > 0 identifies a local user, UserID == 0 with
// OriginalAuthor set identifies an outside reporter with no account.
// Callers must render credited local users via user.GetEmail(), never
// user.Email directly, so KeepEmailPrivate is honoured.
type AdvisoryCredit struct {
	ID         int64 `xorm:"pk autoincr"`
	AdvisoryID int64 `xorm:"INDEX NOT NULL"`

	UserID           int64 `xorm:"INDEX"`
	OriginalAuthor   string
	OriginalAuthorID int64

	Type  CreditType  `xorm:"NOT NULL"`
	State CreditState `xorm:"NOT NULL"`
}

// TableName sets the table name to security_advisory_credit.
func (AdvisoryCredit) TableName() string {
	return "security_advisory_credit"
}

func init() {
	db.RegisterModel(new(AdvisoryCredit))
}
