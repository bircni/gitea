// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package versionrange

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionRangeParse(t *testing.T) {
	t.Run("single bound", func(t *testing.T) {
		r, err := Parse(">= 1.0.0")
		require.NoError(t, err)
		require.Len(t, r.Constraints, 1)
		assert.Equal(t, OpGe, r.Constraints[0].Operator)
		assert.Equal(t, "1.0.0", r.Constraints[0].Version)
	})

	t.Run("bounded range", func(t *testing.T) {
		r, err := Parse(">= 1.0.0, < 1.2.3")
		require.NoError(t, err)
		require.Len(t, r.Constraints, 2)
		assert.Equal(t, OpGe, r.Constraints[0].Operator)
		assert.Equal(t, OpLt, r.Constraints[1].Operator)
		assert.Equal(t, "1.2.3", r.Constraints[1].Version)
		assert.False(t, r.IsUnbounded())
	})

	t.Run("normalizes version representation", func(t *testing.T) {
		r, err := Parse("= v1.0")
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", r.Constraints[0].Version)
	})

	t.Run("double-equals accepted", func(t *testing.T) {
		r, err := Parse("== 2.0.0")
		require.NoError(t, err)
		assert.Equal(t, OpEq, r.Constraints[0].Operator)
	})

	t.Run("empty string rejected", func(t *testing.T) {
		_, err := Parse("")
		require.Error(t, err)
	})

	t.Run("missing operator rejected", func(t *testing.T) {
		_, err := Parse("1.0.0")
		require.Error(t, err)
	})

	t.Run("invalid version rejected", func(t *testing.T) {
		_, err := Parse(">= not-a-version")
		require.Error(t, err)
	})

	t.Run("empty term rejected", func(t *testing.T) {
		_, err := Parse(">= 1.0.0, ")
		require.Error(t, err)
	})

	t.Run("operator with no version rejected", func(t *testing.T) {
		_, err := Parse(">=")
		require.Error(t, err)
	})
}

func TestVersionRangeIsUnbounded(t *testing.T) {
	t.Run("only lower bound is unbounded", func(t *testing.T) {
		r, err := Parse(">= 1.0.0")
		require.NoError(t, err)
		assert.True(t, r.IsUnbounded())
	})

	t.Run("upper bound makes it bounded", func(t *testing.T) {
		r, err := Parse(">= 1.0.0, <= 2.0.0")
		require.NoError(t, err)
		assert.False(t, r.IsUnbounded())
	})

	t.Run("exact version is bounded", func(t *testing.T) {
		r, err := Parse("= 1.0.0")
		require.NoError(t, err)
		assert.False(t, r.IsUnbounded())
	})
}

func TestVersionRangeString(t *testing.T) {
	r, err := Parse(">=1.0.0,<1.2.3")
	require.NoError(t, err)
	assert.Equal(t, ">= 1.0.0, < 1.2.3", r.String())
}
