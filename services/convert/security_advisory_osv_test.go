// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"testing"

	security_model "gitea.dev/models/security"

	"github.com/stretchr/testify/assert"
)

func TestVulnerabilityOSVEvents(t *testing.T) {
	tests := []struct {
		name   string
		vuln   *security_model.AdvisoryVulnerability
		expect []OSVEvent
	}{
		{
			name: "bounded range with patched version prefers fixed over the range's own upper bound",
			vuln: &security_model.AdvisoryVulnerability{
				VulnerableVersionRange: ">= 1.0.0, < 1.2.3",
				PatchedVersions:        ">= 1.2.3",
			},
			expect: []OSVEvent{
				{Introduced: "1.0.0"},
				{Fixed: "1.2.3"},
			},
		},
		{
			name: "unbounded-below range falls back to introduced 0",
			vuln: &security_model.AdvisoryVulnerability{
				VulnerableVersionRange: "< 2.0.0",
				PatchedVersions:        ">= 2.0.0",
			},
			expect: []OSVEvent{
				{Introduced: "0"},
				{Fixed: "2.0.0"},
			},
		},
		{
			name: "no patched version falls back to last_affected from the vulnerable range's upper bound",
			vuln: &security_model.AdvisoryVulnerability{
				VulnerableVersionRange: ">= 1.0.0, <= 1.2.2",
			},
			expect: []OSVEvent{
				{Introduced: "1.0.0"},
				{LastAffected: "1.2.2"},
			},
		},
		{
			name: "no range information at all still emits introduced 0",
			vuln: &security_model.AdvisoryVulnerability{},
			expect: []OSVEvent{
				{Introduced: "0"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := vulnerabilityOSVEvents(tt.vuln)
			assert.NoError(t, err)
			assert.Equal(t, tt.expect, events)
		})
	}
}

func TestToOSV(t *testing.T) {
	advisory := &security_model.Advisory{
		GTSAID:       "GTSA-test-test-test",
		CVEID:        "CVE-2026-00000",
		State:        security_model.StatePublished,
		Summary:      "test summary",
		CVSSv3Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
	}

	osv, err := ToOSV(t.Context(), advisory)
	assert.NoError(t, err)
	assert.Equal(t, "GTSA-test-test-test", osv.ID)
	assert.Equal(t, []string{"CVE-2026-00000"}, osv.Aliases)
	assert.Equal(t, "test summary", osv.Summary)
	assert.Len(t, osv.Severity, 1)
	assert.Equal(t, "CVSS_V3", osv.Severity[0].Type)
	// No vulnerabilities were persisted for this advisory, so affected is empty rather than nil.
	assert.Empty(t, osv.Affected)
}
