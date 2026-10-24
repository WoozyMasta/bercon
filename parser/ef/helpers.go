// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef

import (
	"errors"
	"strings"

	"github.com/woozymasta/bercon/parser"
)

// hasLine reports whether data contains a complete, case-insensitive marker line.
func hasLine(data []byte, marker string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if strings.EqualFold(line, marker) {
			return true
		}
	}

	return false
}

// hasPrefixLine reports whether a complete line starts with a marker.
func hasPrefixLine(data []byte, marker string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(marker)) {
			return true
		}
	}

	return false
}

// joinIssues returns nil for clean input or an errors.Is-compatible aggregate.
func joinIssues(issues []string) error {
	if len(issues) == 0 {
		return nil
	}

	errList := make([]error, 1, len(issues)+1)
	errList[0] = parser.ErrMalformedRows
	for _, issue := range issues {
		errList = append(errList, errors.New(issue))
	}

	return errors.Join(errList...)
}
