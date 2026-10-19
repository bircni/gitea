// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	audit_model "gitea.dev/models/audit"
	"gitea.dev/models/auth"
	"gitea.dev/models/db"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/auth/password"
	"gitea.dev/modules/container"
	"gitea.dev/modules/httplib"
	"gitea.dev/modules/imagecaptcha"
	"gitea.dev/modules/log"
	"gitea.dev/modules/optional"
	"gitea.dev/modules/session"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/templates"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
	"gitea.dev/modules/web"
	"gitea.dev/modules/web/middleware"
	"gitea.dev/services/audit"
	auth_service "gitea.dev/services/auth"
	"gitea.dev/services/auth/source/oauth2"
	"gitea.dev/services/context"
	"gitea.dev/services/externalaccount"
	"gitea.dev/services/forms"
	"gitea.dev/services/mailer"
	user_service "gitea.dev/services/user"
	websocket_service "gitea.dev/services/websocket"

	"github.com/markbates/goth"
)

const (
	tplSignIn         templates.TplName = "user/auth/signin"          // for sign in page
	tplSignUp         templates.TplName = "user/auth/signup"          // for sign up page
	TplActivate       templates.TplName = "user/auth/activate"        // for activate user
	TplActivatePrompt templates.TplName = "user/auth/activate_prompt" // for showing a message for user activation
)

type CommonAuthOptions struct {
	EnableCaptcha bool
}

func prepareCommonAuthPageData(ctx *context.Context, opt CommonAuthOptions) {
	ctx.Data["EnablePasswordSignInForm"] = setting.Service.EnablePasswordSignInForm
	ctx.Data["EnablePasskeyAuth"] = setting.Service.EnablePasskeyAuth

	// for OpenID Connect
	ctx.Data["EnableOpenIDSignUp"] = setting.Service.EnableOpenIDSignUp
	ctx.Data["AllowOnlyInternalRegistration"] = setting.Service.AllowOnlyInternalRegistration

	if opt.EnableCaptcha {
		ctx.Data["EnableCaptcha"] = true
		ctx.Data["RecaptchaAPIScriptURL"] = strings.TrimSuffix(setting.Service.RecaptchaURL, "/") + "/api.js"
		ctx.Data["CaptchaType"] = setting.Service.CaptchaType
		ctx.Data["RecaptchaSitekey"] = setting.Service.RecaptchaSitekey
		ctx.Data["HcaptchaSitekey"] = setting.Service.HcaptchaSitekey
		ctx.Data["McaptchaSitekey"] = setting.Service.McaptchaSitekey
		ctx.Data["McaptchaURL"] = strings.TrimSuffix(setting.Service.McaptchaURL, "/")
		ctx.Data["CfTurnstileSitekey"] = setting.Service.CfTurnstileSitekey
		if setting.Service.CaptchaType == setting.ImageCaptcha {
			ctx.Data["CreateImageCaptcha"] = imagecaptcha.CreateNew
		}
	}
}

// rememberedAccount is an account whose remember-me token is still valid on this device.
type rememberedAccount struct {
	token *auth.AuthToken
	user  *user_model.User
}

type autoSignInResult struct {
	signedIn    bool
	choices     []*user_model.User // several accounts are remembered, the user picks one
	compromised bool
}

// checkRememberedAccounts resolves the remember-me cookie without rotating its tokens.
func checkRememberedAccounts(ctx *context.Context) ([]rememberedAccount, bool, error) {
	tokens, compromised, err := auth_service.CheckAuthTokens(ctx, ctx.GetSiteCookie(setting.CookieRememberName))
	if err != nil {
		return nil, compromised, err
	}

	users, err := user_model.GetUsersMapByIDs(ctx, container.FilterSlice(tokens, func(t *auth.AuthToken) (int64, bool) { return t.UserID, true }))
	if err != nil {
		return nil, false, err
	}
	var accounts []rememberedAccount
	for _, t := range tokens {
		if u, ok := users[t.UserID]; ok && context.CanUseSignedInAccount(u) {
			accounts = append(accounts, rememberedAccount{token: t, user: u})
		}
	}
	return accounts, compromised, nil
}

// signInRemembered rotates only the chosen token, so a tab holding the old cookie can't revoke the others.
// Only the chosen account joins the session, the others stay in the cookie to be picked later.
func signInRemembered(ctx *context.Context, accounts []rememberedAccount, chosen rememberedAccount) error {
	nt, token, err := auth_service.RegenerateAuthToken(ctx, chosen.token)
	if err != nil {
		return err
	}
	var parts, dropped []string
	for _, part := range rememberCookieParts(ctx) {
		id, _, _ := strings.Cut(part, ":")
		if id == chosen.token.ID {
			parts = append(parts, nt.ID+":"+token)
		} else if slices.ContainsFunc(accounts, func(a rememberedAccount) bool { return a.token.ID == id }) {
			parts = append(parts, part)
		} else {
			dropped = append(dropped, part)
		}
	}
	deleteRememberTokens(ctx, dropped) // nothing could revoke them once out of the cookie
	setRememberCookie(ctx, parts)

	hasTwoFactorAuth, err := auth.HasTwoFactorOrWebAuthn(ctx, chosen.user.ID)
	if err != nil {
		return fmt.Errorf("signInRemembered: %w", err)
	}
	if err := regenerateSession(ctx, map[string]any{
		session.KeyUID:                  chosen.user.ID,
		session.KeyUserHasTwoFactorAuth: hasTwoFactorAuth,
	}); err != nil {
		return fmt.Errorf("signInRemembered: %w", err)
	}
	if err := resetLocale(ctx, chosen.user); err != nil {
		log.Error("Failed to reset locale for user %d: %v", chosen.user.ID, err)
	}
	return nil
}

// autoSignIn reads cookie and try to auto-login.
func autoSignIn(ctx *context.Context) (autoSignInResult, error) {
	if ctx.GetSiteCookie(setting.CookieRememberName) == "" {
		return autoSignInResult{}, nil
	}
	if err := auth.DeleteExpiredAuthTokens(ctx); err != nil {
		log.Error("Failed to delete expired auth tokens: %v", err)
	}

	accounts, compromised, err := checkRememberedAccounts(ctx)
	res := autoSignInResult{compromised: compromised}
	switch {
	case errors.Is(err, auth_service.ErrAuthTokenStale): // a concurrent request is rewriting the cookie, acting on it would undo that
		return res, nil
	case err != nil:
		return res, err
	case len(accounts) == 0:
		deleteRememberTokens(ctx, rememberCookieParts(ctx)) // e.g. of disabled users, nothing could revoke them once out of the cookie
		setRememberCookie(ctx, nil)
	case len(accounts) == 1:
		if err := signInRemembered(ctx, accounts, accounts[0]); errors.Is(err, auth_service.ErrAuthTokenStale) {
			return res, nil
		} else if err != nil {
			return res, err
		}
		res.signedIn = true
	default:
		for _, acc := range accounts {
			res.choices = append(res.choices, acc.user)
		}
	}
	return res, nil
}

