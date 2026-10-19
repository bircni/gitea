// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package session

const (
	KeyUID = "uid"

	KeyImpersonatorData = "impersonatorData"

	KeyUserHasTwoFactorAuth = "userHasTwoFactorAuth"

	// KeySignInMethod records how the current session was authenticated so logout
	// can decide whether RP-initiated OIDC logout is appropriate.
	KeySignInMethod = "signInMethod"

	SignInMethodOAuth2 = "oauth2"

	KeySignedInAccounts = "signedInAccounts"

	KeyAddingAccountFor = "addingAccountFor"

	// KeyRememberedAccountsOffered stops anonymous pages from redirecting to a sign-in page which can't auto sign in.
	KeyRememberedAccountsOffered = "rememberedAccountsOffered"
)
