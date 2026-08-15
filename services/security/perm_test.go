// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/perm"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdvisoryACLDeniesNonCollaboratorWriter is the security-critical test
// called out in the milestone plan's build order: a repo *writer* who is not
// an advisory collaborator must not be able to read or write a draft
// advisory they did not author, even though they hold genuine repo write
// access. This is exactly the case a naive access_model.Permission-based
// check would get wrong (see the package doc on CanAdminAdvisory).
//
// Fixture data used, all pre-existing in models/fixtures (per repo
// convention, tests must not add new fixture rows):
//   - repo1 (id=1) is owned by user2 (models/fixtures/repository.yml).
//   - user40 holds mode=2 (write) access on repo1 via
//     models/fixtures/access.yml row id=30 - real repo write access, not
//     ownership or admin, and no security_advisory_collaborator row.
func TestAdvisoryACLDeniesNonCollaboratorWriter(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	writer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 40})

	// Sanity-check the fixture actually gives the writer real repo write
	// access (models/fixtures/access.yml id=30), so a false result below is
	// a genuine ACL assertion and not an accident of missing setup.
	writeMode, err := access_model.AccessLevel(ctx, writer, repo)
	require.NoError(t, err)
	require.Equal(t, perm.AccessModeWrite, writeMode, "fixture must grant the writer plain repo write access")

	draft := &security_model.Advisory{
		RepoID:   repo.ID,
		GTSAID:   "GTSA-2345-2345-2345",
		State:    security_model.StateDraft,
		Summary:  "draft advisory",
		AuthorID: owner.ID,
	}
	require.NoError(t, db.Insert(ctx, draft))

	canRead, err := CanReadAdvisory(ctx, writer, draft)
	require.NoError(t, err)
	assert.False(t, canRead, "repo writer without an advisory-collaborator row must not read a draft advisory")

	canWrite, err := CanWriteAdvisory(ctx, writer, draft)
	require.NoError(t, err)
	assert.False(t, canWrite, "repo writer without an advisory-collaborator row must not write a draft advisory")

	canAdmin, err := CanAdminAdvisory(ctx, writer, repo)
	require.NoError(t, err)
	assert.False(t, canAdmin, "plain repo write access must not confer advisory-admin rights")

	// The owner, by contrast, has full access at every level.
	canRead, err = CanReadAdvisory(ctx, owner, draft)
	require.NoError(t, err)
	assert.True(t, canRead)

	canWrite, err = CanWriteAdvisory(ctx, owner, draft)
	require.NoError(t, err)
	assert.True(t, canWrite)

	canAdmin, err = CanAdminAdvisory(ctx, owner, repo)
	require.NoError(t, err)
	assert.True(t, canAdmin)

	// Once explicitly added as an advisory collaborator, the same writer
	// gains read/write access to the advisory - but still not admin.
	require.NoError(t, db.Insert(ctx, &security_model.AdvisoryCollaborator{
		AdvisoryID: draft.ID,
		UserID:     writer.ID,
	}))

	canRead, err = CanReadAdvisory(ctx, writer, draft)
	require.NoError(t, err)
	assert.True(t, canRead, "an explicit advisory collaborator must be able to read the draft")

	canWrite, err = CanWriteAdvisory(ctx, writer, draft)
	require.NoError(t, err)
	assert.True(t, canWrite, "an explicit advisory collaborator must be able to write the draft")

	canAdmin, err = CanAdminAdvisory(ctx, writer, repo)
	require.NoError(t, err)
	assert.False(t, canAdmin, "advisory collaborator status must not confer advisory-admin rights")
}

// TestAdvisoryACLPublishedVisibleToRepoReader checks the "Published :=
// anyone who can read the repo" half of the ACL: a published advisory is
// visible to a plain repo reader, independent of the draft-only
// collaborator/author checks above.
func TestAdvisoryACLPublishedVisibleToRepoReader(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	// repo1 is public, so an anonymous (nil) doer can already read it.
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	assert.False(t, repo.IsPrivate)
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	published := &security_model.Advisory{
		RepoID:   repo.ID,
		GTSAID:   "GTSA-6789-6789-6789",
		State:    security_model.StatePublished,
		Summary:  "published advisory",
		AuthorID: owner.ID,
	}
	require.NoError(t, db.Insert(ctx, published))

	canRead, err := CanReadAdvisory(ctx, nil, published)
	require.NoError(t, err)
	assert.True(t, canRead, "a published advisory on a public repo must be visible to anonymous visitors")
}