// rememberCookieParts returns the "<id>:<token>" pairs currently held by the remember-me cookie.
func rememberCookieParts(ctx *context.Context) []string {
	value := ctx.GetSiteCookie(setting.CookieRememberName)
	if value == "" {
		return nil
	}
	parts := strings.SplitN(value, ",", session.MaxSignedInAccounts+1)
	return parts[:min(len(parts), session.MaxSignedInAccounts)] // never written longer, don't query a crafted one
}

func setRememberCookie(ctx *context.Context, parts []string) {
	if len(parts) == 0 {
		ctx.DeleteSiteCookie(setting.CookieRememberName)
		return
	}
	n := min(len(parts), session.MaxSignedInAccounts)
	deleteRememberTokens(ctx, parts[n:]) // nothing could revoke them once out of the cookie
	ctx.SetSiteCookie(setting.CookieRememberName, strings.Join(parts[:n], ","), setting.LogInRememberDays*timeutil.Day)
}

func deleteRememberTokens(ctx *context.Context, parts []string) {
	for _, part := range parts {
		id, _, _ := strings.Cut(part, ":")
		if err := auth.DeleteAuthTokenByID(ctx, id); err != nil {
			log.Error("DeleteAuthTokenByID: %v", err)
		}
	}
}

func resetLocale(ctx *context.Context, u *user_model.User) error {
	// Language setting of the user overwrites the one previously set
	// If the user does not have a locale set, we save the current one.
	if u.Language == "" {
		opts := &user_service.UpdateOptions{
			Language: optional.Some(ctx.Locale.Language()),
		}
		if err := user_service.UpdateUser(ctx, u, opts); err != nil {
			return err
		}
	}

	middleware.SetLocaleCookie(ctx.Resp, u.Language, 0)

	if ctx.Locale.Language() != u.Language {
		ctx.Locale = middleware.Locale(ctx.Resp, ctx.Req)
	}

	return nil
}

func rememberAuthRedirectLink(ctx *context.Context) {
	redirectTo := ctx.FormString("redirect_to")
	if redirectTo == "" {
		if ref, err := url.Parse(ctx.Req.Referer()); err == nil && httplib.IsCurrentGiteaSiteURL(ctx, ctx.Req.Referer()) {
			// the request paths starting with "/user/" are either:
			// * auth related pages: don't redirect back to them
			// * user settings pages: they have "require sign-in" protection already, no "referer redirect" would happen
			skipRefererRedirect := strings.HasPrefix(ref.Path, setting.AppSubURL+"/user/")
			if !skipRefererRedirect {
				redirectTo = ref.RequestURI()
			}
		}
	}
	if redirectTo != "" {
		middleware.SetRedirectToCookie(ctx.Resp, redirectTo)
	}
}

func consumeAuthRedirectLink(ctx *context.Context) string {
	redirects := []string{ctx.FormString("redirect_to"), middleware.GetRedirectToCookie(ctx.Req)}
	middleware.DeleteRedirectToCookie(ctx.Resp)
	if setting.LandingPageURL == setting.LandingPageLogin {
		redirects = append(redirects, setting.AppSubURL+"/") // do not cycle-redirect to the login page
	} else {
		redirects = append(redirects, setting.AppSubURL+string(setting.LandingPageURL))
	}
	for _, link := range redirects {
		if link != "" && httplib.IsCurrentGiteaSiteURL(ctx, link) {
			return link
		}
	}
	return setting.AppSubURL + "/"
}

func authRedirectLink(ctx *context.Context) string {
	if setting.Config().Instance.MaintenanceMode.Value(ctx).IsActive() {
		// in maintenance mode, redirect to admin dashboard, it is the only accessible page
		return setting.AppSubURL + "/-/admin"
	}
	return consumeAuthRedirectLink(ctx)
}

func redirectAfterAuth(ctx *context.Context) {
	ctx.RedirectToCurrentSite(authRedirectLink(ctx))
}

func performAutoLogin(ctx *context.Context) bool {
	if ctx.IsSigned { // a remembered token could belong to the active account, so never pick one
		return ctx.DoerIsAddingAccount() && offerRememberedAccountsToAdd(ctx)
	}
	rememberAuthRedirectLink(ctx)

	res, err := autoSignIn(ctx) // try to auto-login
	if err != nil {
		ctx.ServerError("autoSignIn", err)
		return true
	}
	if res.compromised {
		ctx.Flash.Error(ctx.Tr("auth.remember_me.compromised"), true)
	}
	if len(res.choices) > 0 {
		ctx.Data["RememberedAccounts"] = res.choices
		if ctx.Session.Get(session.KeyRememberedAccountsOffered) == nil {
			if err := ctx.Session.Set(session.KeyRememberedAccountsOffered, true); err != nil {
				log.Error("Session.Set: %v", err)
			}
		}
	}

	if res.signedIn {
		redirectAfterAuth(ctx)
		return true
	}

	return false
}

// offerRememberedAccountsToAdd lists the remembered accounts this session doesn't hold yet, it reports whether it answered the request
func offerRememberedAccountsToAdd(ctx *context.Context) bool {
	accounts, _, err := checkRememberedAccounts(ctx)
	if errors.Is(err, auth_service.ErrAuthTokenStale) {
		return false
	} else if err != nil {
		ctx.ServerError("checkRememberedAccounts", err)
		return true
	}
	signedIn := session.GetSignedInAccounts(ctx.Session)
	var choices []*user_model.User
	for _, acc := range accounts {
		if _, ok := session.FindSignedInAccount(signedIn, acc.user.ID); !ok {
			choices = append(choices, acc.user)
		}
	}
	ctx.Data["RememberedAccounts"] = choices
	return false
}

