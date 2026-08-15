// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package versionrange parses and normalizes GitHub-syntax vulnerable
// version ranges, e.g. ">= 1.0.0, < 1.2.3".
//
// This package owns parsing and normalization only. It does not evaluate
// whether a given version satisfies a range: hashicorp/go-version (the only
// semver library in this tree) can validate individual versions but has no
// constraint-evaluation API, and adding one is explicitly a later
// milestone's problem, not this package's.
package versionrange

import (
	"strings"

	"gitea.dev/modules/util"

	"github.com/hashicorp/go-version"
)

// Operator is a comparison operator on a single version bound.
type Operator string

const (
	OpEq Operator = "="
	OpLt Operator = "<"
	OpLe Operator = "<="
	OpGt Operator = ">"
	OpGe Operator = ">="
)

var validOperators = map[string]Operator{
	"=":  OpEq,
	"==": OpEq,
	"<":  OpLt,
	"<=": OpLe,
	">":  OpGt,
	">=": OpGe,
}

// Constraint is a single "<operator> <version>" bound.
type Constraint struct {
	Operator Operator
	Version  string // normalized, per version.Version.String()
}

// Range is a parsed, normalized vulnerable-version range: an ordered list of
// comma-separated constraints, all of which must hold simultaneously.
type Range struct {
	Constraints []Constraint
}

// String renders the range back to GitHub's comparator syntax, using the
// normalized version strings.
func (r *Range) String() string {
	parts := make([]string, len(r.Constraints))
	for i, c := range r.Constraints {
		parts[i] = string(c.Operator) + " " + c.Version
	}
	return strings.Join(parts, ", ")
}

// IsUnbounded reports whether the range has no upper bound (no "<" or "<="
// constraint). An unbounded range is a data-quality warning: it claims every
// future release is vulnerable, which is very rarely true and is a leading
// cause of downstream scanner false positives.
func (r *Range) IsUnbounded() bool {
	for _, c := range r.Constraints {
		if c.Operator == OpLt || c.Operator == OpLe || c.Operator == OpEq {
			return false
		}
	}
	return true
}

// Parse parses a GitHub-syntax vulnerable version range, e.g.
// ">= 1.0.0, < 1.2.3" or "= 2.0.0". Each comma-separated term must be an
// operator followed by a version parseable by hashicorp/go-version.
func Parse(s string) (*Range, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, util.NewInvalidArgumentErrorf("version range is empty")
	}

	terms := strings.Split(s, ",")
	constraints := make([]Constraint, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			return nil, util.NewInvalidArgumentErrorf("version range %q has an empty term", s)
		}

		op, rawVersion, err := splitOperator(term)
		if err != nil {
			return nil, util.NewInvalidArgumentErrorf("version range %q: %v", s, err)
		}

		v, err := version.NewVersion(rawVersion)
		if err != nil {
			return nil, util.NewInvalidArgumentErrorf("version range %q has an invalid version %q: %v", s, rawVersion, err)
		}

		constraints = append(constraints, Constraint{Operator: op, Version: v.String()})
	}

	return &Range{Constraints: constraints}, nil
}

// splitOperator splits a single term such as ">= 1.0.0" into its operator
// and version substring.
func splitOperator(term string) (Operator, string, error) {
	// Longest operators first so ">=" isn't matched as ">" with a leftover "=".
	for _, raw := range []string{">=", "<=", "==", "=", "<", ">"} {
		if rest, ok := strings.CutPrefix(term, raw); ok {
			op := validOperators[raw]
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return "", "", util.NewInvalidArgumentErrorf("term %q has no version after operator", term)
			}
			return op, rest, nil
		}
	}
	return "", "", util.NewInvalidArgumentErrorf("term %q has no recognized comparison operator", term)
}
