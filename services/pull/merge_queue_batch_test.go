// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeQueueTempRefNameRoundTrip(t *testing.T) {
	ref := MergeQueueTempRefName("release/v1", 42)
	assert.Equal(t, "refs/for-merge-queue/release/v1/42", ref)

	baseBranch, batchID, ok := ParseMergeQueueTempRef(ref)
	assert.True(t, ok)
	assert.Equal(t, "release/v1", baseBranch)
	assert.EqualValues(t, 42, batchID)
}

func TestParseMergeQueueTempRefRejectsOtherRefs(t *testing.T) {
	for _, ref := range []string{
		"refs/heads/main",
		"refs/pull/1/head",
		"refs/for-merge-queue/",
		"refs/for-merge-queue/main",
		"refs/for-merge-queue/main/not-a-number",
	} {
		_, _, ok := ParseMergeQueueTempRef(ref)
		assert.Falsef(t, ok, "expected %q to not be parsed as a merge queue ref", ref)
	}
}
