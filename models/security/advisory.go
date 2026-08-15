// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"fmt"
	"slices"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
)

// State is the lifecycle state of a security advisory.
type State string

const (
	StateTriage    State = "triage"
	StateDraft     State = "draft"
	StatePublished State = "published"
	StateClosed    State = "closed"
	StateWithdrawn State = "withdrawn"
)

// validNextStates enumerates the allowed lifecycle transitions.
// See the state diagram in the milestone plan:
//
//	report -> triage -> draft -> published -> withdrawn
//	                 \-> closed
//	maintainer -> draft (directly, skipping triage)
var validNextStates = map[State][]State{
	StateTriage:    {StateDraft, StateClosed},
	StateDraft:     {StatePublished, StateClosed},
	StatePublished: {StateWithdrawn},
	StateClosed:    {},
	StateWithdrawn: {},
}

// CanTransitionTo reports whether moving from s to next is a valid lifecycle transition.
func (s State) CanTransitionTo(next State) bool {
	return slices.Contains(validNextStates[s], next)
}

// Severity is the qualitative severity of an advisory.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Advisory is a repository security advisory.
type Advisory struct {
	ID           int64  `xorm:"pk autoincr"`
	RepoID       int64  `xorm:"INDEX NOT NULL"`
	GTSAID       string `xorm:"'gtsa_id' UNIQUE NOT NULL"`
	CVEID        string `xorm:"'cve_id' INDEX"`
	State        State  `xorm:"INDEX NOT NULL"`
	Summary      string `xorm:"VARCHAR(1024) NOT NULL"`
	Description  string `xorm:"LONGTEXT"`
	Severity     Severity
	CVSSv3Vector string   `xorm:"'cvss_v3_vector'"`
	CVSSv3Score  float64  `xorm:"'cvss_v3_score'"`
	CVSSv4Vector string   `xorm:"'cvss_v4_vector'"`
	CVSSv4Score  float64  `xorm:"'cvss_v4_score'"`
	CWEIDs       []string `xorm:"'cwe_ids' JSON TEXT"`
	AuthorID     int64    `xorm:"INDEX NOT NULL"`
	PublisherID  int64

	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"INDEX updated"`

	PublishedUnix timeutil.TimeStamp
	ClosedUnix    timeutil.TimeStamp
	WithdrawnUnix timeutil.TimeStamp
}

// TableName sets the table name to security_advisory, since xorm's default
// naming would otherwise use the bare struct name "advisory".
func (Advisory) TableName() string {
	return "security_advisory"
}

func init() {
	db.RegisterModel(new(Advisory))
}

// ErrAdvisoryNotExist represents a "AdvisoryNotExist" kind of error.
type ErrAdvisoryNotExist struct {
	ID     int64
	GTSAID string
}

// IsErrAdvisoryNotExist checks if an error is a ErrAdvisoryNotExist.
func IsErrAdvisoryNotExist(err error) bool {
	_, ok := err.(ErrAdvisoryNotExist)
	return ok
}

func (err ErrAdvisoryNotExist) Error() string {
	if err.GTSAID != "" {
		return fmt.Sprintf("advisory does not exist [gtsa_id: %s]", err.GTSAID)
	}
	return fmt.Sprintf("advisory does not exist [id: %d]", err.ID)
}

func (err ErrAdvisoryNotExist) Unwrap() error {
	return util.ErrNotExist
}

// GetAdvisoryByID loads an advisory by its numeric ID, with no permission filtering.
// Callers in service/router layers must apply AccessibleAdvisoryCondition or an
// equivalent permission check themselves.
func GetAdvisoryByID(ctx context.Context, id int64) (*Advisory, error) {
	advisory := new(Advisory)
	has, err := db.GetEngine(ctx).ID(id).Get(advisory)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrAdvisoryNotExist{ID: id}
	}
	return advisory, nil
}

// GetAdvisoryByGTSAID loads an advisory by its GTSA ID, with no permission filtering.
func GetAdvisoryByGTSAID(ctx context.Context, gtsaID string) (*Advisory, error) {
	advisory := new(Advisory)
	has, err := db.GetEngine(ctx).Where("gtsa_id = ?", gtsaID).Get(advisory)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrAdvisoryNotExist{GTSAID: gtsaID}
	}
	return advisory, nil
}
