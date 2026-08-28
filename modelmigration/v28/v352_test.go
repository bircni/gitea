// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v28

import (
	"testing"

	"gitea.dev/modelmigration/migrationtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddMergeQueue(t *testing.T) {
	type ProtectedBranch struct {
		ID       int64  `xorm:"pk autoincr"`
		RepoID   int64  `xorm:"UNIQUE(s)"`
		RuleName string `xorm:"'branch_name' UNIQUE(s)"`
	}

	x, deferable := migrationtest.PrepareTestEnv(t, 0, new(ProtectedBranch))
	defer deferable()
	if x == nil || t.Failed() {
		return
	}

	require.NoError(t, AddMergeQueue(t.Context(), x))

	tables := migrationtest.LoadTableSchemasMap(t, x)
	entryTable := tables["pull_merge_queue_entry"]
	require.NotNil(t, entryTable)
	assert.NotNil(t, entryTable.GetColumn("message"))
	assert.Nil(t, entryTable.GetColumn("merge_title_field"))
	assert.Nil(t, entryTable.GetColumn("merge_message_field"))

	_, err := x.Exec(`INSERT INTO pull_merge_queue_entry
		(repo_id, base_branch, pull_id, position, status, head_commit_id, enqueued_by_id, merge_style, message)
		VALUES (1, 'main', 1, 1, 0, 'abc', 1, 'merge', 'queued merge message')`)
	require.NoError(t, err)
}
