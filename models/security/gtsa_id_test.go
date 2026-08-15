// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"regexp"
	"testing"

	"gitea.dev/models/db"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var gtsaIDPattern = regexp.MustCompile(`^GTSA(-[23456789cfghjmpqrvwx]{4}){3}$`)

func TestGenerateGTSAIDFormat(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	id, err := GenerateGTSAID(t.Context())
	require.NoError(t, err)
	assert.Regexp(t, gtsaIDPattern, id)
}

func TestGenerateGTSAIDRetriesOnCollision(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	require.NoError(t, db.Insert(t.Context(), &Advisory{
		RepoID:   1,
		GTSAID:   "GTSA-2345-2345-2345",
		State:    StateDraft,
		Summary:  "existing",
		AuthorID: 1,
	}))

	calls := 0
	candidates := []string{"GTSA-2345-2345-2345", "GTSA-6789-6789-6789"}
	id, err := generateGTSAID(t.Context(), func() string {
		id := candidates[calls]
		calls++
		return id
	})
	require.NoError(t, err)
	assert.Equal(t, "GTSA-6789-6789-6789", id)
	assert.Equal(t, 2, calls)
}

func TestGenerateGTSAIDGivesUpAfterMaxAttempts(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	require.NoError(t, db.Insert(t.Context(), &Advisory{
		RepoID:   1,
		GTSAID:   "GTSA-9999-9999-9999",
		State:    StateDraft,
		Summary:  "always colliding",
		AuthorID: 1,
	}))

	calls := 0
	_, err := generateGTSAID(t.Context(), func() string {
		calls++
		return "GTSA-9999-9999-9999"
	})
	require.Error(t, err)
	assert.Equal(t, gtsaMaxGenerateAttempts, calls)
}
