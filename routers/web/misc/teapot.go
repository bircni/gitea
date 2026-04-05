// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"net/http"

	"code.gitea.io/gitea/modules/templates"
	"code.gitea.io/gitea/services/context"
)

const tplTeapot templates.TplName = "misc/teapot"

// Teapot responds with RFC 2324 HTTP 418 and renders misc/teapot.
func Teapot(ctx *context.Context) {
	ctx.Data["Title"] = "418 I'm a teapot"
	ctx.Resp.Header().Set("X-Teapot-Heritage", "RFC-2324")
	ctx.HTML(http.StatusTeapot, tplTeapot)
}
