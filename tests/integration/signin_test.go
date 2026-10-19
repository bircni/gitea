// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/models/db"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/test"
	"gitea.dev/modules/translation"
	"gitea.dev/modules/web"
	"gitea.dev/modules/web/middleware"
	"gitea.dev/routers"
	"gitea.dev/routers/web/auth"
	"gitea.dev/services/auth/source/oauth2"
	"gitea.dev/services/context"
	"gitea.dev/tests"

	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLoginFailed(t *testing.T, username, password, message string) {
	session := emptyTestSession(t)
	req := NewRequestWithValues(t, "POST", "/user/login", map[string]string{
		"user_name": username,
		"password":  password,
	})
	resp := session.MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	resultMsg := strings.TrimSpace(htmlDoc.doc.Find(".ui.message.flash-message").Text())
	assert.Equal(t, message, resultMsg)
}

func TestSignin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// add new user with user2's email
	user.Name = "testuser"
	user.LowerName = strings.ToLower(user.Name)
	user.ID = 0
	require.NoError(t, db.Insert(t.Context(), user))

	samples := []struct {
		username string
		password string
		message  string
	}{
		{username: "wrongUsername", password: "wrongPassword", message: translation.NewLocale("en-US").TrString("form.username_password_incorrect")},
		{username: "wrongUsername", password: "password", message: translation.NewLocale("en-US").TrString("form.username_password_incorrect")},
		{username: "user15", password: "wrongPassword", message: translation.NewLocale("en-US").TrString("form.username_password_incorrect")},
		{username: "user1@example.com", password: "wrongPassword", message: translation.NewLocale("en-US").TrString("form.username_password_incorrect")},
	}

	for _, s := range samples {
		testLoginFailed(t, s.username, s.password, s.message)
	}
}

func TestSigninWithRememberMe(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	baseURL, _ := url.Parse(setting.AppURL)

	session := emptyTestSession(t)
	req := NewRequestWithValues(t, "POST", "/user/login", map[string]string{
		"user_name": user.Name,
		"password":  userPassword,
		"remember":  "on",
	})
	session.MakeRequest(t, req, http.StatusSeeOther)

	c := session.GetRawCookie(setting.CookieRememberName)
	assert.NotNil(t, c)

	session = emptyTestSession(t)

	// Without session the settings page should not be reachable
	req = NewRequest(t, "GET", "/user/settings")
	session.MakeRequest(t, req, http.StatusSeeOther)

	req = NewRequest(t, "GET", "/user/login")
	// Set the remember me cookie for the login GET request
	session.jar.SetCookies(baseURL, []*http.Cookie{c})
	session.MakeRequest(t, req, http.StatusSeeOther)

	// With session the settings page should be reachable
	req = NewRequest(t, "GET", "/user/settings")
	session.MakeRequest(t, req, http.StatusOK)
}

func TestEnablePasswordSignInFormAndEnablePasskeyAuth(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	mockLinkAccount := func(ctx *context.Context) {
		authSource := auth_model.Source{ID: 1}
		gothUser := goth.User{Email: "invalid-email", Name: "."}
		_ = auth.Oauth2SetLinkAccountData(ctx, auth.LinkAccountData{AuthSourceID: authSource.ID, GothUser: gothUser})
	}

	t.Run("EnablePasswordSignInForm=false", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.Service.EnablePasswordSignInForm, false)()

		req := NewRequest(t, "GET", "/user/login")
		resp := MakeRequest(t, req, http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, "form[action='/user/login']", false)

		req = NewRequest(t, "POST", "/user/login")
		MakeRequest(t, req, http.StatusForbidden)

		req = NewRequest(t, "GET", "/user/link_account")
		defer web.RouteMockReset()
		web.RouteMock(web.MockAfterMiddlewares, mockLinkAccount)
		resp = MakeRequest(t, req, http.StatusOK)
		doc = NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, "form[action='/user/link_account_signin']", false)
	})

	t.Run("EnablePasswordSignInForm=true", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.Service.EnablePasswordSignInForm, true)()

		req := NewRequest(t, "GET", "/user/login")
		resp := MakeRequest(t, req, http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, "form[action='/user/login']", true)

		req = NewRequest(t, "POST", "/user/login")
		MakeRequest(t, req, http.StatusOK)

		req = NewRequest(t, "GET", "/user/link_account")
		defer web.RouteMockReset()
		web.RouteMock(web.MockAfterMiddlewares, mockLinkAccount)
		resp = MakeRequest(t, req, http.StatusOK)
		doc = NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, "form[action='/user/link_account_signin']", true)
	})

	t.Run("EnablePasskeyAuth=false", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.Service.EnablePasskeyAuth, false)()

		req := NewRequest(t, "GET", "/user/login")
		resp := MakeRequest(t, req, http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, ".signin-passkey", false)
	})

	t.Run("EnablePasskeyAuth=true", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.Service.EnablePasskeyAuth, true)()

		req := NewRequest(t, "GET", "/user/login")
		resp := MakeRequest(t, req, http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		AssertHTMLElement(t, doc, ".signin-passkey", true)
	})
}

