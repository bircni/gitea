// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package cvss parses and scores CVSS vector strings.
//
// v3.1 is scored natively (base score only, per the CVSS v3.1 specification's
// published formula). v4.0 is validated only: the vector's metrics and values
// are checked against the specification, but no score is derived, since a
// correct v4.0 score requires the ~270-entry MacroVector lookup table.
package cvss

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"gitea.dev/modules/util"
)

// Severity is a CVSS qualitative severity rating.
type Severity string

const (
	SeverityNone     Severity = "none"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// SeverityFromScore maps a CVSS v3.1 base score to its qualitative rating,
// per the CVSS v3.1 specification's ratings table.
func SeverityFromScore(score float64) Severity {
	switch {
	case score == 0:
		return SeverityNone
	case score < 4.0:
		return SeverityLow
	case score < 7.0:
		return SeverityMedium
	case score < 9.0:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}

var v31MetricValues = map[string]map[string]float64{
	"AV": {"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2},
	"AC": {"L": 0.77, "H": 0.44},
	"PR": {"N": 0.85, "L": 0.62, "H": 0.27}, // PR:L/PR:H are re-weighted below when Scope is Changed
	"UI": {"N": 0.85, "R": 0.62},
	"S":  {"U": 0, "C": 0}, // Scope has no direct weight; it toggles the formula
	"C":  {"N": 0, "L": 0.22, "H": 0.56},
	"I":  {"N": 0, "L": 0.22, "H": 0.56},
	"A":  {"N": 0, "L": 0.22, "H": 0.56},
}

// v31MetricOrder is the canonical order of CVSS v3.1 base metrics.
var v31MetricOrder = []string{"AV", "AC", "PR", "UI", "S", "C", "I", "A"}

// V31 is a parsed CVSS v3.1 base vector.
type V31 struct {
	Vector string
	Values map[string]string // metric abbreviation -> value, base metrics only
}

// ParseV31 parses a CVSS v3.1 vector string and validates that all eight base
// metrics are present with recognized values. Temporal and environmental
// metrics, if present, are validated for shape but not used in scoring.
func ParseV31(vector string) (*V31, error) {
	const prefix = "CVSS:3.1/"
	if !strings.HasPrefix(vector, prefix) {
		return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector must start with %q", prefix)
	}
	rest := strings.TrimPrefix(vector, prefix)
	if rest == "" {
		return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector has no metrics")
	}

	values := make(map[string]string)
	for part := range strings.SplitSeq(rest, "/") {
		metric, value, ok := strings.Cut(part, ":")
		if !ok || metric == "" || value == "" {
			return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector has malformed metric %q", part)
		}
		if _, dup := values[metric]; dup {
			return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector has duplicate metric %q", metric)
		}
		values[metric] = value
	}

	for _, m := range v31MetricOrder {
		allowed, known := v31MetricValues[m]
		if !known {
			continue
		}
		v, present := values[m]
		if !present {
			return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector is missing required metric %q", m)
		}
		if _, ok := allowed[v]; !ok {
			return nil, util.NewInvalidArgumentErrorf("cvss v3.1 vector has invalid value %q for metric %q", v, m)
		}
	}

	return &V31{Vector: vector, Values: values}, nil
}

// BaseScore computes the CVSS v3.1 base score, per the formula published in
// the CVSS v3.1 specification document, section 7.4.
func (v *V31) BaseScore() float64 {
	scopeChanged := v.Values["S"] == "C"

	prWeight := v31MetricValues["PR"][v.Values["PR"]]
	if scopeChanged {
		switch v.Values["PR"] {
		case "L":
			prWeight = 0.68
		case "H":
			prWeight = 0.5
		}
	}

	c := v31MetricValues["C"][v.Values["C"]]
	i := v31MetricValues["I"][v.Values["I"]]
	a := v31MetricValues["A"][v.Values["A"]]
	iss := 1 - ((1 - c) * (1 - i) * (1 - a))

	var impact float64
	if scopeChanged {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	if impact <= 0 {
		return 0
	}

	exploitability := 8.22 *
		v31MetricValues["AV"][v.Values["AV"]] *
		v31MetricValues["AC"][v.Values["AC"]] *
		prWeight *
		v31MetricValues["UI"][v.Values["UI"]]

	if scopeChanged {
		return roundUp(math.Min(1.08*(impact+exploitability), 10))
	}
	return roundUp(math.Min(impact+exploitability, 10))
}

// roundUp rounds up to the nearest 0.1, using the integer-arithmetic method
// mandated by the CVSS v3.1 specification to avoid floating point drift.
func roundUp(x float64) float64 {
	intInput := int64(math.Round(x * 100000))
	if intInput%10000 == 0 {
		return float64(intInput) / 100000
	}
	return float64((intInput/10000)+1) / 10
}

// v40MetricValues enumerates every CVSS v4.0 metric abbreviation and its
// permitted values, per the CVSS v4.0 specification. Optional metrics
// additionally allow "X" ("Not Defined").
var v40MetricValues = map[string][]string{
	// Base (all required)
	"AV": {"N", "A", "L", "P"},
	"AC": {"L", "H"},
	"AT": {"N", "P"},
	"PR": {"N", "L", "H"},
	"UI": {"N", "P", "A"},
	"VC": {"H", "L", "N"},
	"VI": {"H", "L", "N"},
	"VA": {"H", "L", "N"},
	"SC": {"H", "L", "N"},
	"SI": {"H", "L", "N"},
	"SA": {"H", "L", "N"},
	// Threat (optional)
	"E": {"X", "A", "P", "U"},
	// Environmental (optional)
	"CR":  {"X", "H", "M", "L"},
	"IR":  {"X", "H", "M", "L"},
	"AR":  {"X", "H", "M", "L"},
	"MAV": {"X", "N", "A", "L", "P"},
	"MAC": {"X", "L", "H"},
	"MAT": {"X", "N", "P"},
	"MPR": {"X", "N", "L", "H"},
	"MUI": {"X", "N", "P", "A"},
	"MVC": {"X", "H", "L", "N"},
	"MVI": {"X", "H", "L", "N"},
	"MVA": {"X", "H", "L", "N"},
	"MSC": {"X", "H", "L", "N"},
	"MSI": {"X", "S", "H", "L", "N"},
	"MSA": {"X", "S", "H", "L", "N"},
	// Supplemental (optional)
	"S":  {"X", "N", "P"},
	"AU": {"X", "N", "Y"},
	"R":  {"X", "A", "U", "I"},
	"V":  {"X", "D", "C"},
	"RE": {"X", "L", "M", "H"},
	"U":  {"X", "Clear", "Green", "Amber", "Red"},
}

var v40RequiredBaseMetrics = []string{"AV", "AC", "AT", "PR", "UI", "VC", "VI", "VA", "SC", "SI", "SA"}

// ValidateV40 validates a CVSS v4.0 vector string's structure and metric
// values, per the CVSS v4.0 specification. It does not compute a score:
// full v4.0 scoring requires the specification's MacroVector lookup table,
// which this package does not implement.
func ValidateV40(vector string) error {
	const prefix = "CVSS:4.0/"
	if !strings.HasPrefix(vector, prefix) {
		return util.NewInvalidArgumentErrorf("cvss v4.0 vector must start with %q", prefix)
	}
	rest := strings.TrimPrefix(vector, prefix)
	if rest == "" {
		return util.NewInvalidArgumentErrorf("cvss v4.0 vector has no metrics")
	}

	seen := make(map[string]bool)
	for part := range strings.SplitSeq(rest, "/") {
		metric, value, ok := strings.Cut(part, ":")
		if !ok || metric == "" || value == "" {
			return util.NewInvalidArgumentErrorf("cvss v4.0 vector has malformed metric %q", part)
		}
		if seen[metric] {
			return util.NewInvalidArgumentErrorf("cvss v4.0 vector has duplicate metric %q", metric)
		}
		seen[metric] = true

		allowed, known := v40MetricValues[metric]
		if !known {
			return util.NewInvalidArgumentErrorf("cvss v4.0 vector has unknown metric %q", metric)
		}
		if !slices.Contains(allowed, value) {
			return util.NewInvalidArgumentErrorf("cvss v4.0 vector has invalid value %q for metric %q", value, metric)
		}
	}

	for _, m := range v40RequiredBaseMetrics {
		if !seen[m] {
			return util.NewInvalidArgumentErrorf("cvss v4.0 vector is missing required metric %q", m)
		}
	}

	return nil
}

// String is a small helper for error messages/debugging.
func (v *V31) String() string {
	return fmt.Sprintf("%s (base score %.1f)", v.Vector, v.BaseScore())
}
