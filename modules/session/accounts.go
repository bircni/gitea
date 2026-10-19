// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package session

import (
	"encoding/gob"
	"slices"
	"time"

	"gitea.dev/modules/timeutil"
)

// MaxSignedInAccounts bounds how many accounts one session can hold.
const MaxSignedInAccounts = 5

const addingAccountTimeout = 10 * time.Minute // an abandoned flow must not keep relaxing the sign-in pages

func init() {
	gob.Register([]SignedInAccount{}) // CHI-SESSION-GOB-REGISTER
	gob.Register(addingAccount{})     // CHI-SESSION-GOB-REGISTER
}

// addingAccount marks an "add another account" flow started by UID.
type addingAccount struct {
	UID   int64
	Until timeutil.TimeStamp
}

func StartAddingAccount(sess Store, uid int64) error {
	return sess.Set(KeyAddingAccountFor, addingAccount{UID: uid, Until: timeutil.TimeStampNow().AddDuration(addingAccountTimeout)})
}

// AddingAccountFor returns the uid which started an unexpired "add another account" flow, or 0.
func AddingAccountFor(sess Store) int64 {
	a, ok := sess.Get(KeyAddingAccountFor).(addingAccount)
	if !ok || timeutil.TimeStampNow() > a.Until {
		return 0
	}
	return a.UID
}

// SignedInAccount is one account which completed authentication within this session.
type SignedInAccount struct {
	UID          int64
	SignInMethod string
}

// GetSignedInAccounts returns the accounts authenticated in this session, most recent first.
func GetSignedInAccounts(sess Store) []SignedInAccount {
	accounts, _ := sess.Get(KeySignedInAccounts).([]SignedInAccount)
	return accounts
}

func SetSignedInAccounts(sess Store, accounts []SignedInAccount) error {
	if len(accounts) == 0 {
		return sess.Delete(KeySignedInAccounts)
	}
	return sess.Set(KeySignedInAccounts, accounts[:min(len(accounts), MaxSignedInAccounts)])
}

// AddSignedInAccount adds or refreshes an account and moves it to the front.
func AddSignedInAccount(sess Store, acc SignedInAccount) error {
	return SetSignedInAccounts(sess, slices.Insert(withoutAccount(GetSignedInAccounts(sess), acc.UID), 0, acc))
}

// RemoveSignedInAccount drops an account and returns the remaining ones.
func RemoveSignedInAccount(sess Store, uid int64) ([]SignedInAccount, error) {
	accounts := withoutAccount(GetSignedInAccounts(sess), uid)
	return accounts, SetSignedInAccounts(sess, accounts)
}

func withoutAccount(accounts []SignedInAccount, uid int64) []SignedInAccount {
	return slices.DeleteFunc(slices.Clone(accounts), func(a SignedInAccount) bool { return a.UID == uid }) // the session holds the original
}

func FindSignedInAccount(accounts []SignedInAccount, uid int64) (SignedInAccount, bool) {
	i := slices.IndexFunc(accounts, func(a SignedInAccount) bool { return a.UID == uid })
	if i < 0 {
		return SignedInAccount{}, false
	}
	return accounts[i], true
}
