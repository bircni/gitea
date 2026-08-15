// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import "time"

// SecurityAdvisoryState is the lifecycle state of a repository security advisory.
//
// swagger:enum SecurityAdvisoryState
type SecurityAdvisoryState string

const (
	SecurityAdvisoryStateTriage    SecurityAdvisoryState = "triage"
	SecurityAdvisoryStateDraft     SecurityAdvisoryState = "draft"
	SecurityAdvisoryStatePublished SecurityAdvisoryState = "published"
	SecurityAdvisoryStateClosed    SecurityAdvisoryState = "closed"
	SecurityAdvisoryStateWithdrawn SecurityAdvisoryState = "withdrawn"
)

// SecurityAdvisorySeverity is the qualitative severity of a security advisory.
//
// swagger:enum SecurityAdvisorySeverity
type SecurityAdvisorySeverity string

const (
	SecurityAdvisorySeverityLow      SecurityAdvisorySeverity = "low"
	SecurityAdvisorySeverityMedium   SecurityAdvisorySeverity = "medium"
	SecurityAdvisorySeverityHigh     SecurityAdvisorySeverity = "high"
	SecurityAdvisorySeverityCritical SecurityAdvisorySeverity = "critical"
)

// SecurityAdvisoryCreditType is the role a credited person played, reusing OSV's ten credit roles.
//
// swagger:enum SecurityAdvisoryCreditType
type SecurityAdvisoryCreditType string

const (
	SecurityAdvisoryCreditTypeFinder               SecurityAdvisoryCreditType = "finder"
	SecurityAdvisoryCreditTypeReporter             SecurityAdvisoryCreditType = "reporter"
	SecurityAdvisoryCreditTypeAnalyst              SecurityAdvisoryCreditType = "analyst"
	SecurityAdvisoryCreditTypeCoordinator          SecurityAdvisoryCreditType = "coordinator"
	SecurityAdvisoryCreditTypeRemediationDeveloper SecurityAdvisoryCreditType = "remediation_developer"
	SecurityAdvisoryCreditTypeReviewer             SecurityAdvisoryCreditType = "reviewer"
	SecurityAdvisoryCreditTypeVerifier             SecurityAdvisoryCreditType = "verifier"
	SecurityAdvisoryCreditTypeTool                 SecurityAdvisoryCreditType = "tool"
	SecurityAdvisoryCreditTypeSponsor              SecurityAdvisoryCreditType = "sponsor"
	SecurityAdvisoryCreditTypeOther                SecurityAdvisoryCreditType = "other"
)

// SecurityAdvisoryCreditState is a credit's acceptance state.
//
// swagger:enum SecurityAdvisoryCreditState
type SecurityAdvisoryCreditState string

const (
	SecurityAdvisoryCreditStatePending  SecurityAdvisoryCreditState = "pending"
	SecurityAdvisoryCreditStateAccepted SecurityAdvisoryCreditState = "accepted"
	SecurityAdvisoryCreditStateDeclined SecurityAdvisoryCreditState = "declined"
)

// SecurityAdvisoryCVSSSeverities carries the CVSS v3 and v4 vectors/scores of an advisory.
type SecurityAdvisoryCVSSSeverities struct {
	CVSSv3Vector string  `json:"cvss_v3_vector"`
	CVSSv3Score  float64 `json:"cvss_v3_score"`
	CVSSv4Vector string  `json:"cvss_v4_vector"`
}