func performAutoLoginOAuth2(ctx *context.Context, data *preparedSignInData) bool {
	// If only 1 OAuth provider is present and other login methods are disabled, redirect to the OAuth provider.
	onlySingleOAuth2 := len(data.oauth2Providers) == 1 &&
		!setting.Service.EnablePasswordSignInForm &&
		!setting.Service.EnableOpenIDSignIn &&
		!setting.Service.EnablePasskeyAuth &&
		!data.enableSSPI

	if !onlySingleOAuth2 {
		return false
	}

	skipToOAuthURL := setting.AppSubURL + "/user/oauth2/" + url.PathEscape(data.oauth2Providers[0].DisplayName())
	if redirectTo := ctx.FormString("redirect_to"); redirectTo != "" {
		skipToOAuthURL += "?redirect_to=" + url.QueryEscape(redirectTo)
	}
	ctx.Redirect(skipToOAuthURL)
	return true
}

type preparedSignInData struct {
	oauth2Providers []oauth2.Provider
	enableSSPI      bool
}

func prepareSignInPageData(ctx *context.Context) (ret preparedSignInData) {
	var err error
	ret.enableSSPI = auth.IsSSPIEnabled(ctx)
	ret.oauth2Providers, err = oauth2.GetOAuth2Providers(ctx, optional.Some(true))
	if err != nil {
		log.Error("Failed to get OAuth2 providers: %v", err)
	}
	ctx.Data["Title"] = ctx.Tr("sign_in")
	ctx.Data["OAuth2Providers"] = ret.oauth2Providers
	ctx.Data["Title"] = ctx.Tr("sign_in")
	ctx.Data["SignInLink"] = setting.AppSubURL + "/user/login"
	ctx.Data["PageIsSignIn"] = true
	ctx.Data["PageIsLogin"] = true
	ctx.Data["EnableSSPI"] = ret.enableSSPI
	ctx.Data["IsAddingAccount"] = ctx.DoerIsAddingAccount()

	prepareCommonAuthPageData(ctx, CommonAuthOptions{
		EnableCaptcha: setting.Service.EnableCaptcha && setting.Service.RequireCaptchaForLogin,
	})
	return ret
}

// SignIn render sign in page
func SignIn(ctx *context.Context) {
	if performAutoLogin(ctx) {
		return
	}
	if ctx.IsSigned && !ctx.DoerIsAddingAccount() {
		redirectAfterAuth(ctx)
		return
	}
	data := prepareSignInPageData(ctx)
	if performAutoLoginOAuth2(ctx, &data) {
		return
	}
	ctx.HTML(http.StatusOK, tplSignIn)
}

// SignInRemembered signs in an account by its remember-me token in the caller's cookie, without a password
func SignInRemembered(ctx *context.Context) {
	if ctx.IsSigned && !ctx.DoerIsAddingAccount() { // the picker is only offered while signed out or adding an account
		ctx.HTTPError(http.StatusBadRequest)
		return
	}
	accounts, _, err := checkRememberedAccounts(ctx)
	if errors.Is(err, auth_service.ErrAuthTokenStale) {
		ctx.JSONRedirect(setting.AppSubURL + "/user/login") // the concurrent request's cookie decides there
		return
	} else if err != nil {
		ctx.ServerError("checkRememberedAccounts", err)
		return
	}
	uid := ctx.PathParamInt64("uid")
	i := slices.IndexFunc(accounts, func(a rememberedAccount) bool { return a.user.ID == uid })
	if i < 0 {
		ctx.Flash.Error(ctx.Tr("auth.remembered_account_unavailable"))
		ctx.JSONRedirect(setting.AppSubURL + "/user/login")
		return
	}
	if err := signInRemembered(ctx, accounts, accounts[i]); errors.Is(err, auth_service.ErrAuthTokenStale) {
		ctx.JSONRedirect(setting.AppSubURL + "/user/login")
		return
	} else if err != nil {
		ctx.ServerError("signInRemembered", err)
		return
	}
	ctx.JSONRedirect(authRedirectLink(ctx))
}

// SignInPost response for sign in request
func SignInPost(ctx *context.Context) {
	if !setting.Service.EnablePasswordSignInForm {
		ctx.HTTPError(http.StatusForbidden)
		return
	}

	prepareSignInPageData(ctx)
	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplSignIn)
		return
	}

	form := web.GetForm[*forms.SignInForm](ctx)

	if setting.Service.EnableCaptcha && setting.Service.RequireCaptchaForLogin {
		if !context.VerifyCaptcha(ctx, tplSignIn, form) {
			return
		}
	}

	u, source, err := auth_service.UserSignIn(ctx, form.UserName, form.Password)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) || errors.Is(err, util.ErrInvalidArgument) {
			ctx.RenderWithErrDeprecated(ctx.Tr("form.username_password_incorrect"), tplSignIn, &form)
			log.Warn("Failed authentication attempt for %s from %s: %v", form.UserName, ctx.RemoteAddr(), err)
		} else if user_model.IsErrEmailAlreadyUsed(err) {
			ctx.RenderWithErrDeprecated(ctx.Tr("form.email_been_used"), tplSignIn, &form)
			log.Warn("Failed authentication attempt for %s from %s: %v", form.UserName, ctx.RemoteAddr(), err)
		} else if user_model.IsErrUserProhibitLogin(err) {
			log.Warn("Failed authentication attempt for %s from %s: %v", form.UserName, ctx.RemoteAddr(), err)
			ctx.Data["Title"] = ctx.Tr("auth.prohibit_login")
			ctx.HTML(http.StatusOK, "user/auth/prohibit_login")
		} else {
			ctx.ServerError("UserSignIn", err)
		}
		return
	}

	// Now handle 2FA:
	// First of all if the source can skip local two fa we're done
	if source.TwoFactorShouldSkip() {
		handleSignIn(ctx, u, form.Remember)
		return
	}

	// If this user is enrolled in 2FA TOTP, we can't sign the user in just yet.
	// Instead, redirect them to the 2FA authentication page.
	hasTwoFactor, err := auth.HasTwoFactorOrWebAuthn(ctx, u.ID)
	if err != nil {
		ctx.ServerError("HasTwoFactorOrWebAuthn", err)
		return
	}
	if !hasTwoFactor {
		handleSignIn(ctx, u, form.Remember)
		return
	}

	handleTwoFactorRequired(ctx, u, form.Remember, nil)
}

