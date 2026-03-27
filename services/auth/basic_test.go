// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsLikelyTokenString(t *testing.T) {
	assert.True(t, isLikelyTokenString("d2c6c1ba3890b309189a8e618c72a162e4efbf36"))
	assert.False(t, isLikelyTokenString(""))
	assert.False(t, isLikelyTokenString("not-a-token"))
	assert.False(t, isLikelyTokenString("D2C6C1BA3890B309189A8E618C72A162E4EFBF36"))
	assert.False(t, isLikelyTokenString("d2c6c1ba3890b309189a8e618c72a162e4efbf3g"))
}
