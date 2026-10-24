// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef

import (
	"fmt"
	"strings"

	"github.com/woozymasta/bercon/parser"
)

// Ban is one identity and display name from the Enfusion ban list.
type Ban struct {
	// IdentityID is the account identity used by the server's ban commands.
	IdentityID string `json:"identity_id" yaml:"identity_id"`

	// BannedName is the display name recorded when the ban was created.
	BannedName string `json:"banned_name" yaml:"banned_name"`

	// IdentityIDValid reports whether IdentityID has the observed UUID form.
	IdentityIDValid bool `json:"identity_id_valid" yaml:"identity_id_valid"`
}

// ParseBans parses the paged Enfusion identity/name ban-list response.
// Partial results are reported with parser.ErrMalformedRows.
func ParseBans(data []byte) ([]Ban, error) {
	if hasLine(data, "Server has no bans to list.") {
		return nil, nil
	}
	if !hasLine(data, "Identity Id | Banned name") || !hasPrefixLine(data, "Total bans:") {
		return nil, parser.ErrUnrecognized
	}

	var bans []Ban
	var issues []string
	insideRows := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" || strings.HasPrefix(line, "Total bans:") {
			continue
		}

		if strings.EqualFold(line, "Identity Id | Banned name") {
			insideRows = true
			continue
		}
		if !insideRows {
			continue
		}

		identityID, bannedName, ok := strings.Cut(line, "|")
		if !ok {
			issues = append(issues, fmt.Sprintf("ban row %q: missing separator", line))
			continue
		}

		identityID = strings.TrimSpace(identityID)
		bannedName = strings.TrimSpace(bannedName)
		valid := isIdentityID(identityID)
		if !valid || bannedName == "" {
			issues = append(issues, fmt.Sprintf("ban %q: malformed fields", identityID))
		}
		bans = append(bans, Ban{
			IdentityID:      identityID,
			BannedName:      bannedName,
			IdentityIDValid: valid,
		})
	}

	return bans, joinIssues(issues)
}

// isIdentityID validates the UUID-shaped identity observed in Arma Reforger responses.
func isIdentityID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' ||
		value[18] != '-' || value[23] != '-' {
		return false
	}

	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}

	return true
}
