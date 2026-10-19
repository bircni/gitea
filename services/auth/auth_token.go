// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	auth_model "gitea.dev/models/auth"
	"gitea.dev/modules/session"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"
)

// Based on https://paragonie.com/blog/2015/04/secure-authentication-php-with-long-term-persistence#secure-remember-me-cookies

// The auth token consists of two parts: ID and token hash
// Every device login creates a new auth token with an individual id and hash.
// If a device uses the token to login into the instance, a fresh token gets generated which has the same id but a new hash.

var (
	ErrAuthTokenInvalidFormat = util.NewInvalidArgumentErrorf("auth token has an invalid format")
	ErrAuthTokenStale         = util.NewInvalidArgumentErrorf("auth token has been rotated")
	ErrAuthTokenExpired       = util.NewInvalidArgumentErrorf("auth token has expired")
	ErrAuthTokenInvalidHash   = util.NewInvalidArgumentErrorf("auth token is invalid")
)

func CheckAuthToken(ctx context.Context, value string) (*auth_model.AuthToken, error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return nil, ErrAuthTokenInvalidFormat
	}

	t, err := auth_model.GetAuthTokenByID(ctx, parts[0])
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			return nil, ErrAuthTokenExpired
		}
		return nil, err
	}

	if t.ExpiresUnix < timeutil.TimeStampNow() {
		return nil, ErrAuthTokenExpired
	}

	hashedToken := sha256.Sum256([]byte(parts[1]))

	hash := hex.EncodeToString(hashedToken[:])
	if !util.CryptoConstTimeEqual(t.TokenHash, hash) {
		// Recently rotated cookies can still be in flight, but must not authenticate.
		if t.RotatedUnix.AddDuration(time.Minute) >= timeutil.TimeStampNow() && util.CryptoConstTimeEqual(t.PreviousTokenHash, hash) {
			return nil, ErrAuthTokenStale
		}
		// If an attacker steals a token and uses the token to create a new session the hash gets updated.
		// When the victim uses the old token the hashes don't match anymore and the victim should be notified about the compromised token.
		// Revoke the token so the attacker's rotated token (which shares this ID) can no longer be used.
		if err := auth_model.DeleteAuthTokenByID(ctx, t.ID); err != nil {
			return nil, err
		}
		return nil, ErrAuthTokenInvalidHash
	}

	return t, nil
}

// CheckAuthTokens validates a comma separated list of tokens, keeping the usable ones.
// It returns ErrAuthTokenStale when a concurrent request is rewriting the cookie, the caller must leave it alone then.
func CheckAuthTokens(ctx context.Context, value string) (valid []*auth_model.AuthToken, compromised bool, err error) {
	stale := false
	parts := strings.SplitN(value, ",", session.MaxSignedInAccounts+1)
	for _, part := range parts[:min(len(parts), session.MaxSignedInAccounts)] { // never written longer, don't query a crafted one
		t, err := CheckAuthToken(ctx, part)
		switch {
		case errors.Is(err, ErrAuthTokenInvalidHash):
			compromised = true // the row is already revoked, but the other accounts stay usable
		case errors.Is(err, ErrAuthTokenStale):
			stale = true // keep checking, so a compromised token is still reported
		case errors.Is(err, ErrAuthTokenInvalidFormat), errors.Is(err, ErrAuthTokenExpired):
		case err != nil:
			return nil, false, err
		default:
			valid = append(valid, t)
		}
	}
	if stale {
		return nil, compromised, ErrAuthTokenStale
	}
	return valid, compromised, nil
}

func RegenerateAuthToken(ctx context.Context, t *auth_model.AuthToken) (*auth_model.AuthToken, string, error) {
	token, hash := generateTokenAndHash()

	newToken := &auth_model.AuthToken{
		ID:                t.ID,
		TokenHash:         hash,
		PreviousTokenHash: t.TokenHash,
		RotatedUnix:       timeutil.TimeStampNow(),
		UserID:            t.UserID,
		ExpiresUnix:       timeutil.TimeStampNow().AddDuration(time.Duration(setting.LogInRememberDays*24) * time.Hour),
	}

	updated, err := auth_model.UpdateAuthTokenByID(ctx, newToken, t.TokenHash)
	if err != nil {
		return nil, "", err
	}
	if !updated {
		return nil, "", ErrAuthTokenStale
	}

	return newToken, token, nil
}

func CreateAuthTokenForUserID(ctx context.Context, userID int64) (*auth_model.AuthToken, string, error) {
	t := &auth_model.AuthToken{
		UserID:      userID,
		ExpiresUnix: timeutil.TimeStampNow().AddDuration(time.Duration(setting.LogInRememberDays*24) * time.Hour),
	}

	t.ID = util.CryptoRandomString(10)

	token, hash := generateTokenAndHash()

	t.TokenHash = hash

	if err := auth_model.InsertAuthToken(ctx, t); err != nil {
		return nil, "", err
	}

	return t, token, nil
}

func generateTokenAndHash() (string, string) {
	buf := util.CryptoRandomBytes(32)

	token := hex.EncodeToString(buf)

	hashedToken := sha256.Sum256([]byte(token))

	return token, hex.EncodeToString(hashedToken[:])
}
