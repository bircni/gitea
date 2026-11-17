// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	auth_model "gitea.dev/models/auth"
	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	api "gitea.dev/modules/structs"
	commitstatus_service "gitea.dev/services/repository/commitstatus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullMergeQueue(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, _ *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "user2", Name: "repo1"})
		session := loginUser(t, "user2")
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

		setStatus := func(sha string, state commitstatus.CommitStatusState) {
			require.NoError(t, commitstatus_service.CreateCommitStatus(t.Context(), repo, user2, sha, &git_model.CommitStatus{State: state, Context: "ci"}))
		}
		createPull := func(branch string) *issues_model.PullRequest {
			testEditorActionEdit(t, session, "user2", "repo1", "_new", "master", "", map[string]string{
				"tree_path":       branch + ".txt",
				"content":         branch,
				"commit_choice":   "commit-to-new-branch",
				"new_branch_name": branch,
			})
			testPullCreate(t, session, "user2", "repo1", true, "master", branch, branch)
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch})
			headSHA, err := git.GetBranchCommitID(t.Context(), repo, branch)
			require.NoError(t, err)
			setStatus(headSHA, commitstatus.CommitStatusSuccess)
			return pr
		}
		waitForEntry := func(pr *issues_model.PullRequest, cond func(*pull_model.MergeQueueEntry) bool) *pull_model.MergeQueueEntry {
			var entry *pull_model.MergeQueueEntry
			require.Eventually(t, func() bool {
				entry, _ = pull_model.GetMergeQueueEntryByPullID(t.Context(), pr.ID)
				return entry != nil && cond(entry)
			}, 5*time.Second, 50*time.Millisecond)
			return entry
		}
		enqueue := func(pr *issues_model.PullRequest) {
			req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/user2/repo1/pulls/%d/merge-queue", pr.Index), &api.AddToMergeQueueOption{}).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusCreated)
		}

		req := NewRequestWithValues(t, "POST", "/user2/repo1/settings/branches/edit", map[string]string{
			"rule_name":             "master",
			"enable_push":           "true",
			"enable_status_check":   "true",
			"status_check_contexts": "ci",
			"require_merge_queue":   "true",

			"merge_queue_merge_method":                      "merge",
			"merge_queue_max_entries_to_build":              "5",
			"merge_queue_min_entries_to_merge":              "1",
			"merge_queue_max_entries_to_merge":              "5",
			"merge_queue_min_entries_to_merge_wait_minutes": "0",
			"merge_queue_grouping_strategy":                 "ALLGREEN",
			"merge_queue_check_response_timeout_minutes":    "60",
		})
		session.MakeRequest(t, req, http.StatusOK)

		pr1, pr2, pr3 := createPull("queue-1"), createPull("queue-2"), createPull("queue-3")
		enqueue(pr1)
		enqueue(pr2)
		enqueue(pr3)

		// each queue commit is built on the one before it
		built := func(e *pull_model.MergeQueueEntry) bool { return e.IsBuilt() }
		entry1, entry2, entry3 := waitForEntry(pr1, built), waitForEntry(pr2, built), waitForEntry(pr3, built)
		assert.Equal(t, entry1.GroupHeadSHA, entry2.BaseSHA)
		assert.Equal(t, entry2.GroupHeadSHA, entry3.BaseSHA)
		assert.Equal(t, pull_model.MergeQueueGroupRef("master", pr1.Index, entry1.HeadSHA), entry1.GroupRef)
		groupSHA, err := git.GetFullCommitID(t.Context(), repo, entry1.GroupRef)
		require.NoError(t, err)
		assert.Equal(t, entry1.GroupHeadSHA, groupSHA)

		resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/user2/repo1/pulls/%d/merge_box", pr1.Index)), http.StatusOK)
		assert.Contains(t, resp.Body.String(), "/user2/repo1/queue/master")
		resp = session.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/queue/master"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), fmt.Sprintf("#%d", pr3.Index))

		req = NewRequest(t, "GET", "/api/v1/repos/user2/repo1/merge-queue/master").AddTokenAuth(token)
		var listed []*api.MergeQueueEntry
		DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &listed)
		require.Len(t, listed, 3)
		assert.Equal(t, pr1.Index, listed[0].PullRequestIndex)
		assert.Equal(t, "awaiting_checks", listed[0].State)

		// a failing entry leaves the queue and the entry after it is rebuilt without it
		setStatus(entry2.GroupHeadSHA, commitstatus.CommitStatusFailure)
		require.Eventually(t, func() bool {
			e, _ := pull_model.GetMergeQueueEntryByPullID(t.Context(), pr2.ID)
			return e == nil
		}, 5*time.Second, 50*time.Millisecond)
		unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pr2.IssueID, Type: issues_model.CommentTypePRRemovedFromMergeQueue, Content: string(issues_model.MergeQueueRemovedChecksFailed)})
		entry3 = waitForEntry(pr3, func(e *pull_model.MergeQueueEntry) bool { return e.IsBuilt() && e.BaseSHA == entry1.GroupHeadSHA })

		// passing queue commits fast-forward the base branch
		setStatus(entry1.GroupHeadSHA, commitstatus.CommitStatusSuccess)
		setStatus(entry3.GroupHeadSHA, commitstatus.CommitStatusSuccess)
		require.Eventually(t, func() bool {
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr3.ID})
			return pr.HasMerged
		}, 5*time.Second, 50*time.Millisecond)
		pr1 = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr1.ID})
		pr3 = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr3.ID})
		assert.True(t, pr1.HasMerged)
		assert.Equal(t, entry1.GroupHeadSHA, pr1.MergedCommitID)
		assert.Equal(t, entry3.GroupHeadSHA, pr3.MergedCommitID)
		masterSHA, err := git.GetBranchCommitID(t.Context(), repo, "master")
		require.NoError(t, err)
		assert.Equal(t, entry3.GroupHeadSHA, masterSHA)
		require.Eventually(t, func() bool {
			has, _ := pull_model.HasMergeQueueEntries(t.Context(), repo.ID, "master")
			return !has
		}, 5*time.Second, 50*time.Millisecond)
		assert.False(t, git.IsReferenceExist(t.Context(), repo, entry1.GroupRef))

		// pull requests can't skip the queue
		pr4 := createPull("queue-4")
		req = NewRequestWithValues(t, "POST", fmt.Sprintf("/user2/repo1/pulls/%d/merge", pr4.Index), map[string]string{"do": "merge"})
		session.MakeRequest(t, req, http.StatusOK)
		waitForEntry(pr4, func(*pull_model.MergeQueueEntry) bool { return true })
		pr4 = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr4.ID})
		assert.False(t, pr4.HasMerged)
	})
}
