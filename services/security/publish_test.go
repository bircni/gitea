// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishAdvisoryChecklist(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	advisory := &security_model.Advisory{Summary: "test summary"}
	require.NoError(t, CreateAdvisory(ctx, owner, repo, advisory))

	// No vulnerabilities and no CVSS vector yet: publish must be blocked.
	_, err := PublishAdvisory(ctx, owner, advisory)
	require.Error(t, err)
	checklistErr, ok := err.(PublishChecklistError)
	require.True(t, ok)
	assert.Contains(t, checklistErr.Reasons, "at least one affected package is required")
	assert.Contains(t, checklistErr.Reasons, "a CVSS vector is required")

	// Set the CVSS vector but leave the patched version unset: still blocked,
	// even though GitHub only documents a patched version as advice.
	require.NoError(t, UpdateAdvisory(ctx, owner, advisory, &AdvisoryUpdate{
		Summary:      advisory.Summary,
		Severity:     security_model.SeverityCritical,
		CVSSv3Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
	}))
	vuln := &security_model.AdvisoryVulnerability{
		Ecosystem:              "npm",
		PackageName:            "left-pad",
		VulnerableVersionRange: ">= 1.0.0, < 1.2.3",
	}
	require.NoError(t, AddVulnerability(ctx, owner, advisory, vuln))

	_, err = PublishAdvisory(ctx, owner, advisory)
	require.Error(t, err)
	checklistErr, ok = err.(PublishChecklistError)
	require.True(t, ok)
	assert.Contains(t, checklistErr.Reasons, `package "left-pad" has no patched version set`)

	// Set a patched version: now publish must succeed and bump the repo's
	// published-advisory counter.
	require.NoError(t, UpdateVulnerability(ctx, owner, advisory, &security_model.AdvisoryVulnerability{
		ID:                     vuln.ID,
		AdvisoryID:             advisory.ID,
		Ecosystem:              vuln.Ecosystem,
		PackageName:            vuln.PackageName,
		VulnerableVersionRange: vuln.VulnerableVersionRange,
		PatchedVersions:        ">= 1.2.3",
	}))

	before := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID})

	warnings, err := PublishAdvisory(ctx, owner, advisory)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, security_model.StatePublished, advisory.State)
	assert.NotZero(t, advisory.PublishedUnix)

	after := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID})
	assert.Equal(t, before.NumPublishedAdvisories+1, after.NumPublishedAdvisories)

	// Publishing again must fail: published -> published is not a valid transition.
	_, err = PublishAdvisory(ctx, owner, advisory)
	require.Error(t, err)
}

func TestPublishAdvisoryUnboundedRangeIsWarningOnly(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	advisory := &security_model.Advisory{Summary: "test summary"}
	require.NoError(t, CreateAdvisory(ctx, owner, repo, advisory))
	require.NoError(t, UpdateAdvisory(ctx, owner, advisory, &AdvisoryUpdate{
		Summary:      advisory.Summary,
		Severity:     security_model.SeverityCritical,
		CVSSv3Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
	}))
	require.NoError(t, AddVulnerability(ctx, owner, advisory, &security_model.AdvisoryVulnerability{
		Ecosystem:              "npm",
		PackageName:            "left-pad",
		VulnerableVersionRange: ">= 1.0.0",
		PatchedVersions:        ">= 1.2.3",
	}))

	warnings, err := PublishAdvisory(ctx, owner, advisory)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "unbounded")
}
