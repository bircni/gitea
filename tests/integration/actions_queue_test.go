// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	actions_model "gitea.dev/models/actions"
	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsQueue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ctx := t.Context()

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1}) // owned by user2

	// A queued job in repo1: waiting, unclaimed, so it must appear in the build queue at every scope
	// that covers repo1 (repo, its owner user2, and admin/global).
	run := &actions_model.ActionRun{
		Title:         "queue-test",
		RepoID:        repo1.ID,
		OwnerID:       user2.ID,
		Index:         8801,
		WorkflowID:    "test.yaml",
		TriggerUserID: user2.ID,
		Ref:           "refs/heads/master",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		EventPayload:  "{}",
		Status:        actions_model.StatusWaiting,
	}
	require.NoError(t, db.Insert(ctx, run))
	const queuedJobName = "queued-job-marker"
	job := &actions_model.ActionRunJob{
		RunID:   run.ID,
		RepoID:  repo1.ID,
		OwnerID: user2.ID,
		Name:    queuedJobName,
		JobID:   queuedJobName,
		RunsOn:  []string{"ubuntu-latest"},
		Status:  actions_model.StatusWaiting,
	}
	require.NoError(t, db.Insert(ctx, job))

	sessionAdmin := loginUser(t, "user1") // site admin
	sessionUser2 := loginUser(t, user2.Name)
	sessionUser4 := loginUser(t, "user4") // unrelated user

	assertQueued := func(t *testing.T, sess *TestSession, url string) string {
		t.Helper()
		body := sess.MakeRequest(t, NewRequest(t, "GET", url), http.StatusOK).Body.String()
		assert.Contains(t, body, queuedJobName)
		return body
	}

	// The page renders and lists the queued job for every scope that covers repo1.
	fullPage := assertQueued(t, sessionAdmin, "/-/admin/actions/queue")
	assertQueued(t, sessionUser2, "/user2/repo1/settings/actions/queue")
	assertQueued(t, sessionUser2, "/user/settings/actions/queue")

	// The auto-refresh endpoint returns just the list fragment (no full-page chrome), still listing the job.
	assert.Contains(t, fullPage, `<html`, "the normal page is a full document")
	refresh := sessionAdmin.MakeRequest(t, NewRequest(t, "GET", "/-/admin/actions/queue?refresh=1"), http.StatusOK).Body.String()
	assert.Contains(t, refresh, `id="actions-queue-list"`)
	assert.Contains(t, refresh, queuedJobName)
	assert.NotContains(t, refresh, `<html`, "the refresh response is a fragment, not a full page")

	// Org scope renders too (org3 is owned by user2; empty queue is fine).
	sessionUser2.MakeRequest(t, NewRequest(t, "GET", "/org/org3/settings/actions/queue"), http.StatusOK)

	// Access is gated like the rest of the Actions settings section: an unrelated user cannot view
	// another repo's or org's queue.
	sessionUser4.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/settings/actions/queue"), http.StatusNotFound)
	sessionUser4.MakeRequest(t, NewRequest(t, "GET", "/org/org3/settings/actions/queue"), http.StatusNotFound)
}
