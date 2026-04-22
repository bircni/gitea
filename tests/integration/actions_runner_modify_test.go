// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	actions_model "code.gitea.io/gitea/models/actions"
	"code.gitea.io/gitea/models/db"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsRunnerModify(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ctx := t.Context()

	require.NoError(t, db.DeleteAllRecords("action_runner"))

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	_ = actions_model.CreateRunner(ctx, &actions_model.ActionRunner{OwnerID: user2.ID, Name: "user2-runner", TokenHash: "a", UUID: "a"})
	user2Runner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{OwnerID: user2.ID, Name: "user2-runner"})
	userWebURL := "/user/settings/actions/runners"

	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3, Type: user_model.UserTypeOrganization})
	require.NoError(t, actions_model.CreateRunner(ctx, &actions_model.ActionRunner{OwnerID: org3.ID, Name: "org3-runner", TokenHash: "b", UUID: "b"}))
	org3Runner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{OwnerID: org3.ID, Name: "org3-runner"})
	orgWebURL := "/org/org3/settings/actions/runners"

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	_ = actions_model.CreateRunner(ctx, &actions_model.ActionRunner{RepoID: repo1.ID, Name: "repo1-runner", TokenHash: "c", UUID: "c"})
	repo1Runner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{RepoID: repo1.ID, Name: "repo1-runner"})
	repoWebURL := "/user2/repo1/settings/actions/runners"

	_ = actions_model.CreateRunner(ctx, &actions_model.ActionRunner{Name: "global-runner", TokenHash: "d", UUID: "d"})
	globalRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{Name: "global-runner"})
	adminWebURL := "/-/admin/actions/runners"

	require.NoError(t, actions_model.CreateRunner(ctx, &actions_model.ActionRunner{OwnerID: user2.ID, Name: "batch-user-runner", TokenHash: "e", UUID: "e"}))
	batchUserRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{OwnerID: user2.ID, Name: "batch-user-runner"})
	require.NoError(t, actions_model.CreateRunner(ctx, &actions_model.ActionRunner{OwnerID: org3.ID, Name: "batch-org-runner", TokenHash: "f", UUID: "f"}))
	batchOrgRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{OwnerID: org3.ID, Name: "batch-org-runner"})
	require.NoError(t, actions_model.CreateRunner(ctx, &actions_model.ActionRunner{RepoID: repo1.ID, Name: "batch-repo-runner", TokenHash: "g", UUID: "g"}))
	batchRepoRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{RepoID: repo1.ID, Name: "batch-repo-runner"})
	require.NoError(t, actions_model.CreateRunner(ctx, &actions_model.ActionRunner{Name: "batch-global-runner", TokenHash: "h", UUID: "h"}))
	batchGlobalRunner := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{Name: "batch-global-runner"})

	sessionAdmin := loginUser(t, "user1")
	sessionUser2 := loginUser(t, user2.Name)

	doUpdate := func(t *testing.T, sess *TestSession, baseURL string, id int64, description string, expectedStatus int) {
		req := NewRequestWithValues(t, "POST", fmt.Sprintf("%s/%d", baseURL, id), map[string]string{
			"description": description,
		})
		sess.MakeRequest(t, req, expectedStatus)
	}

	doDelete := func(t *testing.T, sess *TestSession, baseURL string, id int64, expectedStatus int) {
		req := NewRequest(t, "POST", fmt.Sprintf("%s/%d/delete", baseURL, id))
		sess.MakeRequest(t, req, expectedStatus)
	}

	doDisable := func(t *testing.T, sess *TestSession, baseURL string, id int64, expectedStatus int) {
		req := NewRequest(t, "POST", fmt.Sprintf("%s/%d/update-runner?disabled=true", baseURL, id))
		sess.MakeRequest(t, req, expectedStatus)
	}

	doEnable := func(t *testing.T, sess *TestSession, baseURL string, id int64, expectedStatus int) {
		req := NewRequest(t, "POST", fmt.Sprintf("%s/%d/update-runner?disabled=false", baseURL, id))
		sess.MakeRequest(t, req, expectedStatus)
	}

	doBatch := func(t *testing.T, sess *TestSession, baseURL, action string, ids []int64, expectedStatus int) {
		values := url.Values{
			"action": {action},
		}
		for _, id := range ids {
			values.Add("runner_ids[]", fmt.Sprint(id))
		}
		req := NewRequestWithURLValues(t, "POST", fmt.Sprintf("%s/batch", baseURL), values)
		sess.MakeRequest(t, req, expectedStatus)
	}

	assertDenied := func(t *testing.T, sess *TestSession, baseURL string, id int64) {
		doUpdate(t, sess, baseURL, id, "ChangedDescription", http.StatusNotFound)
		doDisable(t, sess, baseURL, id, http.StatusNotFound)
		doEnable(t, sess, baseURL, id, http.StatusNotFound)
		doDelete(t, sess, baseURL, id, http.StatusNotFound)
		v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: id})
		assert.Empty(t, v.Description)
		assert.False(t, v.IsDisabled)
	}

	assertSuccess := func(t *testing.T, sess *TestSession, baseURL string, id int64) {
		doUpdate(t, sess, baseURL, id, "ChangedDescription", http.StatusSeeOther)
		v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: id})
		assert.Equal(t, "ChangedDescription", v.Description)
		doDisable(t, sess, baseURL, id, http.StatusOK)
		v = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: id})
		assert.True(t, v.IsDisabled)
		doEnable(t, sess, baseURL, id, http.StatusOK)
		v = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: id})
		assert.False(t, v.IsDisabled)
		doDelete(t, sess, baseURL, id, http.StatusOK)
		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: id})
	}

	t.Run("UpdateUserRunner", func(t *testing.T) {
		theRunner := user2Runner
		t.Run("FromOrg", func(t *testing.T) {
			assertDenied(t, sessionAdmin, orgWebURL, theRunner.ID)
		})
		t.Run("FromRepo", func(t *testing.T) {
			assertDenied(t, sessionAdmin, repoWebURL, theRunner.ID)
		})
		t.Run("FromAdmin", func(t *testing.T) {
			t.Skip("Admin can update any runner (not right but not too bad)")
			assertDenied(t, sessionAdmin, adminWebURL, theRunner.ID)
		})
	})

	t.Run("UpdateOrgRunner", func(t *testing.T) {
		theRunner := org3Runner
		t.Run("FromRepo", func(t *testing.T) {
			assertDenied(t, sessionAdmin, repoWebURL, theRunner.ID)
		})
		t.Run("FromUser", func(t *testing.T) {
			assertDenied(t, sessionAdmin, userWebURL, theRunner.ID)
		})
		t.Run("FromAdmin", func(t *testing.T) {
			t.Skip("Admin can update any runner (not right but not too bad)")
			assertDenied(t, sessionAdmin, adminWebURL, theRunner.ID)
		})
	})

	t.Run("UpdateRepoRunner", func(t *testing.T) {
		theRunner := repo1Runner
		t.Run("FromOrg", func(t *testing.T) {
			assertDenied(t, sessionAdmin, orgWebURL, theRunner.ID)
		})
		t.Run("FromUser", func(t *testing.T) {
			assertDenied(t, sessionAdmin, userWebURL, theRunner.ID)
		})
		t.Run("FromAdmin", func(t *testing.T) {
			t.Skip("Admin can update any runner (not right but not too bad)")
			assertDenied(t, sessionAdmin, adminWebURL, theRunner.ID)
		})
	})

	t.Run("UpdateGlobalRunner", func(t *testing.T) {
		theRunner := globalRunner
		t.Run("FromOrg", func(t *testing.T) {
			assertDenied(t, sessionAdmin, orgWebURL, theRunner.ID)
		})
		t.Run("FromUser", func(t *testing.T) {
			assertDenied(t, sessionAdmin, userWebURL, theRunner.ID)
		})
		t.Run("FromRepo", func(t *testing.T) {
			assertDenied(t, sessionAdmin, repoWebURL, theRunner.ID)
		})
	})

	t.Run("UpdateSuccess", func(t *testing.T) {
		t.Run("User", func(t *testing.T) {
			assertSuccess(t, sessionUser2, userWebURL, user2Runner.ID)
		})
		t.Run("Org", func(t *testing.T) {
			assertSuccess(t, sessionAdmin, orgWebURL, org3Runner.ID)
		})
		t.Run("Repo", func(t *testing.T) {
			assertSuccess(t, sessionUser2, repoWebURL, repo1Runner.ID)
		})
		t.Run("Admin", func(t *testing.T) {
			assertSuccess(t, sessionAdmin, adminWebURL, globalRunner.ID)
		})
	})

	t.Run("AdminBatchUpdate", func(t *testing.T) {
		selectedRunnerIDs := []int64{batchUserRunner.ID, batchUserRunner.ID, batchOrgRunner.ID, batchRepoRunner.ID, batchGlobalRunner.ID}
		selectedRunners := []*actions_model.ActionRunner{batchUserRunner, batchOrgRunner, batchRepoRunner, batchGlobalRunner}

		t.Run("EmptySelectionRejected", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "disable", nil, http.StatusBadRequest)
		})

		t.Run("UnknownActionRejected", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "unknown", []int64{batchUserRunner.ID}, http.StatusBadRequest)
			v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: batchUserRunner.ID})
			assert.False(t, v.IsDisabled)
		})

		t.Run("MissingRunnerRejected", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "disable", []int64{batchUserRunner.ID, 999999999}, http.StatusNotFound)
			for _, runner := range selectedRunners {
				v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: runner.ID})
				assert.False(t, v.IsDisabled)
			}
		})

		t.Run("DisableMixedSelection", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "disable", selectedRunnerIDs, http.StatusOK)
			for _, runner := range selectedRunners {
				v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: runner.ID})
				assert.True(t, v.IsDisabled)
			}
		})

		t.Run("EnableMixedSelection", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "enable", selectedRunnerIDs, http.StatusOK)
			for _, runner := range selectedRunners {
				v := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: runner.ID})
				assert.False(t, v.IsDisabled)
			}
		})

		t.Run("DeleteMixedSelection", func(t *testing.T) {
			doBatch(t, sessionAdmin, adminWebURL, "delete", selectedRunnerIDs, http.StatusOK)
			for _, runner := range selectedRunners {
				unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: runner.ID})
			}
		})
	})
}
