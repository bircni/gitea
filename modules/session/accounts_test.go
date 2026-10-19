// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package session

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitea.dev/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignedInAccounts(t *testing.T) {
	backend := newMemoryBackend(3600)
	newSession := func(t *testing.T, sid string) *store {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if sid != "" {
			req.AddCookie(&http.Cookie{Name: setting.SessionConfig.CookieName, Value: sid})
		}
		sess, err := startSession(backend, httptest.NewRecorder(), req)
		require.NoError(t, err)
		return sess
	}

	t.Run("AddMovesToFrontAndDedups", func(t *testing.T) {
		sess := newSession(t, "")
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 1}))
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 2}))
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 1, SignInMethod: SignInMethodOAuth2}))
		assert.Equal(t, []SignedInAccount{{UID: 1, SignInMethod: SignInMethodOAuth2}, {UID: 2}}, GetSignedInAccounts(sess))
	})

	t.Run("CapEvictsOldest", func(t *testing.T) {
		sess := newSession(t, "")
		for i := range int64(MaxSignedInAccounts + 2) {
			require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: i + 1}))
		}
		accounts := GetSignedInAccounts(sess)
		require.Len(t, accounts, MaxSignedInAccounts)
		assert.Equal(t, int64(MaxSignedInAccounts+2), accounts[0].UID)
		_, ok := FindSignedInAccount(accounts, 1)
		assert.False(t, ok)
	})

	t.Run("Remove", func(t *testing.T) {
		sess := newSession(t, "")
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 1}))
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 2}))

		remaining, err := RemoveSignedInAccount(sess, 2)
		require.NoError(t, err)
		assert.Equal(t, []SignedInAccount{{UID: 1}}, remaining)

		remaining, err = RemoveSignedInAccount(sess, 1)
		require.NoError(t, err)
		assert.Empty(t, remaining)
		assert.Nil(t, sess.Get(KeySignedInAccounts))
	})

	t.Run("SurvivesSessionStorage", func(t *testing.T) {
		sess := newSession(t, "")
		require.NoError(t, AddSignedInAccount(sess, SignedInAccount{UID: 1, SignInMethod: SignInMethodOAuth2}))
		require.NoError(t, StartAddingAccount(sess, 1))
		require.NoError(t, sess.Release())

		reloaded := newSession(t, sess.ID())
		assert.Equal(t, []SignedInAccount{{UID: 1, SignInMethod: SignInMethodOAuth2}}, GetSignedInAccounts(reloaded))
		assert.EqualValues(t, 1, AddingAccountFor(reloaded))
	})
}
