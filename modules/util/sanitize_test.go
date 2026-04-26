// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package util

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeErrorCredentialURLs(t *testing.T) {
	err := errors.New("error with https://a@b.com")
	se := SanitizeErrorCredentialURLs(err)
	assert.Equal(t, "error with https://"+userInfoPlaceholder+"@b.com", se.Error())
}

func TestSanitizeCredentialURLs(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			"https://github.com/go-gitea/test_repo.git",
			"https://github.com/go-gitea/test_repo.git",
		},
		{
			"https://mytoken@github.com/go-gitea/test_repo.git",
			"https://" + userInfoPlaceholder + "@github.com/go-gitea/test_repo.git",
		},
		{
			"https://user:password@github.com/go-gitea/test_repo.git",
			"https://" + userInfoPlaceholder + "@github.com/go-gitea/test_repo.git",
		},
		{
			"ftp://x@",
			"ftp://x@",
		},
		{
			"ftp://x/@",
			"ftp://x/@",
		},
		{
			"ftp://u@x/@", // test multiple @ chars
			"ftp://" + userInfoPlaceholder + "@x/@",
		},
		{
			"😊ftp://u@x😊", // test unicode
			"😊ftp://" + userInfoPlaceholder + "@x😊",
		},
		{
			"://@",
			"://@",
		},
		{
			"//u:p@h",
			"//" + userInfoPlaceholder + "@h",
		},
		{
			"fatal: unable to look up username:token@github.com (port 9418)",
			"fatal: unable to look up " + userInfoPlaceholder + "@github.com (port 9418)",
		},
		{
			"git failed for user:token@github.com/go-gitea/test_repo.git",
			"git failed for " + userInfoPlaceholder + "@github.com/go-gitea/test_repo.git",
		},
		{
			"s://u@h", // the minimal pattern to be sanitized
			"s://" + userInfoPlaceholder + "@h",
		},
		{
			"URLs in log https://u:b@h and https://u:b@h:80/, with https://h.com and u@h.com",
			"URLs in log https://" + userInfoPlaceholder + "@h and https://" + userInfoPlaceholder + "@h:80/, with https://h.com and u@h.com",
		},
	}

	for n, c := range cases {
		result := SanitizeCredentialURLs(c.input)
		assert.Equal(t, c.expected, result, "case %d: error should match", n)
	}
}
