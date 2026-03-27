// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"net/http"
	"testing"

	actions_model "code.gitea.io/gitea/models/actions"
	"code.gitea.io/gitea/models/unittest"
	"code.gitea.io/gitea/modules/json"
	"code.gitea.io/gitea/modules/setting"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAuthorizationToken(t *testing.T) {
	var taskID int64 = 23
	token, err := CreateAuthorizationToken(taskID, 1, 2)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return setting.GetGeneralTokenSigningSecret(), nil
	})
	assert.NoError(t, err)
	scp, ok := claims["scp"]
	assert.True(t, ok, "Has scp claim in jwt token")
	assert.Contains(t, scp, "Actions.Results:1:2")
	taskIDClaim, ok := claims["TaskID"]
	assert.True(t, ok, "Has TaskID claim in jwt token")
	assert.InDelta(t, float64(taskID), taskIDClaim, 0, "Supplied taskid must match stored one")
	acClaim, ok := claims["ac"]
	assert.True(t, ok, "Has ac claim in jwt token")
	ac, ok := acClaim.(string)
	assert.True(t, ok, "ac claim is a string for buildx gha cache")
	scopes := []actionsCacheScope{}
	err = json.Unmarshal([]byte(ac), &scopes)
	assert.NoError(t, err, "ac claim is a json list for buildx gha cache")
	assert.GreaterOrEqual(t, len(scopes), 1, "Expected at least one action cache scope for buildx gha cache")
}

func TestParseAuthorizationToken(t *testing.T) {
	var taskID int64 = 23
	token, err := CreateAuthorizationToken(taskID, 1, 2)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	rTaskID, err := ParseAuthorizationToken(&http.Request{
		Header: headers,
	})
	assert.NoError(t, err)
	assert.Equal(t, taskID, rTaskID)
}

func TestParseAuthorizationTokenNoAuthHeader(t *testing.T) {
	headers := http.Header{}
	rTaskID, err := ParseAuthorizationToken(&http.Request{
		Header: headers,
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(0), rTaskID)
}

func TestCreateTaskAuthorizationToken(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	task := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 53})
	require.NoError(t, task.LoadJob(t.Context()))
	require.NoError(t, task.Job.LoadRepo(t.Context()))

	token, err := CreateTaskAuthorizationToken(task)
	require.NoError(t, err)

	meta, err := ParseTaskAuthorizationToken(token)
	require.NoError(t, err)
	assert.Equal(t, task.ID, meta.TaskID)
	assert.Equal(t, task.RepoID, meta.RepoID)
	assert.Equal(t, task.Job.Repo.OwnerID, meta.OwnerID)
	assert.Equal(t, task.Job.Repo.IsPrivate, meta.TaskRepoIsPrivate)
	assert.Equal(t, task.IsForkPullRequest, meta.IsForkPullRequest)
}
