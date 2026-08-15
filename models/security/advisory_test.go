// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateCanTransitionTo(t *testing.T) {
	cases := []struct {
		from, to State
		allowed  bool
	}{
		{StateTriage, StateDraft, true},
		{StateTriage, StateClosed, true},
		{StateTriage, StatePublished, false},
		{StateDraft, StatePublished, true},
		{StateDraft, StateClosed, true},
		{StateDraft, StateWithdrawn, false},
		{StatePublished, StateWithdrawn, true},
		{StatePublished, StateDraft, false},
		{StateClosed, StateDraft, false},
		{StateWithdrawn, StatePublished, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.allowed, c.from.CanTransitionTo(c.to), "%s -> %s", c.from, c.to)
	}
}

func TestGetAdvisoryByID(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	advisory := &Advisory{
		RepoID:   1,
		GTSAID:   "GTSA-2345-2345-2345",
		State:    StateDraft,
		Summary:  "test advisory",
		AuthorID: 1,
	}
	require.NoError(t, db.Insert(t.Context(), advisory))

	got, err := GetAdvisoryByID(t.Context(), advisory.ID)
	require.NoError(t, err)
	assert.Equal(t, advisory.GTSAID, got.GTSAID)

	_, err = GetAdvisoryByID(t.Context(), advisory.ID+1000)
	require.Error(t, err)
	assert.True(t, IsErrAdvisoryNotExist(err))
}

func TestGetAdvisoryByGTSAID(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	advisory := &Advisory{
		RepoID:   1,
		GTSAID:   "GTSA-2345-2345-2345",
		State:    StateDraft,
		Summary:  "test advisory",
		AuthorID: 1,
	}
	require.NoError(t, db.Insert(t.Context(), advisory))

	got, err := GetAdvisoryByGTSAID(t.Context(), advisory.GTSAID)
	require.NoError(t, err)
	assert.Equal(t, advisory.ID, got.ID)

	_, err = GetAdvisoryByGTSAID(t.Context(), "GTSA-0000-0000-0000")
	require.Error(t, err)
	assert.True(t, IsErrAdvisoryNotExist(err))
}