// SecurityAdvisoryPackage identifies an affected package.
type SecurityAdvisoryPackage struct {
	// Ecosystem is a validated open string: GitHub's 13 ecosystem values,
	// any OSV ecosystem string, or "other" for a package with no registry.
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// SecurityAdvisoryVulnerability is one affected package entry on an advisory.
type SecurityAdvisoryVulnerability struct {
	Package *SecurityAdvisoryPackage `json:"package"`
	// VulnerableVersionRange is stored normalized, e.g. ">= 1.0.0, < 1.2.3".
	VulnerableVersionRange string   `json:"vulnerable_version_range"`
	PatchedVersions        string   `json:"patched_versions"`
	VulnerableFunctions    []string `json:"vulnerable_functions"`
}

// SecurityAdvisoryVulnerabilityOption is the request-side shape for creating or replacing a vulnerability entry.
type SecurityAdvisoryVulnerabilityOption struct {
	// required: true
	Package *SecurityAdvisoryPackage `json:"package" binding:"Required"`
	// required: true
	VulnerableVersionRange string   `json:"vulnerable_version_range" binding:"Required"`
	PatchedVersions        string   `json:"patched_versions"`
	VulnerableFunctions    []string `json:"vulnerable_functions"`
}

// SecurityAdvisoryCredit attributes a person's contribution to an advisory.
type SecurityAdvisoryCredit struct {
	User *User `json:"user,omitempty"`
	// OriginalAuthor is set instead of User for an outside reporter with no local account.
	OriginalAuthor string                      `json:"original_author,omitempty"`
	Type           SecurityAdvisoryCreditType  `json:"type"`
	State          SecurityAdvisoryCreditState `json:"state"`
}

// SecurityAdvisory represents a repository security advisory.
//
// swagger:model
type SecurityAdvisory struct {
	GTSAID  string `json:"gtsa_id"`
	CVEID   string `json:"cve_id"`
	URL     string `json:"url"`
	HTMLURL string `json:"html_url"`

	Summary     string                   `json:"summary"`
	Description string                   `json:"description"`
	Severity    SecurityAdvisorySeverity `json:"severity"`
	State       SecurityAdvisoryState    `json:"state"`

	CVSSSeverities  *SecurityAdvisoryCVSSSeverities  `json:"cvss_severities"`
	CWEIDs          []string                         `json:"cwe_ids"`
	Credits         []*SecurityAdvisoryCredit        `json:"credits"`
	Vulnerabilities []*SecurityAdvisoryVulnerability `json:"vulnerabilities"`

	Author    *User `json:"author"`
	Publisher *User `json:"publisher,omitempty"`

	Repository *RepositoryMeta `json:"repository,omitempty"`

	// swagger:strfmt date-time
	CreatedAt time.Time `json:"created_at"`
	// swagger:strfmt date-time
	UpdatedAt time.Time `json:"updated_at"`
	// swagger:strfmt date-time
	PublishedAt *time.Time `json:"published_at"`
	// swagger:strfmt date-time
	ClosedAt *time.Time `json:"closed_at"`
	// swagger:strfmt date-time
	WithdrawnAt *time.Time `json:"withdrawn_at"`
}

// CreateSecurityAdvisoryOption is the request body to create a maintainer-authored draft advisory.
type CreateSecurityAdvisoryOption struct {
	// required: true
	Summary      string                   `json:"summary" binding:"Required"`
	Description  string                   `json:"description"`
	Severity     SecurityAdvisorySeverity `json:"severity"`
	CVEID        string                   `json:"cve_id"`
	CVSSv3Vector string                   `json:"cvss_v3_vector"`
	CVSSv4Vector string                   `json:"cvss_v4_vector"`
	CWEIDs       []string                 `json:"cwe_ids"`
	// required: true
	Vulnerabilities []*SecurityAdvisoryVulnerabilityOption `json:"vulnerabilities" binding:"Required"`
}

// EditSecurityAdvisoryOption is the request body to update an advisory's metadata or transition its state.
type EditSecurityAdvisoryOption struct {
	Summary      *string                   `json:"summary"`
	Description  *string                   `json:"description"`
	Severity     *SecurityAdvisorySeverity `json:"severity"`
	CVEID        *string                   `json:"cve_id"`
	CVSSv3Vector *string                   `json:"cvss_v3_vector"`
	CVSSv4Vector *string                   `json:"cvss_v4_vector"`
	CWEIDs       *[]string                 `json:"cwe_ids"`
	// State transitions the advisory, e.g. to "published", "closed", or "withdrawn".
	// Publishing runs the publish-time checklist and may be rejected.
	State *SecurityAdvisoryState `json:"state"`
}
