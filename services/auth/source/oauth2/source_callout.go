// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package oauth2

import (
	"net/http"
	"net/url"

	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
)

// Callout redirects request/response pair to authenticate against the provider, selectAccount asks it for an account picker
func (source *Source) Callout(request *http.Request, response http.ResponseWriter, selectAccount bool) error {
	// not sure if goth is thread safe (?) when using multiple providers
	request.Header.Set(ProviderHeaderKey, source.AuthSource.Name)

	// don't use the default gothic begin handler to prevent issues when some error occurs
	// normally the gothic library will write some custom stuff to the response instead of our own nice error page
	// gothic.BeginAuthHandler(response, request)

	gothRWMutex.RLock()
	defer gothRWMutex.RUnlock()

	authURL, err := gothic.GetAuthURL(response, request)
	if err != nil {
		return err
	}
	if selectAccount {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		q.Set("prompt", "select_account") // OIDC standard, other providers ignore unknown parameters
		u.RawQuery = q.Encode()
		authURL = u.String()
	}
	http.Redirect(response, request, authURL, http.StatusTemporaryRedirect)
	return nil
}

// Callback handles OAuth callback, resolve to a goth user and send back to original url
// this will trigger a new authentication request, but because we save it in the session we can use that
func (source *Source) Callback(request *http.Request, response http.ResponseWriter) (goth.User, error) {
	// not sure if goth is thread safe (?) when using multiple providers
	request.Header.Set(ProviderHeaderKey, source.AuthSource.Name)

	gothRWMutex.RLock()
	defer gothRWMutex.RUnlock()

	user, err := gothic.CompleteUserAuth(response, request)
	if err != nil {
		return user, err
	}

	return user, nil
}
