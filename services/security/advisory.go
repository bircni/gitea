// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/security/cvss"
	"gitea.dev/modules/security/versionrange"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
)

// ErrPermissionDenied is returned by service-layer calls when doer lacks the
// access level a given operation requires.
type ErrPermissionDenied struct {
	Doer int64
}

func (err ErrPermissionDenied) Error() string {
	return "user does not have permission to perform this advisory operation"
}

// CreateAdvisory creates a new maintainer-authored advisory in StateDraft.
// Reports (StateTriage) are out of scope for this milestone - see
// services/security/report.go, a later milestone.
func CreateAdvisory(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, advisory *security_model.Advisory) error {
	isAdmin, err := CanAdminAdvisory(ctx, doer, repo)
	if err != nil {
		return err
	}
	if !isAdmin {
		return ErrPermissionDenied{Doer: doer.ID}
	}

	if advisory.Summary == "" {
		return util.NewInvalidArgumentErrorf("advisory summary is required")
	}
	if err := validateCVSS(advisory); err != nil {
		return err
	}

	advisory.RepoID = repo.ID
	advisory.AuthorID = doer.ID
	advisory.State = security_model.StateDraft

	gtsaID, err := security_model.GenerateGTSAID(ctx)
	if err != nil {
		return err
	}
	advisory.GTSAID = gtsaID

	return db.Insert(ctx, advisory)
}

// AdvisoryUpdate carries the subset of Advisory fields UpdateAdvisory allows
// changing. State transitions go through TransitionAdvisory instead, since
// publishing has its own checklist (services/security/publish.go).
type AdvisoryUpdate struct {
	Summary      string
	Description  string
	Severity     security_model.Severity
	CVSSv3Vector string
	CVSSv4Vector string
	CWEIDs       []string
	CVEID        string
}

// UpdateAdvisory applies an AdvisoryUpdate to advisory, enforcing
// AdvisoryWrite and CVSS validation. It does not change advisory.State.
func UpdateAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, update *AdvisoryUpdate) error {
	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrPermissionDenied{Doer: doer.ID}
	}
	if update.Summary == "" {
		return util.NewInvalidArgumentErrorf("advisory summary is required")
	}

	advisory.Summary = update.Summary
	advisory.Description = update.Description
	advisory.Severity = update.Severity
	advisory.CVSSv3Vector = update.CVSSv3Vector
	advisory.CVSSv4Vector = update.CVSSv4Vector
	advisory.CWEIDs = update.CWEIDs
	advisory.CVEID = update.CVEID

	if err := validateCVSS(advisory); err != nil {
		return err
	}

	_, err = db.GetEngine(ctx).ID(advisory.ID).Cols(
		"summary", "description", "severity",
		"cvss_v3_vector", "cvss_v3_score", "cvss_v4_vector", "cvss_v4_score",
		"cwe_ids", "cve_id",
	).Update(advisory)
	return err
}

// validateCVSS parses any CVSS vectors set on advisory, fills in the derived
// v3.1 score, and rejects a stored Severity that contradicts the computed
// v3.1 score. v4.0 is validated but not scored (see modules/security/cvss).
func validateCVSS(advisory *security_model.Advisory) error {
	if advisory.CVSSv3Vector != "" {
		v31, err := cvss.ParseV31(advisory.CVSSv3Vector)
		if err != nil {
			return err
		}
		advisory.CVSSv3Score = v31.BaseScore()

		if advisory.Severity != "" && advisory.Severity != security_model.Severity(cvss.SeverityFromScore(advisory.CVSSv3Score)) {
			return util.NewInvalidArgumentErrorf("severity %q contradicts the CVSS v3.1 score %.1f", advisory.Severity, advisory.CVSSv3Score)
		}
	} else {
		advisory.CVSSv3Score = 0
	}

	if advisory.CVSSv4Vector != "" {
		if err := cvss.ValidateV40(advisory.CVSSv4Vector); err != nil {
			return err
		}
	}

	return nil
}

// TransitionAdvisory moves advisory to next, enforcing both AdvisoryWrite
// and State.CanTransitionTo. Publishing (next == StatePublished) must go
// through PublishAdvisory instead, since it has its own checklist.
func TransitionAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, next security_model.State) error {
	if next == security_model.StatePublished {
		return util.NewInvalidArgumentErrorf("use PublishAdvisory to transition to published")
	}

	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrPermissionDenied{Doer: doer.ID}
	}
	if !advisory.State.CanTransitionTo(next) {
		return util.NewInvalidArgumentErrorf("cannot transition advisory from %q to %q", advisory.State, next)
	}

	advisory.State = next
	cols := []string{"state"}
	switch next {
	case security_model.StateClosed:
		advisory.ClosedUnix = timeutil.TimeStampNow()
		cols = append(cols, "closed_unix")
	case security_model.StateWithdrawn:
		advisory.WithdrawnUnix = timeutil.TimeStampNow()
		cols = append(cols, "withdrawn_unix")
	}

	_, err = db.GetEngine(ctx).ID(advisory.ID).Cols(cols...).Update(advisory)
	return err
}

