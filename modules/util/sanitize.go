// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package util

import (
	"bytes"
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

var schemeSep = []byte("://")

const userInfoPlaceholder = "(masked)"

// SanitizeCredentialURLs remove all credentials in URLs for the input string:
// * "https://userinfo@domain.com" => "https://(masked)@domain.com"
// * "user:pass@domain.com" => "(masked)@domain.com"
func SanitizeCredentialURLs(s string) string {
	// Compare existence, not position: "git@host:path" SSH URLs put '@' before ':',
	// so a position check would skip later credential URLs in the same log line.
	sepAtPos := strings.IndexByte(s, '@')
	if sepAtPos == -1 || strings.IndexByte(s, ':') == -1 {
		return s // fast return if there is no URL scheme or no userinfo
	}
	res := make([]byte, 0, len(s)+len(userInfoPlaceholder)) // a best guess to avoid too many re-allocations
	bs := UnsafeStringToBytes(s)
	for {
		leftPos := sepAtPos - 1
	leftLoop:
		for leftPos >= 0 {
			c := bs[leftPos]
			switch c {
			case '-', '.', '_', '~', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', ':', '%':
				// RFC 3986, userinfo can contain - . _ ~ ! $ & ' ( ) * + , ; = : and any percent-encoded chars
			default:
				valid := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
				if !valid {
					break leftLoop
				}
			}
			leftPos--
		}
		leftPos++

		rightPos := sepAtPos + 1
		// Consume an IP-literal "[...]" (RFC 3986, e.g. IPv6) as one unit so inner ':' don't terminate the host walk.
		if rightPos < len(bs) && bs[rightPos] == '[' {
			if end := bytes.IndexByte(bs[rightPos:], ']'); end > 0 {
				rightPos += end + 1
			} else {
				rightPos++ // unmatched '['; let the host loop consume the rest
			}
		}
	rightLoop:
		for rightPos < len(bs) {
			c := bs[rightPos]
			switch c {
			case '.', '-':
				// valid host char
			default:
				valid := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
				if !valid {
					break rightLoop
				}
			}
			rightPos++
		}

		leading, leftPart, rightPart := bs[:leftPos], bs[leftPos:sepAtPos], bs[sepAtPos+1:rightPos]

		// Either:
		// * git log message: "user:pass@host" (it contains a colon in userinfo)
		// * http like URL: "https://userinfo@host.com" (it has "://" before the userinfo)
		needSanitize := bytes.IndexByte(leftPart, ':') >= 0 || bytes.HasSuffix(leading, schemeSep)
		needSanitize = needSanitize && len(leftPart) > 0 && len(rightPart) > 0
		if needSanitize {
			res = append(res, leading...)
			res = append(res, userInfoPlaceholder...)
			res = append(res, '@')
			res = append(res, rightPart...)
		} else {
			res = append(res, bs[:rightPos]...)
		}
		bs = bs[rightPos:]
		sepAtPos = bytes.IndexByte(bs, '@')
		if sepAtPos == -1 {
			break
		}
	}
	res = append(res, bs...)
	return UnsafeBytesToString(res)
}
