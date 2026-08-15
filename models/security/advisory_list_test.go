// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"

	_ "gitea.dev/models/organization"
	_ "gitea.dev/models/perm/access"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccessibleAdvisoryCondition covers the model-layer condition builder
// only: it is not the security-critical ACL test (that is a services/security
// integration test asserting a repo *writer* who is not a collaborator
// cannot see a draft, per the milestone plan's build order). This just
// checks the SQL the condition builds actually filters as intended for the
// simplest cases: anonymous visitors, the repo owner, and an unrelated user.
func TestAccessibleAdvisoryCondition(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// repo1 (id=1) is owned by user2, per models/fixtures/repository.yml.
	draft := &Advisory{RepoID: 1, GTSAID: "GTSA-2345-2345-2345", State: StateDraft, Summary: "draft", AuthorID: 2}
	published := &Advisory{RepoID: 1, GTSAID: "GTSA-6789-6789-6789", State: StatePublished, Summary: "published", AuthorID: 2}
	require.NoError(t, db.Insert(t.Context(), draft, published))

	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1, OwnerID: 2})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	stranger := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	visibleIDs := func(doer *user_model.User) []int64 {
		var advisories []*Advisory
		require.NoError(t, db.GetEngine(t.Context()).
			Table("security_advisory").
			Where(AccessibleAdvisoryCondition(doer)).
			Find(&advisories))
		ids := make([]int64, 0, len(advisories))
		for _, a := range advisories {
			ids = append(ids, a.ID)
		}
		return ids
	}

	assert.ElementsMatch(t, []int64{published.ID}, visibleIDs(nil))
	assert.ElementsMatch(t, []int64{published.ID}, visibleIDs(stranger))
	assert.ElementsMatch(t, []int64{draft.ID, published.ID}, visibleIDs(owner))
}