// AddCollaborator grants userID write access to advisory. AdvisoryAdmin only.
func AddCollaborator(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, userID int64) error {
	repo, err := repo_model.GetRepositoryByID(ctx, advisory.RepoID)
	if err != nil {
		return err
	}
	isAdmin, err := CanAdminAdvisory(ctx, doer, repo)
	if err != nil {
		return err
	}
	if !isAdmin {
		return ErrPermissionDenied{Doer: doer.ID}
	}

	exists, err := security_model.IsAdvisoryCollaborator(ctx, advisory.ID, userID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	return db.Insert(ctx, &security_model.AdvisoryCollaborator{
		AdvisoryID: advisory.ID,
		UserID:     userID,
	})
}

// RemoveCollaborator revokes userID's write access to advisory. AdvisoryAdmin only.
func RemoveCollaborator(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, userID int64) error {
	repo, err := repo_model.GetRepositoryByID(ctx, advisory.RepoID)
	if err != nil {
		return err
	}
	isAdmin, err := CanAdminAdvisory(ctx, doer, repo)
	if err != nil {
		return err
	}
	if !isAdmin {
		return ErrPermissionDenied{Doer: doer.ID}
	}

	_, err = db.GetEngine(ctx).Where("advisory_id = ? AND user_id = ?", advisory.ID, userID).
		Delete(new(security_model.AdvisoryCollaborator))
	return err
}

// AddVulnerability validates and normalizes vuln's version ranges, then adds
// it to advisory. AdvisoryWrite required.
func AddVulnerability(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, vuln *security_model.AdvisoryVulnerability) error {
	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrPermissionDenied{Doer: doer.ID}
	}
	if err := normalizeVulnerability(vuln); err != nil {
		return err
	}

	vuln.AdvisoryID = advisory.ID
	return db.Insert(ctx, vuln)
}

// UpdateVulnerability validates and normalizes vuln's version ranges, then
// persists it. vuln.AdvisoryID and vuln.ID must already be set.
// AdvisoryWrite required.
func UpdateVulnerability(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, vuln *security_model.AdvisoryVulnerability) error {
	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrPermissionDenied{Doer: doer.ID}
	}
	if err := normalizeVulnerability(vuln); err != nil {
		return err
	}

	_, err = db.GetEngine(ctx).ID(vuln.ID).Cols(
		"ecosystem", "package_name", "vulnerable_version_range", "patched_versions", "vulnerable_functions",
	).Update(vuln)
	return err
}

// RemoveVulnerability deletes a vulnerability entry from advisory. AdvisoryWrite required.
func RemoveVulnerability(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory, vulnerabilityID int64) error {
	canWrite, err := CanWriteAdvisory(ctx, doer, advisory)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrPermissionDenied{Doer: doer.ID}
	}

	_, err = db.GetEngine(ctx).Where("id = ? AND advisory_id = ?", vulnerabilityID, advisory.ID).
		Delete(new(security_model.AdvisoryVulnerability))
	return err
}

// normalizeVulnerability parses vuln's version range fields via
// modules/security/versionrange and rewrites them to their normalized form.
// PatchedVersions is optional at this stage - PublishAdvisory enforces it as
// a hard blocker at publish time, not on every intermediate save.
func normalizeVulnerability(vuln *security_model.AdvisoryVulnerability) error {
	if vuln.Ecosystem == "" {
		return util.NewInvalidArgumentErrorf("vulnerability ecosystem is required")
	}
	if vuln.PackageName == "" {
		return util.NewInvalidArgumentErrorf("vulnerability package name is required")
	}

	if vuln.VulnerableVersionRange != "" {
		r, err := versionrange.Parse(vuln.VulnerableVersionRange)
		if err != nil {
			return err
		}
		vuln.VulnerableVersionRange = r.String()
	}
	if vuln.PatchedVersions != "" {
		r, err := versionrange.Parse(vuln.PatchedVersions)
		if err != nil {
			return err
		}
		vuln.PatchedVersions = r.String()
	}

	return nil
}

// ListAdvisories returns the advisories in repo that doer may see, filtered
// entirely in SQL via security_model.AccessibleAdvisoryCondition so an
// unpublished advisory's existence is never leaked.
func ListAdvisories(ctx context.Context, doer *user_model.User, repo *repo_model.Repository) ([]*security_model.Advisory, error) {
	var advisories []*security_model.Advisory
	err := db.GetEngine(ctx).
		Where("security_advisory.repo_id = ?", repo.ID).
		And(security_model.AccessibleAdvisoryCondition(doer)).
		Find(&advisories)
	return advisories, err
}
