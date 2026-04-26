// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package util

import (
	"regexp"
	"strings"
)

type sanitizedError struct {
	err error
}

func (err sanitizedError) Error() string {
	return SanitizeCredentialURLs(err.err.Error())
}

func (err sanitizedError) Unwrap() error {
	return err.err
}

// SanitizeErrorCredentialURLs wraps the error and make sure the returned error message doesn't contain sensitive credentials in URLs
func SanitizeErrorCredentialURLs(err error) error {
	return sanitizedError{err: err}
}

const userPlaceholder = "sanitized-credential"

var (
	schemeCredentialURL     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([A-Za-z0-9._~!$&'()*+,;=:%-]+@)([A-Za-z0-9.-]+(:[0-9]+)?|$)`)
	schemelessCredentialURL = regexp.MustCompile(`(^|[^A-Za-z0-9._~%!$&'()*+,;=-])([A-Za-z0-9._~!$&'()*+,;=%-]+:[A-Za-z0-9._~!$&'()*+,;=:%-]+@)([A-Za-z0-9.-]+(:[0-9]+)?)`)
)

// SanitizeCredentialURLs remove all credentials in URLs for the input string: "https://user:pass@domain.com" => "https://sanitized-credential@domain.com"
func SanitizeCredentialURLs(s string) string {
	if !strings.Contains(s, "@") {
		return s // fast return if there is no userinfo
	}
	s = schemeCredentialURL.ReplaceAllString(s, "${1}"+userPlaceholder+"@${3}")
	return schemelessCredentialURL.ReplaceAllString(s, "${1}"+userPlaceholder+"@${3}")
}
