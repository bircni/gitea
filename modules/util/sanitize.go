// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package util

import (
	"regexp"
	"strings"
	"sync"
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

var globalVars = sync.OnceValue(func() (ret struct {
	schemeCredentialURL     *regexp.Regexp
	schemelessCredentialURL *regexp.Regexp
},
) {
	// RFC 3986: userinfo can contain - . _ ~ ! $ & ' ( ) * + , ; = : and any percent-encoded chars
	ret.schemeCredentialURL = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([A-Za-z0-9-._~!$&'()*+,;=:%]+@)([A-Za-z0-9.-]+(:[0-9]+)?|$)`)
	ret.schemelessCredentialURL = regexp.MustCompile(`(^|[^A-Za-z0-9._~%!$&'()*+,;=-])([A-Za-z0-9-._~!$&'()*+,;=%]+:[A-Za-z0-9-._~!$&'()*+,;=:%]+@)([A-Za-z0-9.-]+(:[0-9]+)?)`)
	return ret
})

// SanitizeCredentialURLs remove all credentials in URLs for the input string:
// * "https://userinfo@domain.com" => "https://sanitized-credential@domain.com"
// * "user:pass@domain.com" => "sanitized-credential@domain.com"
func SanitizeCredentialURLs(s string) string {
	if strings.Contains(s, ":") && strings.Contains(s, "@") {
		return globalVars().schemelessCredentialURL.ReplaceAllString(s, "${1}"+userPlaceholder+"@${3}")
	}
	if strings.Contains(s, "://") && strings.Contains(s, "@") {
		return globalVars().schemeCredentialURL.ReplaceAllString(s, "${1}"+userPlaceholder+"@${3}")
	}
	return s
}
