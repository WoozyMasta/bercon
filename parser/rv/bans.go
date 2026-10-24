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

// Ban is one GUID or IP ban row.
type Ban struct {
	// GUID or IP text named by the ban.
	Subject string `json:"subject" yaml:"subject"`

	// Optional free-form reason.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`

	// Row identifier, or -1 when invalid.
	ID int `json:"id" yaml:"id"`

	// Minutes is the remaining duration; -1 means permanent and 0 means unknown or expired.
	Minutes int `json:"minutes" yaml:"minutes"`

	// Whether Subject matches its section's format.
	SubjectValid bool `json:"subject_valid" yaml:"subject_valid"`

	// Whether the row contained a numeric ID.
	IDValid bool `json:"id_valid" yaml:"id_valid"`

	// Whether ban duration parsed.
	MinutesValid bool `json:"minutes_valid" yaml:"minutes_valid"`

	// True when Subject belongs to the GUID section.
	GUID bool `json:"guid" yaml:"guid"`
}

// Bans contains parsed GUID and IP ban rows.
// Bans contains the Real Virtuality GUID and IP ban rows.
type Bans struct {
	GUID []Ban `json:"guid,omitempty" yaml:"guid,omitempty"` // Parsed GUID bans.
	IP   []Ban `json:"ip,omitempty" yaml:"ip,omitempty"`     // Parsed IPv4 bans.
}

// ParseBans parses the Real Virtuality GUID and IP ban sections.
func ParseBans(data []byte) (Bans, error) {
	if !hasLine(data, "GUID Bans:") && !hasLine(data, "IP Bans:") {
		return Bans{}, parser.ErrUnrecognized
	}

	var out Bans
	var issues []string
	section := ""

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Section headings switch the subject validator and destination slice
		// for all following rows; the response combines two different ban formats.
		if strings.Contains(line, "GUID Bans:") {
			section = "guid"
			continue
		}

		if strings.Contains(line, "IP Bans:") {
			section = "ip"
			continue
		}

		if line == "" || section == "" {
			continue
		}

		if strings.Contains(line, "[GUID]") ||
			strings.Contains(line, "[IP Address]") ||
			strings.Trim(line, "-") == "" {
			continue
		}

		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}

		if strings.EqualFold(f[0], "ID") {
			continue
		}

		id, idErr := strconv.Atoi(f[0])
		idValid := idErr == nil
		if !idValid {
			id = -1 // Keep the row and its subject/reason even when the server ID is invalid.
		}

		minutes, minutesErr := parseMinutes(f[2])
		validSubject := (section == "guid" && isGUID(f[1])) || (section == "ip" && validIP(f[1]))

		// Record a diagnostic without discarding fields
		// that may still help callers investigate or display the partial response.
		if !idValid || minutesErr != nil || !validSubject {
			issues = append(issues, fmt.Sprintf("ban row %q: malformed fields", f[0]))
		}

		b := Ban{
			ID:           id,
			IDValid:      idValid,
			Subject:      f[1],
			SubjectValid: validSubject,
			Minutes:      minutes,
			MinutesValid: minutesErr == nil,
			Reason:       strings.Join(f[3:], " "),
			GUID:         section == "guid",
		}

		if b.GUID {
			out.GUID = append(out.GUID, b)
		} else {
			out.IP = append(out.IP, b)
		}
	}

	return out, joinIssues(issues)
}

// parseMinutes preserves the observed distinction between unknown and permanent values.
func parseMinutes(s string) (int, error) {
	// The server uses '-' for an unspecified or elapsed duration;
	// it is distinct from the explicit permanent marker below.
	if s == "-" {
		return 0, nil
	}

	if strings.EqualFold(s, "perm") {
		return -1, nil
	}

	return strconv.Atoi(s)
}

// validIP accepts only syntactically valid IPv4 addresses in RV ban rows.
func validIP(s string) bool {
	a, e := netip.ParseAddr(s)
	return e == nil && a.Is4()
}
