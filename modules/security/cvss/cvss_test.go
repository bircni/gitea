// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cvss

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCVSSParseV31(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		v, err := ParseV31("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
		require.NoError(t, err)
		assert.Equal(t, "N", v.Values["AV"])
	})

	t.Run("wrong prefix", func(t *testing.T) {
		_, err := ParseV31("CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
		require.Error(t, err)
	})

	t.Run("missing metric", func(t *testing.T) {
		_, err := ParseV31("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H")
		require.Error(t, err)
	})

	t.Run("invalid value", func(t *testing.T) {
		_, err := ParseV31("CVSS:3.1/AV:Z/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
		require.Error(t, err)
	})

	t.Run("duplicate metric", func(t *testing.T) {
		_, err := ParseV31("CVSS:3.1/AV:N/AV:L/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
		require.Error(t, err)
	})

	t.Run("malformed metric", func(t *testing.T) {
		_, err := ParseV31("CVSS:3.1/AV/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
		require.Error(t, err)
	})
}

func TestCVSSBaseScoreV31(t *testing.T) {
	cases := []struct {
		vector string
		score  float64
	}{
		// Scope unchanged, all-high impact.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8},
		// Scope changed (Log4Shell, CVE-2021-44228).
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H", 10.0},
		// No impact at all.
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N", 0.0},
		// Low-severity local vector.
		{"CVSS:3.1/AV:L/AC:H/PR:H/UI:R/S:U/C:L/I:N/A:N", 1.8},
	}
	for _, c := range cases {
		v, err := ParseV31(c.vector)
		require.NoError(t, err, c.vector)
		assert.InDelta(t, c.score, v.BaseScore(), 0.001, c.vector)
	}
}

func TestCVSSSeverityFromScore(t *testing.T) {
	assert.Equal(t, SeverityNone, SeverityFromScore(0))
	assert.Equal(t, SeverityLow, SeverityFromScore(3.9))
	assert.Equal(t, SeverityMedium, SeverityFromScore(6.9))
	assert.Equal(t, SeverityHigh, SeverityFromScore(8.9))
	assert.Equal(t, SeverityCritical, SeverityFromScore(9.0))
	assert.Equal(t, SeverityCritical, SeverityFromScore(10.0))
}

func TestCVSSValidateV40(t *testing.T) {
	t.Run("valid base only", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N")
		require.NoError(t, err)
	})

	t.Run("valid with optional metrics", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N/E:A/CR:H/U:Red")
		require.NoError(t, err)
	})

	t.Run("wrong prefix", func(t *testing.T) {
		err := ValidateV40("CVSS:3.1/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N")
		require.Error(t, err)
	})

	t.Run("missing required metric", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N")
		require.Error(t, err)
	})

	t.Run("unknown metric", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N/ZZ:X")
		require.Error(t, err)
	})

	t.Run("invalid value", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:Q/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N")
		require.Error(t, err)
	})

	t.Run("duplicate metric", func(t *testing.T) {
		err := ValidateV40("CVSS:4.0/AV:N/AV:L/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N")
		require.Error(t, err)
	})
}
