// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package rv

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/woozymasta/bercon/parser"
)

// Admin is one connected RCon administrator.
type Admin struct {
	// Parsed endpoint.
	Address netip.AddrPort `json:"address" yaml:"address"`

	// Original invalid address.
	AddressText string `json:"address_text,omitempty" yaml:"address_text,omitempty"`

	// Row ID, or -1 if invalid.
	ID int `json:"id" yaml:"id"`

	// IDValid reports whether the row contained a numeric ID.
	IDValid bool `json:"id_valid" yaml:"id_valid"`

	// AddressValid reports whether Address parsed successfully.
	AddressValid bool `json:"address_valid" yaml:"address_valid"`
}

// ParseAdmins parses the Real Virtuality admins response observed in Arma 3 and DayZ.
// Arma 2 compatibility has not been independently verified.
func ParseAdmins(data []byte) ([]Admin, error) {
	if !hasLine(data, "Connected RCon admins:") {
		return nil, parser.ErrUnrecognized
	}

	var out []Admin
	var issues []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Skip section headings, column labels, and separators before attempting row parsing;
		// they are structure, not malformed administrator records.
		if line == "" ||
			strings.Contains(line, "Connected RCon admins:") ||
			strings.Contains(line, "[IP Address]") ||
			strings.Trim(line, "-") == "" {
			continue
		}

		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if strings.EqualFold(f[0], "id") || strings.EqualFold(f[0], "[#]") {
			continue
		}

		id, err := strconv.Atoi(f[0])
		idValid := err == nil
		if !idValid {
			// Keep malformed rows visible with an explicit invalid ID
			// instead of assigning a synthetic value that could be mistaken for a server ID.
			id = -1
			issues = append(issues, "admin: invalid ID")
		}

		addr, err := parseAddrPort(f[1])
		if err != nil {
			issues = append(issues, fmt.Sprintf("admin %d: invalid address", id))
		}

		admin := Admin{
			ID:           id,
			IDValid:      idValid,
			Address:      addr,
			AddressValid: err == nil,
		}
		if err != nil {
			admin.AddressText = f[1]
		}

		out = append(out, admin)
	}

	return out, joinIssues(issues)
}