func TestRequireSignInView(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	t.Run("NoRequireSignInView", func(t *testing.T) {
		require.False(t, setting.Service.RequireSignInViewStrict)
		require.False(t, setting.Service.BlockAnonymousAccessExpensive)
		req := NewRequest(t, "GET", "/user2/repo1/src/branch/master")
		MakeRequest(t, req, http.StatusOK)
		req = NewRequest(t, "GET", "/-/ws")
		MakeRequest(t, req, http.StatusUpgradeRequired)
	})
	t.Run("RequireSignInView", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.RequireSignInViewStrict, true)()
		defer test.MockVariableValue(&testWebRoutes, routers.NormalRoutes())()
		t.Run("AccessPublicRepo", func(t *testing.T) {
			req := NewRequest(t, "GET", "/user2/repo1/src/branch/master")
			resp := MakeRequest(t, req, http.StatusSeeOther)
			assert.Equal(t, "/user/login?redirect_to=%2Fuser2%2Frepo1%2Fsrc%2Fbranch%2Fmaster", resp.Header().Get("Location"))
		})
		t.Run("UpdateTheme", func(t *testing.T) {
			session := emptyTestSession(t)
			req := NewRequest(t, "POST", "/-/web-theme/apply?theme=gitea-dark")
			session.MakeRequest(t, req, http.StatusOK)
			assert.Equal(t, "gitea-dark", session.GetSiteCookie(middleware.CookieTheme))
		})
	})
	t.Run("BlockAnonymousAccessExpensive", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.RequireSignInViewStrict, false)()
		defer test.MockVariableValue(&setting.Service.BlockAnonymousAccessExpensive, true)()
		defer test.MockVariableValue(&testWebRoutes, routers.NormalRoutes())()

		req := NewRequest(t, "GET", "/user2/repo1")
		MakeRequest(t, req, http.StatusOK)
		req = NewRequest(t, "GET", "/-/ws")
		MakeRequest(t, req, http.StatusSeeOther)

		req = NewRequest(t, "GET", "/user2/repo1/src/branch/master")
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/user/login?redirect_to=%2Fuser2%2Frepo1%2Fsrc%2Fbranch%2Fmaster", resp.Header().Get("Location"))
	})
}

func assertSignedInAs(t *testing.T, session *TestSession, name string) {
	t.Helper()
	req := NewRequest(t, "GET", "/")
	resp := session.MakeRequest(t, req, http.StatusOK)
	assert.Equal(t, name, NewHTMLParser(t, resp.Body).Find("[data-signed-in-username]").AttrOr("data-signed-in-username", ""))
}

func loginUserRemembered(t *testing.T, name string) *TestSession {
	t.Helper()
	session := emptyTestSession(t)
	session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/login", map[string]string{
		"user_name": name, "password": userPassword, "remember": "on",
	}), http.StatusSeeOther)
	return session
}

func addAccount(t *testing.T, session *TestSession, name string, remember bool) {
	t.Helper()
	session.MakeRequest(t, NewRequest(t, "POST", "/user/accounts/add"), http.StatusOK)
	session.MakeRequest(t, NewRequest(t, "GET", "/user/login"), http.StatusOK)
	values := map[string]string{"user_name": name, "password": userPassword}
	if remember {
		values["remember"] = "on"
	}
	session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/login", values), http.StatusSeeOther)
}

func TestMultiAccountSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user4"})

	t.Run("AddAndSwitch", func(t *testing.T) {
		session := loginUser(t, "user2")
		addAccount(t, session, "user4", false)
		assertSignedInAs(t, session, "user4")

		req := NewRequestf(t, "POST", "/user/accounts/switch/%d", user2.ID)
		session.MakeRequest(t, req, http.StatusOK)
		assertSignedInAs(t, session, "user2")
	})

	t.Run("SwitchRejectsUnknownUID", func(t *testing.T) {
		session := loginUser(t, "user2")
		req := NewRequestf(t, "POST", "/user/accounts/switch/%d", user4.ID)
		session.MakeRequest(t, req, http.StatusBadRequest)
	})

	t.Run("SwitchRejectsProhibitedUser", func(t *testing.T) {
		session := loginUser(t, "user2")
		addAccount(t, session, "user4", false)

		defer func() {
			require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: user2.ID, ProhibitLogin: false}, "prohibit_login"))
		}()
		require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: user2.ID, ProhibitLogin: true}, "prohibit_login"))

		req := NewRequestf(t, "POST", "/user/accounts/switch/%d", user2.ID)
		session.MakeRequest(t, req, http.StatusOK)
		assertSignedInAs(t, session, "user4")

		// the unusable account is pruned, so a second attempt is rejected outright
		session.MakeRequest(t, NewRequestf(t, "POST", "/user/accounts/switch/%d", user2.ID), http.StatusBadRequest)
	})

	t.Run("AddViaOAuth2DoesNotLinkToCurrentAccount", func(t *testing.T) {
		mockServer := createOAuth2MockProvider()
		defer mockServer.Close()
		addOAuth2Source(t, "test-add-account-source", oauth2.Source{
			Provider:                      "openidConnect",
			ClientID:                      "test-client-id",
			OpenIDConnectAutoDiscoveryURL: mockServer.URL + "/.well-known/openid-configuration",
		})
		defer test.MockVariableValue(&setting.OAuth2Client.Username, "")()
		defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()

		session := loginUser(t, "user2")
		session.MakeRequest(t, NewRequest(t, "POST", "/user/accounts/add"), http.StatusOK)

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/oauth2/test-add-account-source"), http.StatusTemporaryRedirect)
		assert.Contains(t, resp.Header().Get("Location"), "prompt=select_account")

		defer test.MockVariableValue(&gothic.CompleteUserAuth, func(http.ResponseWriter, *http.Request) (goth.User, error) {
			return goth.User{Provider: "openidConnect", UserID: "add-account-userid", Email: "add-account@example.com"}, nil
		})()
		session.MakeRequest(t, NewRequest(t, "GET", "/user/oauth2/test-add-account-source/callback?code=XYZ&state=XYZ"), http.StatusSeeOther)

		unittest.AssertCount(t, &user_model.ExternalLoginUser{UserID: user2.ID}, 0)
		assertSignedInAs(t, session, "add-account-userid")
	})

	t.Run("ReverseProxyCannotAddOrSwitch", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.EnableReverseProxyAuth, true)()
		session := emptyTestSession(t)
		for _, req := range []*RequestWrapper{NewRequest(t, "POST", "/user/accounts/add"), NewRequestf(t, "POST", "/user/accounts/switch/%d", user4.ID)} {
			req.Header.Set(setting.ReverseProxyAuthUser, "user2")
			session.MakeRequest(t, req, http.StatusForbidden)
		}
	})
}

func TestSignInRemembered(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user4"})

	// remember two accounts on one device
	session := loginUserRemembered(t, "user2")
	addAccount(t, session, "user4", true)

	// drop the session but keep the remember-me cookie, as closing the browser would
	remembered := emptyTestSession(t)
	baseURL, err := url.Parse(setting.AppURL)
	require.NoError(t, err)
	remembered.jar.SetCookies(baseURL, []*http.Cookie{session.GetRawCookie(setting.CookieRememberName)})
	session = remembered

	t.Run("OffersBothAccounts", func(t *testing.T) {
		resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/login"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)
		assert.Equal(t, 2, htmlDoc.doc.Find("[data-url*='/user/login/remembered/']").Length())
	})

	t.Run("SignsInWithoutPassword", func(t *testing.T) {
		req := NewRequestf(t, "POST", "/user/login/remembered/%d", user4.ID)
		session.MakeRequest(t, req, http.StatusOK)
		assertSignedInAs(t, session, "user4")

		// the other remembered account only joins the session once it is picked while adding an account
		session.MakeRequest(t, NewRequestf(t, "POST", "/user/accounts/switch/%d", user2.ID), http.StatusBadRequest)
		session.MakeRequest(t, NewRequest(t, "POST", "/user/accounts/add"), http.StatusOK)
		resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/login"), http.StatusOK)
		assert.Equal(t, 1, NewHTMLParser(t, resp.Body).Find("[data-url*='/user/login/remembered/']").Length())
		session.MakeRequest(t, NewRequestf(t, "POST", "/user/login/remembered/%d", user2.ID), http.StatusOK)
		assertSignedInAs(t, session, "user2")
		session.MakeRequest(t, NewRequestf(t, "POST", "/user/accounts/switch/%d", user4.ID), http.StatusOK)
		assertSignedInAs(t, session, "user4")
	})

	t.Run("RejectsAccountNotRemembered", func(t *testing.T) {
		other := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user5"})
		fresh := emptyTestSession(t)
		req := NewRequestf(t, "POST", "/user/login/remembered/%d", other.ID)
		fresh.MakeRequest(t, req, http.StatusOK)
		assertSignedInAs(t, fresh, "")
	})
}
