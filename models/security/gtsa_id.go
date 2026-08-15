// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"strings"

	"gitea.dev/models/db"
	"gitea.dev/modules/util"

	"xorm.io/builder"
)

// gtsaAlphabet is GitHub's ambiguity-free alphabet: no 0/O/1/l/I, etc.
const gtsaAlphabet = "23456789cfghjmpqrvwx"

const gtsaMaxGenerateAttempts = 10

// GenerateGTSAID generates a new, unique GTSA advisory ID of the form
// "GTSA-xxxx-xxxx-xxxx", retrying on the rare collision. There is no
// per-repo sequential index: the ID is the URL key, as on GitHub.
func GenerateGTSAID(ctx context.Context) (string, error) {
	return generateGTSAID(ctx, newGTSAID)
}

// generateGTSAID is GenerateGTSAID with an injectable candidate generator,
// so the retry-on-collision path is deterministically testable.
func generateGTSAID(ctx context.Context, genCandidate func() string) (string, error) {
	for range gtsaMaxGenerateAttempts {
		id := genCandidate()
		exists, err := db.Exist[Advisory](ctx, builder.Eq{"gtsa_id": id})
		if err != nil {
			return "", err
		}
		if !exists {
			return id, nil
		}
	}
	return "", util.NewInvalidArgumentErrorf("could not generate a unique GTSA ID after %d attempts", gtsaMaxGenerateAttempts)
}

func newGTSAID() string {
	var b strings.Builder
	b.WriteString("GTSA")
	for range 3 {
		b.WriteByte('-')
		for range 4 {
			b.WriteByte(gtsaAlphabet[util.CryptoRandomInt(int64(len(gtsaAlphabet)))])
		}
	}
	return b.String()
}
