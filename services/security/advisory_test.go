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

func TestCreateAdvisoryRequiresAdmin(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 40})

	err := CreateAdvisory(ctx, writer, repo, &security_model.Advisory{Summary: "s"})
	require.Error(t, err, "plain repo write access must not be able to create an advisory")

	advisory := &security_model.Advisory{Summary: "test summary"}
	err = CreateAdvisory(ctx, owner, repo, advisory)
	require.NoError(t, err)
	assert.Equal(t, security_model.StateDraft, advisory.State)
	assert.Equal(t, owner.ID, advisory.AuthorID)
	assert.NotEmpty(t, advisory.GTSAID)
}

func TestUpdateAdvisoryRejectsContradictorySeverity(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	advisory := &security_model.Advisory{Summary: "test summary"}
	require.NoError(t, CreateAdvisory(ctx, owner, repo, advisory))

	// AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H scores 9.8 (critical), so
	// asserting "low" must be rejected.
	err := UpdateAdvisory(ctx, owner, advisory, &AdvisoryUpdate{
		Summary:      "updated",
		Severity:     security_model.SeverityLow,
		CVSSv3Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
	})
	require.Error(t, err)

	err = UpdateAdvisory(ctx, owner, advisory, &AdvisoryUpdate{
		Summary:      "updated",
		Severity:     security_model.SeverityCritical,
		CVSSv3Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
	})
	require.NoError(t, err)
	assert.InDelta(t, 9.8, advisory.CVSSv3Score, 0.01)
}

func TestAddVulnerabilityNormalizesRanges(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	advisory := &security_model.Advisory{Summary: "test summary"}
	require.NoError(t, CreateAdvisory(ctx, owner, repo, advisory))

	vuln := &security_model.AdvisoryVulnerability{
		Ecosystem:              "npm",
		PackageName:            "left-pad",
		VulnerableVersionRange: ">=1.0.0,<1.2.3",
		PatchedVersions:        ">= 1.2.3",
	}
	require.NoError(t, AddVulnerability(ctx, owner, advisory, vuln))
	assert.Equal(t, ">= 1.0.0, < 1.2.3", vuln.VulnerableVersionRange)

	bad := &security_model.AdvisoryVulnerability{
		Ecosystem:              "npm",
		PackageName:            "left-pad",
		VulnerableVersionRange: "not a range",
	}
	require.Error(t, AddVulnerability(ctx, owner, advisory, bad))
}