func handleTwoFactorRequired(ctx *context.Context, u *user_model.User, remember bool, extra map[string]any) {
	updates := map[string]any{"twofaUid": u.ID, "twofaRemember": remember}
	maps.Copy(updates, extra)
	if err := regenerateSession(ctx, updates); err != nil {
		ctx.ServerError("RegenerateSession", err)
		return
	}
	hasWebAuthn, err := auth.HasWebAuthnRegistrationsByUID(ctx, u.ID)
	if err != nil {
		ctx.ServerError("HasWebAuthnRegistrationsByUID", err)
		return
	}
	if hasWebAuthn {
		ctx.Redirect(setting.AppSubURL + "/user/webauthn")
		return
	}
	ctx.Redirect(setting.AppSubURL + "/user/two_factor")
}

// This handles the final part of the sign-in process of the user.
func handleSignIn(ctx *context.Context, u *user_model.User, remember bool) {
	handleSignInFull(ctx, u, remember)
	if ctx.Written() {
		return
	}
	redirectAfterAuth(ctx)
}

func handleSignInFull(ctx *context.Context, u *user_model.User, remember bool) {
	if remember {
		if err := auth.DeleteExpiredAuthTokens(ctx); err != nil {
			log.Error("Failed to delete expired auth tokens: %v", err)
		}
		nt, token, err := auth_service.CreateAuthTokenForUserID(ctx, u.ID)
		if err != nil {
			ctx.ServerError("CreateAuthTokenForUserID", err)
			return
		}

		others := revokeRememberTokens(ctx, u.ID)
		setRememberCookie(ctx, append([]string{nt.ID + ":" + token}, others...))
	}

	userHasTwoFactorAuth, err := auth.HasTwoFactorOrWebAuthn(ctx, u.ID)
	if err != nil {
		ctx.ServerError("HasTwoFactorOrWebAuthn", err)
		return
	}

	updates := map[string]any{
		session.KeyUID:                  u.ID,
		session.KeyUserHasTwoFactorAuth: userHasTwoFactorAuth,
	}
	if twofaUID, _ := ctx.Session.Get("twofaUid").(int64); twofaUID == u.ID {
		updates[session.KeySignInMethod], _ = ctx.Session.Get("twofaSignInMethod").(string)
	}
	auth_service.ClearSessionKeysForSignIn(ctx.Session)
	if err := regenerateSession(ctx, updates); err != nil {
		ctx.ServerError("RegenerateSession", err)
		return
	}

	// Language setting of the user overwrites the one previously set
	// If the user does not have a locale set, we save the current one.
	if u.Language == "" {
		opts := &user_service.UpdateOptions{
			Language: optional.Some(ctx.Locale.Language()),
		}
		if err := user_service.UpdateUser(ctx, u, opts); err != nil {
			ctx.ServerError("UpdateUser Language", fmt.Errorf("Error updating user language [user: %d, locale: %s]", u.ID, ctx.Locale.Language()))
			return
		}
	}

	middleware.SetLocaleCookie(ctx.Resp, u.Language, 0)

	if ctx.Locale.Language() != u.Language {
		ctx.Locale = middleware.Locale(ctx.Resp, ctx.Req)
	}

	// Register last login
	if err := user_service.UpdateUser(ctx, u, &user_service.UpdateOptions{SetLastLogin: true}); err != nil {
		ctx.ServerError("UpdateUser", err)
		return
	}
}

// extractUserNameFromOAuth2 tries to extract a normalized username from the given OAuth2 user.
// It returns ("", nil) if the required field doesn't exist.
func extractUserNameFromOAuth2(gothUser *goth.User) (string, error) {
	switch setting.OAuth2Client.Username {
	case setting.OAuth2UsernameEmail:
		return user_model.NormalizeUserName(gothUser.Email)
	case setting.OAuth2UsernamePreferredUsername:
		if preferredUsername, ok := gothUser.RawData["preferred_username"].(string); ok {
			return user_model.NormalizeUserName(preferredUsername)
		}
		return "", nil
	case setting.OAuth2UsernameNickname:
		return user_model.NormalizeUserName(gothUser.NickName)
	default: // OAuth2UsernameUserid
		return gothUser.UserID, nil
	}
}

// HandleSignOut resets the session and sets the cookies
func HandleSignOut(ctx *context.Context) {
	_ = ctx.Session.Flush()
	if err := ctx.Session.Destroy(ctx.Resp, ctx.Req); err != nil {
		log.Error("Unable to destroy session: %v", err)
	}
	ctx.DeleteSiteCookie(setting.CookieRememberName)
	middleware.DeleteRedirectToCookie(ctx.Resp)
}

// SignOut signs out every account of this session
func SignOut(ctx *context.Context) {
	if redirectTo := signOut(ctx, true); !ctx.Written() {
		ctx.Redirect(redirectTo)
	}
}

// SignOutCurrentAccount signs out the active account, handing the session over to another one if possible
func SignOutCurrentAccount(ctx *context.Context) {
	if redirectTo := signOut(ctx, false); !ctx.Written() {
		ctx.JSONRedirect(redirectTo)
	}
}

func signOut(ctx *context.Context, all bool) (redirectTo string) {
	if ctx.Doer != nil {
		websocket_service.PublishLogout(ctx.Doer.ID, ctx.Session.ID())
	}

	impersonator := audit.ImpersonatorFromContext(ctx)

	exitedImpersonated, err := auth_service.ExitImpersonatedUser(ctx.Session)
	if err != nil {
		ctx.ServerError("ExitImpersonatedUser", err)
		return ""
	}
	if exitedImpersonated {
		audit.RecordAs(ctx, impersonator, audit_model.UserImpersonationExit, ctx.Doer)
		return setting.AppSubURL + "/-/admin"
	}

	if !all {
		remaining, err := session.RemoveSignedInAccount(ctx.Session, ctx.Doer.ID)
		if err != nil {
			ctx.ServerError("RemoveSignedInAccount", err)
			return ""
		}
		if len(remaining) > 0 {
			setRememberCookie(ctx, revokeRememberTokens(ctx, ctx.Doer.ID))
			for _, acc := range remaining {
				switched, err := switchToAccount(ctx, acc)
				if err != nil {
					ctx.ServerError("switchToAccount", err)
					return ""
				}
				if switched { // not buildSignOutRedirectURL: ending the SSO session would end the remaining accounts too
					return setting.AppSubURL + "/"
				}
			}
		}
	}

	deleteRememberTokens(ctx, rememberCookieParts(ctx))
	// prepare the sign-out URL before destroying the session
	redirectTo = buildSignOutRedirectURL(ctx)
	HandleSignOut(ctx)
	return redirectTo
}

