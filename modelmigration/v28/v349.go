// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v28

import (
	"context"

	"gitea.dev/modelmigration/base"

	"xorm.io/xorm"
)

// AddSecurityAdvisoryColumnsToRepository adds the columns gating the security
// advisories nav tab and tracking published advisory counts.
func AddSecurityAdvisoryColumnsToRepository(_ context.Context, x base.EngineMigration) error {
	type Repository struct {
		PrivateReportingEnabled bool `xorm:"NOT NULL DEFAULT false"`
		NumPublishedAdvisories  int  `xorm:"NOT NULL DEFAULT 0"`
	}

	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreConstrains:  true,
		IgnoreDropIndices: true,
	}, new(Repository))
	return err
}
