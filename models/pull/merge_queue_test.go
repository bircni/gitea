// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull_test

import (
	"context"
	"testing"

	git_model "gitea.dev/models/git"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enqueueTestEntry(ctx context.Context, pullID int64, headCommitID string, mergeStyle repo_model.MergeStyle) (*pull_model.MergeQueueEntry, error) {
	return pull_model.EnqueueMergeQueueEntry(ctx, pull_model.EnqueueMergeQueueEntryOptions{
		RepoID:       1,
		BaseBranch:   "master",
		PullID:       pullID,
		EnqueuedByID: 2,
		HeadCommitID: headCommitID,
		MergeStyle:   mergeStyle,
	})
}

func TestEnqueueMergeQueueEntry_FIFOPositionAndUniqueness(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// pull_request.yml ids 2 and 3 both target repo 1 / master, and aren't merged.
	e1, err := enqueueTestEntry(t.Context(), 2, "aaaa", repo_model.MergeStyleMerge)
	require.NoError(t, err)
	assert.Equal(t, int64(1), e1.Position)

	e2, err := enqueueTestEntry(t.Context(), 3, "bbbb", repo_model.MergeStyleMerge)
	require.NoError(t, err)
	assert.Equal(t, int64(2), e2.Position)

	// Enqueueing the same PR again while it has an active entry must fail.
	_, err = enqueueTestEntry(t.Context(), 2, "aaaa", repo_model.MergeStyleMerge)
	assert.True(t, pull_model.IsErrAlreadyInMergeQueue(err))

	queued, err := pull_model.GetQueuedEntriesForBranch(t.Context(), 1, "master", 10)
	require.NoError(t, err)
	assert.Len(t, queued, 2)
	assert.Equal(t, e1.ID, queued[0].ID)
	assert.Equal(t, e2.ID, queued[1].ID)

	ahead, err := pull_model.CountQueuedEntriesAhead(t.Context(), 1, "master", e2.Position)
	require.NoError(t, err)
	assert.EqualValues(t, 1, ahead)

	// Removing an entry frees the PR up to be re-enqueued (uniqueness is only enforced for active entries).
	removed, err := pull_model.RemoveMergeQueueEntryByPullID(t.Context(), 2, "test removal")
	require.NoError(t, err)
	assert.True(t, removed)

	exists, _, err := pull_model.GetActiveMergeQueueEntryByPullID(t.Context(), 2)
	require.NoError(t, err)
	assert.False(t, exists)

	e3, err := enqueueTestEntry(t.Context(), 2, "cccc", repo_model.MergeStyleSquash)
	require.NoError(t, err)
	assert.Equal(t, int64(3), e3.Position)
}

func TestEnqueueMergeQueueEntry_PersistsMessage(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	entry, err := pull_model.EnqueueMergeQueueEntry(t.Context(), pull_model.EnqueueMergeQueueEntryOptions{
		RepoID:       1,
		BaseBranch:   "master",
		PullID:       2,
		EnqueuedByID: 2,
		HeadCommitID: "aaaa",
		MergeStyle:   repo_model.MergeStyleMerge,
		Message:      "title\n\nbody",
	})
	require.NoError(t, err)

	exists, got, err := pull_model.GetMergeQueueEntryByRepoAndID(t.Context(), 1, entry.ID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, "title\n\nbody", got.Message)

	exists, _, err = pull_model.GetMergeQueueEntryByRepoAndID(t.Context(), 2, entry.ID)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMergeQueueBatchLifecycle(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	entry, err := enqueueTestEntry(t.Context(), 2, "aaaa", repo_model.MergeStyleMerge)
	require.NoError(t, err)

	active, err := pull_model.HasActiveBatch(t.Context(), 1, "master")
	require.NoError(t, err)
	assert.False(t, active)

	batch, err := pull_model.CreateMergeQueueBatch(t.Context(), 1, "master", "basesha", repo_model.MergeStyleMerge, []*pull_model.MergeQueueEntry{entry})
	require.NoError(t, err)
	assert.Equal(t, pull_model.MergeQueueBatchStatusBuilding, batch.Status)

	active, err = pull_model.HasActiveBatch(t.Context(), 1, "master")
	require.NoError(t, err)
	assert.True(t, active)

	entries, err := pull_model.GetMergeQueueEntriesByBatchID(t.Context(), batch.ID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, pull_model.MergeQueueEntryStatusTesting, entries[0].Status)

	batch.TempCommitID = "deadbeef"
	batch.Status = pull_model.MergeQueueBatchStatusTesting
	require.NoError(t, pull_model.UpdateMergeQueueBatch(t.Context(), batch, "temp_commit_id", "status"))

	found, gotBatch, err := pull_model.GetMergeQueueBatchByTempCommitID(t.Context(), 1, "deadbeef")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, batch.ID, gotBatch.ID)

	require.NoError(t, pull_model.RequeueMergeQueueEntries(t.Context(), []int64{entry.ID}))
	exists, requeued, err := pull_model.GetActiveMergeQueueEntryByPullID(t.Context(), entry.PullID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, pull_model.MergeQueueEntryStatusQueued, requeued.Status)
	assert.EqualValues(t, 0, requeued.BatchID)

	found, gotBatch, err = pull_model.GetMergeQueueBatchByID(t.Context(), batch.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, batch.ID, gotBatch.ID)
}

func TestRequeueMergeQueueEntries_DoesNotResurrectRemoved(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	entry, err := enqueueTestEntry(t.Context(), 2, "aaaa", repo_model.MergeStyleMerge)
	require.NoError(t, err)
	removed, err := pull_model.RemoveMergeQueueEntryByPullID(t.Context(), entry.PullID, "gone")
	require.NoError(t, err)
	require.True(t, removed)

	require.NoError(t, pull_model.RequeueMergeQueueEntries(t.Context(), []int64{entry.ID}))

	exists, _, err := pull_model.GetActiveMergeQueueEntryByPullID(t.Context(), entry.PullID)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestGetMergeQueueEntriesForRuleView_MatchesGlob(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	onMaster, err := enqueueTestEntry(t.Context(), 2, "aaaa", repo_model.MergeStyleMerge)
	require.NoError(t, err)

	onRelease, err := pull_model.EnqueueMergeQueueEntry(t.Context(), pull_model.EnqueueMergeQueueEntryOptions{
		RepoID:       1,
		BaseBranch:   "release/v1",
		PullID:       5,
		EnqueuedByID: 2,
		HeadCommitID: "bbbb",
		MergeStyle:   repo_model.MergeStyleMerge,
	})
	require.NoError(t, err)

	rule := &git_model.ProtectedBranch{RuleName: "release/*"}
	entries, err := pull_model.GetMergeQueueEntriesForRuleView(t.Context(), 1, rule.Match, 20)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, onRelease.ID, entries[0].ID)

	masterRule := &git_model.ProtectedBranch{RuleName: "master"}
	entries, err = pull_model.GetMergeQueueEntriesForRuleView(t.Context(), 1, masterRule.Match, 20)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, onMaster.ID, entries[0].ID)
}
