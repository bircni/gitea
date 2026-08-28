// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mergequeue

import (
	"testing"

	pull_model "gitea.dev/models/pull"

	"github.com/stretchr/testify/assert"
)

func TestTestingEntriesSkipsRemoved(t *testing.T) {
	entries := []*pull_model.MergeQueueEntry{
		{ID: 1, Status: pull_model.MergeQueueEntryStatusTesting},
		{ID: 2, Status: pull_model.MergeQueueEntryStatusRemoved},
		{ID: 3, Status: pull_model.MergeQueueEntryStatusTesting},
		{ID: 4, Status: pull_model.MergeQueueEntryStatusFailed},
	}
	got := testingEntries(entries)
	assert.Equal(t, []int64{1, 3}, entryIDs(got))
}

func TestBisectHalvesStartsOnlyFirstHalf(t *testing.T) {
	entries := []*pull_model.MergeQueueEntry{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}
	first, rest := bisectHalves(entries)
	assert.Equal(t, []int64{1, 2}, entryIDs(first))
	assert.Equal(t, []int64{3, 4}, entryIDs(rest))

	odd := []*pull_model.MergeQueueEntry{{ID: 1}, {ID: 2}, {ID: 3}}
	first, rest = bisectHalves(odd)
	assert.Equal(t, []int64{1}, entryIDs(first))
	assert.Equal(t, []int64{2, 3}, entryIDs(rest))
}
