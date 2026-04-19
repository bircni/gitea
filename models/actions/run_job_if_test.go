// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasIfCondition(t *testing.T) {
	t.Run("invalid payload returns false", func(t *testing.T) {
		job := &ActionRunJob{WorkflowPayload: []byte("not: valid: yaml: [")}
		assert.False(t, job.HasIfCondition())
	})
	t.Run("payload without if returns false", func(t *testing.T) {
		job := &ActionRunJob{WorkflowPayload: []byte(`
name: test
on: push
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - run: echo
`)}
		assert.False(t, job.HasIfCondition())
	})
	t.Run("payload with if returns true", func(t *testing.T) {
		job := &ActionRunJob{WorkflowPayload: []byte(`
name: test
on: push
jobs:
  j:
    runs-on: ubuntu-latest
    if: ${{ always() }}
    steps:
      - run: echo
`)}
		assert.True(t, job.HasIfCondition())
	})
}
