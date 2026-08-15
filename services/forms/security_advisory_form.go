// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forms

import "gitea.dev/modules/web/middleware"

// NewAdvisoryForm is the maintainer-authored "draft a new advisory" form.
// The structured reporter intake form (services/security/report.go) is a
// separate, later milestone step and has its own form type.
type NewAdvisoryForm struct {
	middleware.FormDefaultValidator
	Summary      string `binding:"Required;MaxSize(1024)"`
	Description  string
	Severity     string
	CVSSv3Vector string
}
