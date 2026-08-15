// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v28

import (
	"testing"

	"gitea.dev/modelmigration/migrationtest"

	"github.com/stretchr/testify/require"
)

func TestAddSecurityAdvisoryColumnsToRepository(t *testing.T) {
	type Repository struct {
		ID int64 `xorm:"pk autoincr"`
	}

	x, deferable := migrationtest.PrepareTestEnv(t, 0, new(Repository))
	defer deferable()
	if x == nil || t.Failed() {
		return
	}

	_, err := x.Insert(&Repository{})
	require.NoError(t, err)

	require.NoError(t, AddSecurityAdvisoryColumnsToRepository(t.Context(), x))

	type RepositoryAfter struct {
		ID                      int64 `xorm:"pk autoincr"`
		PrivateReportingEnabled bool
		NumPublishedAdvisories  int
	}
	got := new(RepositoryAfter)
	has, err := x.Table("repository").Get(got)
	require.NoError(t, err)
	require.True(t, has)
	require.False(t, got.PrivateReportingEnabled)
	require.Equal(t, 0, got.NumPublishedAdvisories)
}
