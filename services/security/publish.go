// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"fmt"

	"gitea.dev/models/db"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/security/cvss"
	"gitea.dev/modules/security/versionrange"
	"gitea.dev/modules/timeutil"
)

// PublishChecklistError is a blocking failure of the publish-time checklist.
// Unlike ErrPermissionDenied or a plain validation error, callers are
// expected to collect and display every blocking Reason at once, since the
// checklist is meant to be fixed in one pass rather than discovered one
// field at a time.
type PublishChecklistError struct {
	Reasons []string
}

func (err PublishChecklistError) Error() string {
	return fmt.Sprintf("advisory is not ready to publish: %v", err.Reasons)
}

// checkPublishReady runs the publish-time checklist from the milestone plan
// and returns (blocking errors, warnings). It does not mutate advisory.
func checkPublishReady(ctx context.Context, advisory *security_model.Advisory) ([]string, []string, error) {
	var blocking, warnings []string

	if advisory.Summary == "" {
		blocking = append(blocking, "summary is required")
	}

	var vulns []*security_model.AdvisoryVulnerability
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).Find(&vulns); err != nil {
		return nil, nil, err
	}
	if len(vulns) == 0 {
		blocking = append(blocking, "at least one affected package is required")
	}
	for _, v := range vulns {
		if v.VulnerableVersionRange == "" {
			blocking = append(blocking, fmt.Sprintf("package %q has no vulnerable version range", v.PackageName))
			continue
		}
		r, err := versionrange.Parse(v.VulnerableVersionRange)
		if err != nil {
			blocking = append(blocking, fmt.Sprintf("package %q has an unparseable vulnerable version range: %v", v.PackageName, err))
			continue
		}
		if r.IsUnbounded() {
			warnings = append(warnings, fmt.Sprintf("package %q has an unbounded vulnerable version range", v.PackageName))
		}

		// A patched version is a hard blocker here, unlike GitHub (which only
		// documents it as advice): its absence is what breaks downstream
		// scanners that expect a "fixed" event, per the plan's publish
		// checklist.
		if v.PatchedVersions == "" {
			blocking = append(blocking, fmt.Sprintf("package %q has no patched version set", v.PackageName))
			continue
		}
		if _, err := versionrange.Parse(v.PatchedVersions); err != nil {
			blocking = append(blocking, fmt.Sprintf("package %q has an unparseable patched version: %v", v.PackageName, err))
		}
	}

	if advisory.CVSSv3Vector == "" && advisory.CVSSv4Vector == "" {
		blocking = append(blocking, "a CVSS vector is required")
	}
	if advisory.CVSSv3Vector != "" {
		v31, err := cvss.ParseV31(advisory.CVSSv3Vector)
		if err != nil {
			blocking = append(blocking, fmt.Sprintf("CVSS v3.1 vector is invalid: %v", err))
		} else if advisory.Severity != "" && advisory.Severity != security_model.Severity(cvss.SeverityFromScore(v31.BaseScore())) {
			blocking = append(blocking, fmt.Sprintf("severity %q contradicts the CVSS v3.1 score %.1f", advisory.Severity, v31.BaseScore()))
		}
	}
	if advisory.CVSSv4Vector != "" {
		if err := cvss.ValidateV40(advisory.CVSSv4Vector); err != nil {
			blocking = append(blocking, fmt.Sprintf("CVSS v4.0 vector is invalid: %v", err))
		}
	}

	var credits []*security_model.AdvisoryCredit
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).Find(&credits); err != nil {
		return nil, nil, err
	}
	for _, c := range credits {
		// A credit is "resolved" once it has been accepted or declined, or
		// is explicitly anonymous (a local user with no accept/decline
		// needed isn't possible here - UserID identifies a real account
		// that must act - so pending on a UserID credit blocks publish).
		if c.UserID > 0 && c.State == security_model.CreditStatePending {
			blocking = append(blocking, "one or more credits are still pending acceptance")
			break
		}
	}

	return blocking, warnings, nil
}

// PublishAdvisory runs the publish-time checklist and, if it passes,
// transitions advisory to StatePublished: sets PublishedUnix and increments
// Repository.NumPublishedAdvisories. AdvisoryWrite is required, matching
// GitHub's model where any advisory collaborator (not just an admin) may
// publish.
//
// This deliberately does not fire a webhook or send mail: those are a later
// milestone step (see the plan's "Notifications & webhooks" section) and
// this function is the seam they hook into.
func PublishAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory) (warnings []string, err error) {
	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return nil, err
	}
	if !canWrite {
		return nil, ErrPermissionDenied{Doer: doer.ID}
	}
	if !advisory.State.CanTransitionTo(security_model.StatePublished) {
		return nil, PublishChecklistError{Reasons: []string{fmt.Sprintf("cannot publish an advisory in state %q", advisory.State)}}
	}

	blocking, warnings, err := checkPublishReady(ctx, advisory)
	if err != nil {
		return nil, err
	}
	if len(blocking) > 0 {
		return nil, PublishChecklistError{Reasons: blocking}
	}

	err = db.WithTx(ctx, func(ctx context.Context) error {
		advisory.State = security_model.StatePublished
		advisory.PublishedUnix = timeutil.TimeStampNow()
		advisory.PublisherID = doer.ID
		if _, err := db.GetEngine(ctx).ID(advisory.ID).
			Cols("state", "published_unix", "publisher_id").
			Update(advisory); err != nil {
			return err
		}

		_, err := db.Exec(ctx, "UPDATE `repository` SET num_published_advisories = num_published_advisories + 1 WHERE id = ?", advisory.RepoID)
		return err
	})
	if err != nil {
		return nil, err
	}

	return warnings, nil
}