// revokeRememberTokens deletes the remember-me tokens of uid in this cookie and returns the remaining parts
func revokeRememberTokens(ctx *context.Context, uid int64) (kept []string) {
	for _, part := range rememberCookieParts(ctx) {
		id, _, _ := strings.Cut(part, ":")
		t, err := auth.GetAuthTokenByID(ctx, id)
		if err != nil {
			if !errors.Is(err, util.ErrNotExist) {
				log.Error("GetAuthTokenByID: %v", err)
			}
			continue // an unusable part is dropped either way
		}
		if t.UserID == uid {
			if err := auth.DeleteAuthTokenByID(ctx, id); err != nil {
				log.Error("DeleteAuthTokenByID: %v", err)
			}
			continue
		}
		kept = append(kept, part)
	}
	return kept
}

// SwitchFromGoneAccount hands the session over to another of its accounts once the active one stopped authenticating,
// it reports whether it did and answered the request
func SwitchFromGoneAccount(ctx *context.Context) bool {
	goneUID, ok := ctx.Session.Get(session.KeyUID).(int64)
	if !ok {
		return false
	}
	remaining, err := session.RemoveSignedInAccount(ctx.Session, goneUID)
	if err != nil {
		log.Error("SwitchFromGoneAccount: %v", err)
		return false
	}
	for _, acc := range remaining {
		switched, err := switchToAccount(ctx, acc)
		if err != nil {
			log.Error("SwitchFromGoneAccount: %v", err)
			return false
		}
		if switched {
			ctx.Redirect(setting.AppSubURL + "/") // not the requested page, a form meant for the gone account must not be replayed
			return true
		}
	}
	return false
}

// switchToAccount reports false without an error when the account is no longer usable, pruning it
func switchToAccount(ctx *context.Context, acc session.SignedInAccount) (bool, error) {
	u, err := user_model.GetUserByID(ctx, acc.UID)
	if err != nil && !user_model.IsErrUserNotExist(err) {
		return false, err
	}
	if err != nil || !context.CanUseSignedInAccount(u) { // it may have been deleted or disabled since it was added
		_, err = session.RemoveSignedInAccount(ctx.Session, acc.UID)
		return false, err
	}

	userHasTwoFactorAuth, err := auth.HasTwoFactorOrWebAuthn(ctx, u.ID)
	if err != nil {
		return false, err
	}

	auth_service.ClearSessionKeysForSignIn(ctx.Session) // a half-finished flow must not complete as another account
	if err = regenerateSession(ctx, map[string]any{
		session.KeyUID:                  u.ID,
		session.KeyUserHasTwoFactorAuth: userHasTwoFactorAuth,
		session.KeySignInMethod:         acc.SignInMethod,
	}); err != nil {
		return false, err
	}
	return true, resetLocale(ctx, u)
}

// isReverseProxySignIn reports whether the proxy header decides the account, it would undo any switch on the next request
func isReverseProxySignIn(ctx *context.Context) bool {
	return ctx.Data["IsReverseProxy"] == true
}

// AddAnotherAccount sends the user to the sign-in page while keeping the current account signed in
func AddAnotherAccount(ctx *context.Context) {
	if context.IsDoerSessionImpersonated(ctx.Session) || ctx.Doer.MustChangePassword || isReverseProxySignIn(ctx) {
		ctx.HTTPError(http.StatusForbidden)
		return
	}
	accounts := session.GetSignedInAccounts(ctx.Session)
	if len(accounts) >= session.MaxSignedInAccounts {
		ctx.Flash.Error(ctx.Tr("auth.max_accounts_reached", session.MaxSignedInAccounts))
		ctx.JSONRedirect(setting.AppSubURL + "/")
		return
	}
	if _, ok := session.FindSignedInAccount(accounts, ctx.Doer.ID); !ok { // signed in by a middleware, or before the list existed
		method, _ := ctx.Session.Get(session.KeySignInMethod).(string)
		if err := session.AddSignedInAccount(ctx.Session, session.SignedInAccount{UID: ctx.Doer.ID, SignInMethod: method}); err != nil {
			ctx.ServerError("AddSignedInAccount", err)
			return
		}
	}
	if err := session.StartAddingAccount(ctx.Session, ctx.Doer.ID); err != nil {
		ctx.ServerError("StartAddingAccount", err)
		return
	}
	if err := ctx.Session.Release(); err != nil {
		ctx.ServerError("Session.Release", err)
		return
	}
	ctx.JSONRedirect(setting.AppSubURL + "/user/login")
}

// CancelAddAccount leaves the "add another account" flow without signing in
func CancelAddAccount(ctx *context.Context) {
	auth_service.ClearSessionKeysForSignIn(ctx.Session) // a half-finished sign-in of the other account must not be resumable
	if err := ctx.Session.Delete(session.KeyAddingAccountFor); err != nil {
		ctx.ServerError("Session.Delete", err)
		return
	}
	if err := ctx.Session.Release(); err != nil {
		ctx.ServerError("Session.Release", err)
		return
	}
	ctx.JSONRedirect(setting.AppSubURL + "/")
}

// SwitchAccount activates another account already authenticated within this session
func SwitchAccount(ctx *context.Context) {
	if isReverseProxySignIn(ctx) {
		ctx.HTTPError(http.StatusForbidden)
		return
	}
	uid := ctx.PathParamInt64("uid")
	acc, ok := session.FindSignedInAccount(session.GetSignedInAccounts(ctx.Session), uid)
	if !ok || uid == ctx.Doer.ID {
		ctx.HTTPError(http.StatusBadRequest)
		return
	}
	oldUID, oldSessID := ctx.Doer.ID, ctx.Session.ID()
	switched, err := switchToAccount(ctx, acc)
	if err != nil {
		ctx.ServerError("switchToAccount", err)
		return
	}
	if !switched {
		ctx.Flash.Error(ctx.Tr("auth.switch_account_unavailable"))
		ctx.JSONRedirect(setting.AppSubURL + "/")
		return
	}
	websocket_service.PublishLogout(oldUID, oldSessID)
	ctx.JSONRedirect(setting.AppSubURL + "/")
}

func buildSignOutRedirectURL(ctx *context.Context) string {
	if ctx.Doer != nil && shouldRedirectToOIDCEndSession(ctx) {
		if s := buildOIDCEndSessionURL(ctx, ctx.Doer); s != "" {
			return s
		}
	}

	// The assumption is: if reverse proxy auth is enabled, then the users should only sign-in via reverse proxy auth.
	if setting.Service.EnableReverseProxyAuth && setting.ReverseProxyLogoutRedirect != "" {
		return setting.ReverseProxyLogoutRedirect
	}
	return setting.AppSubURL + "/"
}

