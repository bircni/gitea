// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignOut(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("NormalLogout", func(t *testing.T) {
		session := loginUser(t, "user2")

		req := NewRequest(t, "GET", "/user/logout")
		resp := session.MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", resp.Header().Get("Location"))

		// logged out, try to view a private repo, should fail
		req = NewRequest(t, "GET", "/user2/repo2")
		session.MakeRequest(t, req, http.StatusNotFound)
	})

	t.Run("PartialSignOutSwitchesToNextAccount", func(t *testing.T) {
		session := loginUser(t, "user2")
		addAccount(t, session, "user4", false)

		req := NewRequest(t, "POST", "/user/accounts/logout")
		resp := session.MakeRequest(t, req, http.StatusOK)
		assert.JSONEq(t, `{"redirect":"/"}`, resp.Body.String())

		assertSignedInAs(t, session, "user2")
		session.MakeRequest(t, NewRequest(t, "GET", "/user2/repo2"), http.StatusOK)
	})

	t.Run("SignOutDestroysSessionWithSeveralAccounts", func(t *testing.T) {
		session := loginUser(t, "user2")
		addAccount(t, session, "user4", false)

		req := NewRequest(t, "GET", "/user/logout")
		resp := session.MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", resp.Header().Get("Location"))

		session.MakeRequest(t, NewRequest(t, "GET", "/user2/repo2"), http.StatusNotFound)
	})

	t.Run("PartialSignOutRevokesOnlyTheDepartingRememberToken", func(t *testing.T) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user4"})

		session := loginUserRemembered(t, "user2")
		addAccount(t, session, "user4", true)
		addAccount(t, session, "user2", false)
		require.Equal(t, 1, unittest.GetCount(t, &auth_model.AuthToken{UserID: user2.ID}))

		session.MakeRequest(t, NewRequest(t, "POST", "/user/accounts/logout"), http.StatusOK)

		assert.Equal(t, 0, unittest.GetCount(t, &auth_model.AuthToken{UserID: user2.ID}))
		assert.Equal(t, 1, unittest.GetCount(t, &auth_model.AuthToken{UserID: user4.ID}))
		assert.Len(t, strings.Split(session.GetRawCookie(setting.CookieRememberName).Value, ","), 1)
		assertSignedInAs(t, session, "user4")
	})

	t.Run("PartialSignOutSkipsReverseProxyLogoutRedirect", func(t *testing.T) {
		session := loginUser(t, "user2")
		addAccount(t, session, "user4", false)

		defer test.MockVariableValue(&setting.Service.EnableReverseProxyAuth, true)()
		defer test.MockVariableValue(&setting.ReverseProxyLogoutRedirect, "/my-sso/logout")()

		req := NewRequest(t, "POST", "/user/accounts/logout")
		resp := session.MakeRequest(t, req, http.StatusOK)
		assert.JSONEq(t, `{"redirect":"/"}`, resp.Body.String())
	})

	t.Run("ReverseProxyLogoutRedirect", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.EnableReverseProxyAuth, true)()
		defer test.MockVariableValue(&setting.ReverseProxyLogoutRedirect, "/my-sso/logout?return_to=/my-sso/home")()

		session := loginUser(t, "user2")
		req := NewRequest(t, "GET", "/user/logout")
		resp := session.MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/my-sso/logout?return_to=/my-sso/home", resp.Header().Get("Location"))

		// logged out, try to view a private repo, should fail
		req = NewRequest(t, "GET", "/user2/repo2")
		session.MakeRequest(t, req, http.StatusNotFound)
	})
}
