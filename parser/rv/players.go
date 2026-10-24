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

// Player is one row in the Real Virtuality response format observed in Arma 3 and DayZ.
// Arma 2 compatibility has not been independently verified.
type Player struct {
	// Parsed endpoint.
	Address netip.AddrPort `json:"address" yaml:"address"`

	// AddressText preserves the original address when parsing fails.
	AddressText string `json:"address_text,omitempty" yaml:"address_text,omitempty"`

	// GUID is the BattlEye identifier text returned by the server.
	GUID string `json:"guid" yaml:"guid"`

	// Name is the display name returned by the server.
	Name string `json:"name" yaml:"name"`

	// ID is the server-assigned player number.
	ID int `json:"id" yaml:"id"`

	// Ping is the reported latency or zero when the response value is invalid.
	Ping int `json:"ping" yaml:"ping"`

	// AddressValid reports whether a returned address parsed successfully.
	AddressValid bool `json:"address_valid" yaml:"address_valid"`

	// GUIDValid reports whether GUID contains a valid BattlEye hexadecimal value.
	GUIDValid bool `json:"guid_valid" yaml:"guid_valid"`

	// PingValid reports whether the response ping value was parsed.
	PingValid bool `json:"ping_valid" yaml:"ping_valid"`

	// Verified reports whether a valid GUID carried the (OK) marker.
	Verified bool `json:"verified" yaml:"verified"`

	// Lobby reports whether the name carried the (Lobby) marker.
	Lobby bool `json:"lobby" yaml:"lobby"`
}

// ParsePlayers parses Real Virtuality player rows
// and preserves identifiable malformed rows while reporting them
// with parser.ErrMalformedRows.
func ParsePlayers(data []byte) ([]Player, error) {
	if !hasLine(data, "Players on server:") {
		return nil, parser.ErrUnrecognized
	}

	var out []Player
	var issues []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Headers and the final count are not candidate player rows,
		// so they should not turn an otherwise valid response into ErrMalformedRows.
		if line == "" || strings.EqualFold(line, "Players on server:") ||
			strings.HasPrefix(line, "[#]") || strings.Trim(line, "-") == "" || isFooter(line) {
			continue
		}

		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}

		// Skip obvious non-numeric IDs before Atoi to avoid errors for headers and other non-row lines.
		first := f[0][0]
		if (first < '0' || first > '9') && first != '+' && first != '-' {
			continue
		}

		id, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}

		addr, addressErr := parseAddrPort(f[1])
		pingValue, pingErr := strconv.ParseUint(f[2], 10, 16)
		pingValid := pingErr == nil
		ping := int(pingValue)

		// Keep the row because its ID, address, GUID, and name are still useful;
		// PingValid distinguishes the fallback zero from a reported zero ping.
		if !pingValid {
			ping = 0
			issues = append(issues, fmt.Sprintf("player %d: invalid ping", id))
		}

		if addressErr != nil {
			issues = append(issues, fmt.Sprintf("player %d: invalid address", id))
		}
		addressText := ""
		if addressErr != nil {
			addressText = f[1]
		}

		guidIndex := -1
		verified := false

		// Names may contain spaces and older rows can omit optional fields.
		// Find the first valid GUID token so the name starts after the identity, not at a column.
		for i := 3; i < len(f); i++ {
			tok := f[i]
			okMarker := false
			markerStart := strings.LastIndexByte(tok, '(')
			if markerStart > 0 && strings.HasSuffix(tok, ")") {
				// The RV response may attach a status marker to the GUID.
				// Keep the GUID valid even when the marker is not the recognized OK state.
				marker := tok[markerStart+1 : len(tok)-1]
				tok = tok[:markerStart]
				okMarker = strings.EqualFold(marker, "OK")
			}

			if isGUID(tok) {
				guidIndex = i
				verified = okMarker
				f[i] = tok
				break
			}
		}

		guid := ""
		guidValid := guidIndex >= 0

		switch {
		case guidValid:
			guid = f[guidIndex]

		case len(f) > 3:
			// Preserve the first expected GUID position as raw input when no valid token exists;
			// this keeps the remainder available as the player's name.
			guidIndex = 3
			guid = strings.TrimSuffix(f[guidIndex], "(OK)")
			issues = append(issues, fmt.Sprintf("player %d: invalid or missing GUID", id))

		default:
			guidIndex = len(f)
			issues = append(issues, fmt.Sprintf("player %d: missing GUID", id))
		}

		nameStart := min(guidIndex+1, len(f))
		name := strings.Join(f[nameStart:], " ")
		lobby := strings.HasSuffix(name, "(Lobby)")

		// The marker is protocol metadata rather than part of the display name.
		if lobby {
			name = strings.TrimSpace(strings.TrimSuffix(name, "(Lobby)"))
		}

		out = append(out, Player{
			ID:           id,
			Address:      addr,
			AddressText:  addressText,
			Ping:         ping,
			PingValid:    pingValid,
			AddressValid: addressErr == nil,
			GUID:         guid,
			GUIDValid:    guidValid,
			Name:         name,
			Verified:     verified && guidValid,
			Lobby:        lobby,
		})
	}

	return out, joinIssues(issues)
}

// isFooter recognizes the parenthesized player-count line.
func isFooter(line string) bool {
	return strings.HasPrefix(line, "(") && strings.Contains(line, " players in total)")
}
