// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package swagger

import (
	api "gitea.dev/modules/structs"
)

// SecurityAdvisory
// swagger:response SecurityAdvisory
type swaggerResponseSecurityAdvisory struct {
	// in:body
	Body api.SecurityAdvisory `json:"body"`
}

// SecurityAdvisoryList
// swagger:response SecurityAdvisoryList
type swaggerResponseSecurityAdvisoryList struct {
	// in:body
	Body []api.SecurityAdvisory `json:"body"`
}