// shouldRedirectToOIDCEndSession reports whether this session should end at the
// OIDC provider. Prefer the session sign-in method so an OAuth2-linked account
// that signed in with a password does not hit end_session_endpoint.
func shouldRedirectToOIDCEndSession(ctx *context.Context) bool {
	return ctx.Session.Get(session.KeySignInMethod) == session.SignInMethodOAuth2
}

func prepareSignUpPageData(ctx *context.Context) bool {
	ctx.Data["Title"] = ctx.Tr("sign_up")
	ctx.Data["SignUpLink"] = setting.AppSubURL + "/user/sign_up"
	ctx.Data["PageIsSignUp"] = true
	ctx.Data["EnableSSPI"] = auth.IsSSPIEnabled(ctx)

	hasUsers, err := user_model.HasUsers(ctx)
	if err != nil {
		ctx.ServerError("HasUsers", err)
		return false
	}
	ctx.Data["IsFirstTimeRegistration"] = !hasUsers.HasAnyUser

	oauth2Providers, err := oauth2.GetOAuth2Providers(ctx, optional.Some(true))
	if err != nil {
		ctx.ServerError("GetOAuth2Providers", err)
		return false
	}
	ctx.Data["OAuth2Providers"] = oauth2Providers

	prepareCommonAuthPageData(ctx, CommonAuthOptions{
		EnableCaptcha: setting.Service.EnableCaptcha,
	})

	// Show Disabled Registration message if DisableRegistration or AllowOnlyExternalRegistration options are true
	ctx.Data["DisableRegistration"] = setting.Service.DisableRegistration || setting.Service.AllowOnlyExternalRegistration

	return true
}

// SignUp render the register page
func SignUp(ctx *context.Context) {
	if !prepareSignUpPageData(ctx) {
		return
	}
	rememberAuthRedirectLink(ctx)
	ctx.HTML(http.StatusOK, tplSignUp)
}

// SignUpPost response for sign up information submission
func SignUpPost(ctx *context.Context) {
	if !prepareSignUpPageData(ctx) {
		return
	}

	form := web.GetForm[*forms.RegisterForm](ctx)

	// Permission denied if DisableRegistration or AllowOnlyExternalRegistration options are true
	if setting.Service.DisableRegistration || setting.Service.AllowOnlyExternalRegistration {
		ctx.HTTPError(http.StatusForbidden)
		return
	}

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplSignUp)
		return
	}

	if !context.VerifyCaptcha(ctx, tplSignUp, form) {
		return
	}

	if !form.IsEmailDomainAllowed() {
		ctx.RenderWithErrDeprecated(ctx.Tr("auth.email_domain_blacklisted"), tplSignUp, &form)
		return
	}

	if form.Password != form.Retype {
		ctx.Data["Err_Password"] = true
		ctx.RenderWithErrDeprecated(ctx.Tr("form.password_not_match"), tplSignUp, &form)
		return
	}
	if len(form.Password) < setting.MinPasswordLength {
		ctx.Data["Err_Password"] = true
		ctx.RenderWithErrDeprecated(ctx.Tr("auth.password_too_short", setting.MinPasswordLength), tplSignUp, &form)
		return
	}
	if !password.IsComplexEnough(form.Password) {
		ctx.Data["Err_Password"] = true
		ctx.RenderWithErrDeprecated(password.BuildComplexityError(ctx.Locale), tplSignUp, &form)
		return
	}
	if err := password.IsPwned(ctx, form.Password); err != nil {
		errMsg := ctx.Tr("auth.password_pwned", "https://haveibeenpwned.com/Passwords")
		if password.IsErrIsPwnedRequest(err) {
			log.Error(err.Error())
			errMsg = ctx.Tr("auth.password_pwned_err")
		}
		ctx.Data["Err_Password"] = true
		ctx.RenderWithErrDeprecated(errMsg, tplSignUp, &form)
		return
	}

	u := &user_model.User{
		Name:   form.UserName,
		Email:  form.Email,
		Passwd: form.Password,
	}

	if !createAndHandleCreatedUser(ctx, tplSignUp, form, u, nil, nil) {
		// error already handled
		return
	}

	ctx.Flash.Success(ctx.Tr("auth.sign_up_successful"))
	handleSignIn(ctx, u, false)
}

// createAndHandleCreatedUser calls createUserInContext and
// then handleUserCreated.
func createAndHandleCreatedUser(ctx *context.Context, tpl templates.TplName, form any, u *user_model.User, overwrites *user_model.CreateUserOverwriteOptions, possibleLinkAccountData *LinkAccountData) bool {
	if !createUserInContext(ctx, tpl, form, u, overwrites, possibleLinkAccountData) {
		return false
	}
	return handleUserCreated(ctx, u, possibleLinkAccountData)
}

