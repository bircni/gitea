// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

// ProjectMeta contains project metadata for API responses
// swagger:model
type ProjectMeta struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}
