// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTokenMetadataEncoding(t *testing.T) {
	t.Run("NilAndEmpty", func(t *testing.T) {
		encoded, err := EncodeTaskTokenMetadata(nil)
		require.NoError(t, err)
		assert.Empty(t, encoded)

		meta, err := DecodeTaskTokenMetadata("")
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("RoundTrip", func(t *testing.T) {
		original := &TaskTokenMetadata{
			TaskID:            53,
			RepoID:            2,
			OwnerID:           2,
			TaskRepoIsPrivate: true,
			IsForkPullRequest: false,
		}
		encoded, err := EncodeTaskTokenMetadata(original)
		require.NoError(t, err)
		require.NotEmpty(t, encoded)

		decoded, err := DecodeTaskTokenMetadata(encoded)
		require.NoError(t, err)
		assert.Equal(t, original, decoded)
	})
}