// createUserInContext creates a user and handles errors within a given context.
// Optionally, a template can be specified.
func createUserInContext(ctx *context.Context, tpl templates.TplName, form any, u *user_model.User, overwrites *user_model.CreateUserOverwriteOptions, possibleLinkAccountData *LinkAccountData) (ok bool) {
	meta := &user_model.Meta{
		InitialIP:        ctx.RemoteAddr(),
		InitialUserAgent: ctx.Req.UserAgent(),
	}
	if err := user_model.CreateUser(ctx, u, meta, overwrites); err != nil {
		if possibleLinkAccountData != nil && (user_model.IsErrUserAlreadyExist(err) || user_model.IsErrEmailAlreadyUsed(err)) {
			switch setting.OAuth2Client.AccountLinking {
			case setting.OAuth2AccountLinkingAuto:
				user, err := user_model.GetIndividualUserByName(ctx, u.Name)
				if err != nil {
					if !user_model.IsErrUserNotExist(err) {
						ctx.ServerError("GetIndividualUserByName", err)
						return false
					}
					user, err = user_model.GetIndividualUserByPrimaryEmail(ctx, u.Email)
					if err != nil {
						if !user_model.IsErrUserNotExist(err) {
							ctx.ServerError("GetIndividualUserByPrimaryEmail", err)
						} else {
							ctx.NotFound(err)
						}
						return false
					}
				}
				// TODO: probably we should respect 'remember' user's choice...
				oauth2LinkAccount(ctx, user, possibleLinkAccountData, true)
				return false // user is already created here, all redirects are handled
			case setting.OAuth2AccountLinkingLogin:
				showLinkingLogin(ctx, possibleLinkAccountData.AuthSourceID, possibleLinkAccountData.GothUser)
				return false // user will be created only after linking login
			}
		}

		// handle error without a template
		if len(tpl) == 0 {
			ctx.ServerError("CreateUser", err)
			return false
		}

		// handle error with template
		var errNameReserved db.ErrNameReserved
		var errNamePatternNotAllowed db.ErrNamePatternNotAllowed
		var errNameCharsNotAllowed db.ErrNameCharsNotAllowed
		var errEmailInvalid user_model.ErrEmailInvalid
		switch {
		case user_model.IsErrUserAlreadyExist(err):
			ctx.Data["Err_UserName"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("form.username_been_taken"), tpl, form)
		case user_model.IsErrEmailAlreadyUsed(err):
			ctx.Data["Err_Email"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("form.email_been_used"), tpl, form)
		case errors.As(err, &errEmailInvalid):
			ctx.Data["Err_Email"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("form.email_invalid"), tpl, form)
		case errors.As(err, &errNameReserved):
			ctx.Data["Err_UserName"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("user.form.name_reserved", errNameReserved.Name), tpl, form)
		case errors.As(err, &errNamePatternNotAllowed):
			ctx.Data["Err_UserName"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("user.form.name_pattern_not_allowed", errNamePatternNotAllowed.Pattern), tpl, form)
		case errors.As(err, &errNameCharsNotAllowed):
			ctx.Data["Err_UserName"] = true
			ctx.RenderWithErrDeprecated(ctx.Tr("user.form.name_chars_not_allowed", errNameCharsNotAllowed.Name), tpl, form)
		default:
			ctx.ServerError("CreateUser", err)
		}
		return false
	}
	log.Trace("Account created: %s", u.Name)
	return true
}

// handleUserCreated does additional steps after a new user is created.
// It auto-sets admin for the only user, updates the optional external user and
// sends a confirmation email if required.
func handleUserCreated(ctx *context.Context, u *user_model.User, possibleLinkAccountData *LinkAccountData) (ok bool) {
	// Auto-set admin for the only user.
	hasUsers, err := user_model.HasUsers(ctx)
	if err != nil {
		ctx.ServerError("HasUsers", err)
		return false
	}
	if hasUsers.HasOnlyOneUser {
		// the only user is the one just created, will set it as admin
		opts := &user_service.UpdateOptions{
			IsActive:     optional.Some(true),
			IsAdmin:      user_service.UpdateOptionFieldFromValue(true),
			SetLastLogin: true,
		}
		if err := user_service.UpdateUser(ctx, u, opts); err != nil {
			ctx.ServerError("UpdateUser", err)
			return false
		}
	}

	// update external user information
	if possibleLinkAccountData != nil {
		if err := externalaccount.EnsureLinkExternalToUser(ctx, possibleLinkAccountData.AuthSourceID, u, possibleLinkAccountData.GothUser); err != nil {
			log.Error("EnsureLinkExternalToUser failed: %v", err)
		}
	}

	// for active user or the first (admin) user, we don't need to send confirmation email
	if u.IsActive || u.ID == 1 {
		return true
	}

	if setting.Service.RegisterManualConfirm {
		renderActivationPromptMessage(ctx, ctx.Locale.Tr("auth.manual_activation_only"))
		return false
	}

	sendActivateEmail(ctx, u)
	return false
}

func renderActivationPromptMessage(ctx *context.Context, msg template.HTML) {
	ctx.Data["ActivationPromptMessage"] = msg
	ctx.HTML(http.StatusOK, TplActivatePrompt)
}

func sendActivateEmail(ctx *context.Context, u *user_model.User) {
	if ctx.Cache.IsExist("MailResendLimit_" + u.LowerName) {
		renderActivationPromptMessage(ctx, ctx.Locale.Tr("auth.resent_limit_prompt"))
		return
	}

	if err := ctx.Cache.Put("MailResendLimit_"+u.LowerName, u.LowerName, 180); err != nil {
		log.Error("Set cache(MailResendLimit) fail: %v", err)
		renderActivationPromptMessage(ctx, ctx.Locale.Tr("auth.resent_limit_prompt"))
		return
	}

	mailer.SendActivateAccountMail(ctx.Locale, u)

	activeCodeLives := timeutil.MinutesToFriendly(setting.Service.ActiveCodeLives, ctx.Locale)
	msgHTML := ctx.Locale.Tr("auth.confirmation_mail_sent_prompt_ex", u.Email, activeCodeLives)
	renderActivationPromptMessage(ctx, msgHTML)
}

func renderActivationVerifyPassword(ctx *context.Context, code string) {
	ctx.Data["ActivationCode"] = code
	ctx.Data["NeedVerifyLocalPassword"] = true
	ctx.HTML(http.StatusOK, TplActivate)
}

func renderActivationChangeEmail(ctx *context.Context) {
	ctx.HTML(http.StatusOK, TplActivate)
}

// Activate render activate user page
func Activate(ctx *context.Context) {
	code := ctx.FormString("code")

	if code == "" {
		if ctx.Doer == nil {
			ctx.Redirect(setting.AppSubURL + "/user/login")
			return
		} else if ctx.Doer.IsActive {
			ctx.Redirect(setting.AppSubURL + "/")
			return
		}

		if setting.MailService == nil || !setting.Service.RegisterEmailConfirm {
			renderActivationPromptMessage(ctx, ctx.Tr("auth.disable_register_mail"))
			return
		}

		// Resend confirmation email. FIXME: ideally this should be in a POST request
		sendActivateEmail(ctx, ctx.Doer)
		return
	}

	// TODO: ctx.Doer/ctx.Data["SignedUser"] could be nil or not the same user as the one being activated
	user := user_model.VerifyUserTimeLimitCode(ctx, &user_model.TimeLimitCodeOptions{Purpose: user_model.TimeLimitCodeActivateAccount}, code)
	if user == nil { // if code is wrong
		renderActivationPromptMessage(ctx, ctx.Locale.Tr("auth.invalid_code"))
		return
	}

	// if account is local account, verify password
	if user.LoginSource == 0 {
		renderActivationVerifyPassword(ctx, code)
		return
	}

	handleAccountActivation(ctx, user)
}

