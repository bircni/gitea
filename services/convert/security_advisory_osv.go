// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"context"
	"time"

	"gitea.dev/models/db"
	security_model "gitea.dev/models/security"
	"gitea.dev/modules/security/versionrange"
)

// OSV is the ossf.github.io/osv-schema shape of a single advisory. Only the
// fields this milestone can populate accurately are included; unsupported
// fields (credits, database-specific severity calculators, etc.) are
// deliberately omitted rather than filled with placeholder data.
type OSV struct {
	SchemaVersion string        `json:"schema_version"`
	ID            string        `json:"id"`
	Aliases       []string      `json:"aliases,omitempty"`
	Summary       string        `json:"summary,omitempty"`
	Details       string        `json:"details,omitempty"`
	Severity      []OSVSeverity `json:"severity,omitempty"`
	Affected      []OSVAffected `json:"affected"`
	Published     time.Time     `json:"published"`
	Modified      time.Time     `json:"modified"`
	Withdrawn     *time.Time    `json:"withdrawn,omitempty"`
}

// OSVSeverity carries a CVSS vector under OSV's severity type tags.
type OSVSeverity struct {
	Type  string `json:"type"` // "CVSS_V3" or "CVSS_V4"
	Score string `json:"score"`
}

// OSVAffected is one affected package, with its version ranges.
type OSVAffected struct {
	Package OSVPackage `json:"package"`
	Ranges  []OSVRange `json:"ranges"`
}

// OSVPackage identifies an affected package's ecosystem and name.
type OSVPackage struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// OSVRange is one ordered sequence of version events.
type OSVRange struct {
	Type   string     `json:"type"` // "ECOSYSTEM": Gitea does not evaluate semver-typed ranges, see modules/security/versionrange.
	Events []OSVEvent `json:"events"`
}

// OSVEvent is a single version boundary. Exactly one field is set per event,
// per the OSV schema.
type OSVEvent struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
}

// OSVList is the bulk-export document shape used by osv.dev's "all.zip" feeds:
// a single object with a "vulns" array, rather than a bare JSON array.
type OSVList struct {
	Vulns []*OSV `json:"vulns"`
}

// ToOSV converts a published advisory to its OSV export shape. Callers must
// ensure advisory.State == security_model.StatePublished themselves: this
// function does not check state, since draft/triage content must never reach
// an OSV endpoint regardless of what this function would produce for it.
func ToOSV(ctx context.Context, advisory *security_model.Advisory) (*OSV, error) {
	var vulns []*security_model.AdvisoryVulnerability
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisory.ID).OrderBy("id").Find(&vulns); err != nil {
		return nil, err
	}

	osv := &OSV{
		SchemaVersion: "1.6.0",
		ID:            advisory.GTSAID,
		Summary:       advisory.Summary,
		Details:       advisory.Description,
		Affected:      make([]OSVAffected, 0, len(vulns)),
		Published:     advisory.PublishedUnix.AsTime(),
		Modified:      advisory.UpdatedUnix.AsTime(),
	}
	if advisory.CVEID != "" {
		osv.Aliases = append(osv.Aliases, advisory.CVEID)
	}
	if advisory.CVSSv3Vector != "" {
		osv.Severity = append(osv.Severity, OSVSeverity{Type: "CVSS_V3", Score: advisory.CVSSv3Vector})
	}
	if advisory.CVSSv4Vector != "" {
		osv.Severity = append(osv.Severity, OSVSeverity{Type: "CVSS_V4", Score: advisory.CVSSv4Vector})
	}
	if advisory.State == security_model.StateWithdrawn && advisory.WithdrawnUnix != 0 {
		osv.Withdrawn = advisory.WithdrawnUnix.AsTimePtr()
	}

	for _, v := range vulns {
		events, err := vulnerabilityOSVEvents(v)
		if err != nil {
			return nil, err
		}
		osv.Affected = append(osv.Affected, OSVAffected{
			Package: OSVPackage{Ecosystem: v.Ecosystem, Name: v.PackageName},
			Ranges: []OSVRange{{
				Type:   "ECOSYSTEM",
				Events: events,
			}},
		})
	}

	return osv, nil
}

// ToOSVList converts a batch of published advisories to the OSV bulk-export
// document shape. Non-published advisories are skipped defensively, on top
// of whatever filtering the caller already applied - an OSV export must
// never include embargoed content.
func ToOSVList(ctx context.Context, advisories []*security_model.Advisory) (*OSVList, error) {
	list := &OSVList{Vulns: make([]*OSV, 0, len(advisories))}
	for _, advisory := range advisories {
		if advisory.State != security_model.StatePublished {
			continue
		}
		osv, err := ToOSV(ctx, advisory)
		if err != nil {
			return nil, err
		}
		list.Vulns = append(list.Vulns, osv)
	}
	return list, nil
}

// vulnerabilityOSVEvents maps a vulnerability's normalized version ranges to
// OSV events, per the mapping in the milestone plan:
//
//   - VulnerableVersionRange's ">=" (or ">") constraint becomes "introduced".
//     "introduced" must always be set per OSV's data-quality guidance, so an
//     unbounded-below range (no such constraint) falls back to "0", OSV's
//     convention for "vulnerable from the beginning of history".
//   - PatchedVersions' ">=" (or ">") constraint becomes "fixed", since a
//     published advisory always has PatchedVersions set (the publish
//     checklist in services/security/publish.go enforces this as a hard
//     blocker).
//   - If PatchedVersions is empty (should not happen for a published
//     advisory, but handled defensively), VulnerableVersionRange's "<"/"<="
//     constraint becomes "last_affected" instead: OSV's guidance prefers
//     "fixed" over "last_affected" to minimise false negatives, so
//     "last_affected" is only ever the fallback, never the primary path.
func vulnerabilityOSVEvents(vuln *security_model.AdvisoryVulnerability) ([]OSVEvent, error) {
	introduced := "0"
	var vulnRange *versionrange.Range
	if vuln.VulnerableVersionRange != "" {
		r, err := versionrange.Parse(vuln.VulnerableVersionRange)
		if err != nil {
			return nil, err
		}
		vulnRange = r
		for _, c := range r.Constraints {
			if c.Operator == versionrange.OpGe || c.Operator == versionrange.OpGt {
				introduced = c.Version
			}
		}
	}

	events := []OSVEvent{{Introduced: introduced}}

	if vuln.PatchedVersions != "" {
		patchedRange, err := versionrange.Parse(vuln.PatchedVersions)
		if err != nil {
			return nil, err
		}
		for _, c := range patchedRange.Constraints {
			if c.Operator == versionrange.OpGe || c.Operator == versionrange.OpGt {
				events = append(events, OSVEvent{Fixed: c.Version})
			}
		}
		return events, nil
	}

	if vulnRange != nil {
		for _, c := range vulnRange.Constraints {
			if c.Operator == versionrange.OpLt || c.Operator == versionrange.OpLe {
				events = append(events, OSVEvent{LastAffected: c.Version})
			}
		}
	}

	return events, nil
}
