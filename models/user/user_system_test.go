// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemUser(t *testing.T) {
	u, err := GetPossibleUserByID(t.Context(), -1)
	require.NoError(t, err)
	assert.Equal(t, "Ghost", u.Name)
	assert.Equal(t, "ghost", u.LowerName)
	assert.True(t, u.IsGhost())

	u = GetSystemUserByName("gHost")
	require.NotNil(t, u)
	assert.Equal(t, "Ghost", u.Name)

	u, err = GetPossibleUserByID(t.Context(), -2)
	require.NoError(t, err)
	assert.Equal(t, "gitea-actions", u.Name)
	assert.Equal(t, "gitea-actions", u.LowerName)
	assert.True(t, u.IsGiteaActions())

	u = GetSystemUserByName("Gitea-actionS")
	require.NotNil(t, u)
	assert.Equal(t, "Gitea Actions", u.FullName)

	_, err = GetPossibleUserByID(t.Context(), -3)
	require.Error(t, err)
}

func TestActionsUserTaskEncoding(t *testing.T) {
	t.Run("TaskIDOnly", func(t *testing.T) {
		u := NewActionsUserWithTaskID(47)
		taskID, ok := GetActionsUserTaskID(u)
		require.True(t, ok)
		assert.Equal(t, int64(47), taskID)

		payload, ok := GetActionsUserTaskPayload(u)
		assert.False(t, ok)
		assert.Empty(t, payload)
	})

	t.Run("TaskIDWithPayload", func(t *testing.T) {
		u := NewActionsUserWithTaskPayload(53, "encoded-payload")
		taskID, ok := GetActionsUserTaskID(u)
		require.True(t, ok)
		assert.Equal(t, int64(53), taskID)

		payload, ok := GetActionsUserTaskPayload(u)
		require.True(t, ok)
		assert.Equal(t, "encoded-payload", payload)
	})

	t.Run("InvalidShape", func(t *testing.T) {
		u := NewActionsUser()
		u.LoginName = "@gitea-actions/not-a-number/encoded-payload"

		taskID, ok := GetActionsUserTaskID(u)
		assert.False(t, ok)
		assert.Equal(t, int64(0), taskID)

		payload, ok := GetActionsUserTaskPayload(u)
		require.True(t, ok)
		assert.Equal(t, "encoded-payload", payload)
	})
}