// ActivatePost handles account activation with password check
func ActivatePost(ctx *context.Context) {
	code := ctx.FormString("code")
	if ctx.Doer != nil && ctx.Doer.IsActive {
		ctx.Redirect(setting.AppSubURL + "/user/activate") // it will redirect again to the correct page
		return
	}

	if code == "" {
		newEmail := strings.TrimSpace(ctx.FormString("change_email"))
		if ctx.Doer != nil && newEmail != "" && !strings.EqualFold(ctx.Doer.Email, newEmail) {
			if user_model.ValidateEmail(newEmail) != nil {
				ctx.Flash.Error(ctx.Locale.Tr("form.email_invalid"), true)
				renderActivationChangeEmail(ctx)
				return
			}
			err := user_model.ChangeInactivePrimaryEmail(ctx, ctx.Doer.ID, ctx.Doer.Email, newEmail)
			if err != nil {
				ctx.Flash.Error(ctx.Locale.Tr("admin.emails.not_updated", newEmail), true)
				renderActivationChangeEmail(ctx)
				return
			}
			ctx.Doer.Email = newEmail
		}
		// FIXME: at the moment, GET request handles the "send confirmation email" action. But the old code does this redirect and then send a confirmation email.
		ctx.Redirect(setting.AppSubURL + "/user/activate")
		return
	}

	// TODO: ctx.Doer/ctx.Data["SignedUser"] could be nil or not the same user as the one being activated
	user := user_model.VerifyUserTimeLimitCode(ctx, &user_model.TimeLimitCodeOptions{Purpose: user_model.TimeLimitCodeActivateAccount}, code)
	if user == nil { // if code is wrong
		renderActivationPromptMessage(ctx, ctx.Locale.Tr("auth.invalid_code"))
		return
	}

	// if account is local account, verify password
	if user.LoginSource == 0 {
		password := ctx.FormString("password")
		if password == "" {
			renderActivationVerifyPassword(ctx, code)
			return
		}
		if !user.ValidatePassword(password) {
			ctx.Flash.Error(ctx.Locale.Tr("auth.invalid_password"), true)
			renderActivationVerifyPassword(ctx, code)
			return
		}
	}

	handleAccountActivation(ctx, user)
}

func handleAccountActivation(ctx *context.Context, user *user_model.User) {
	user.IsActive = true
	var err error
	if user.Rands, err = user_model.GetUserSalt(); err != nil {
		ctx.ServerError("UpdateUser", err)
		return
	}
	if err := user_model.UpdateUserCols(ctx, user, "is_active", "rands"); err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.NotFound(err)
		} else {
			ctx.ServerError("UpdateUser", err)
		}
		return
	}

	if err := user_model.ActivateUserEmail(ctx, user.ID, user.Email, true); err != nil {
		log.Error("Unable to activate email for user: %-v with email: %s: %v", user, user.Email, err)
		ctx.ServerError("ActivateUserEmail", err)
		return
	}

	log.Trace("User activated: %s", user.Name)

	if err := regenerateSession(ctx, map[string]any{session.KeyUID: user.ID}); err != nil {
		log.Error("Unable to regenerate session for user: %-v with email: %s: %v", user, user.Email, err)
		ctx.ServerError("ActivateUserEmail", err)
		return
	}

	if err := resetLocale(ctx, user); err != nil {
		ctx.ServerError("resetLocale", err)
		return
	}

	if err := user_service.UpdateUser(ctx, user, &user_service.UpdateOptions{SetLastLogin: true}); err != nil {
		ctx.ServerError("UpdateUser", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("auth.account_activated"))
	redirectAfterAuth(ctx)
}

// ActivateEmail render the activate email page
func ActivateEmail(ctx *context.Context) {
	code := ctx.FormString("code")
	emailStr := ctx.FormString("email")

	// Verify code.
	if email := user_model.VerifyActiveEmailCode(ctx, code, emailStr); email != nil {
		if err := user_model.ActivateEmail(ctx, email); err != nil {
			ctx.ServerError("ActivateEmail", err)
			return
		}

		log.Trace("Email activated: %s", email.Email)
		ctx.Flash.Success(ctx.Tr("settings.add_email_success"))

		if u, err := user_model.GetUserByID(ctx, email.UID); err != nil {
			log.Warn("GetUserByID: %d", email.UID)
		} else {
			// Allow user to validate more emails
			_ = ctx.Cache.Delete("MailResendLimit_" + u.LowerName)
		}
	}

	// FIXME: e-mail verification does not require the user to be logged in,
	// so this could be redirecting to the login page.
	// Should users be logged in automatically here? (consider 2FA requirements, etc.)
	ctx.Redirect(setting.AppSubURL + "/user/settings/account")
}

// recordSignedInAccount makes uid the active account; outside an "add another account" flow a new one
// replaces the list, so nobody inherits accounts they never authenticated
func recordSignedInAccount(sess session.Store, uid int64, method string, adding bool) error {
	if err := sess.Set(session.KeySignInMethod, method); err != nil { // even when empty, it would be the previous account's
		return err
	}
	if _, known := session.FindSignedInAccount(session.GetSignedInAccounts(sess), uid); !known && !adding {
		if err := session.SetSignedInAccounts(sess, nil); err != nil {
			return err
		}
	}
	if err := session.AddSignedInAccount(sess, session.SignedInAccount{UID: uid, SignInMethod: method}); err != nil {
		return err
	}
	return sess.Delete(session.KeyAddingAccountFor)
}

func regenerateSession(ctx *context.Context, updates map[string]any) error {
	sess := ctx.Session
	prevUID, _ := sess.Get(session.KeyUID).(int64)
	addingFor := session.AddingAccountFor(sess)
	sess.Regenerate(ctx.Resp, ctx.Req)
	sessID := sess.ID()
	for k, v := range updates {
		if err := sess.Set(k, v); err != nil {
			return fmt.Errorf("set %v in session[%s]: %w", k, sessID, err)
		}
	}
	if uid, ok := updates[session.KeyUID].(int64); ok {
		method, _ := updates[session.KeySignInMethod].(string)
		if err := recordSignedInAccount(sess, uid, method, addingFor != 0 && addingFor == prevUID); err != nil {
			return fmt.Errorf("record signed-in account in session[%s]: %w", sessID, err)
		}
	}
	if err := sess.Release(); err != nil {
		return fmt.Errorf("store session[%s]: %w", sessID, err)
	}
	return nil
}
