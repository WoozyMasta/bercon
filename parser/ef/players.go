// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/woozymasta/bercon/parser"
)

// Player is one player row in the semicolon-separated Enfusion response format.
type Player struct {
	// IdentityID is the account identity returned by the server.
	IdentityID string `json:"identity_id" yaml:"identity_id"`

	// Name is the player's display name; it may contain semicolons.
	Name string `json:"name" yaml:"name"`

	// ID is the server-assigned player number.
	ID int `json:"id" yaml:"id"`

	// IdentityIDValid reports whether IdentityID is present.
	IdentityIDValid bool `json:"identity_id_valid" yaml:"identity_id_valid"`
}

// ParsePlayers parses Enfusion player rows and preserves identifiable malformed rows.
// Partial results are reported with parser.ErrMalformedRows.
func ParsePlayers(data []byte) ([]Player, error) {
	const header = "Players on server: [Player#] ; [Player UID] ; [Player Name]"
	if !hasLine(data, header) {
		return nil, parser.ErrUnrecognized
	}

	var players []Player
	var issues []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, header) || isFooter(line) {
			continue
		}

		id, ok := playerID(line)
		if !ok {
			continue
		}

		player, issue := parsePlayer(line, id)
		players = append(players, player)
		if issue != "" {
			issues = append(issues, issue)
		}
	}

	return players, joinIssues(issues)
}

// playerID recognizes rows by their numeric first column and semicolon delimiter.
func playerID(line string) (int, bool) {
	firstColumn, _, found := strings.Cut(line, ";")
	if !found {
		return 0, false
	}

	id, err := strconv.Atoi(strings.TrimSpace(firstColumn))
	return id, err == nil
}

// parsePlayer keeps a display name intact by splitting only at the first two delimiters.
func parsePlayer(line string, id int) (Player, string) {
	columns := strings.SplitN(line, ";", 3)
	if len(columns) != 3 {
		return Player{ID: id}, fmt.Sprintf("player %d: incomplete Enfusion row", id)
	}

	identityID := strings.TrimSpace(columns[1])
	player := Player{
		ID:              id,
		IdentityID:      identityID,
		IdentityIDValid: identityID != "",
		Name:            strings.TrimSpace(columns[2]),
	}
	if !player.IdentityIDValid {
		return player, fmt.Sprintf("player %d: missing identity", id)
	}

	return player, ""
}

// isFooter recognizes the parenthesized player-count line.
func isFooter(line string) bool {
	return strings.HasPrefix(line, "(") && strings.Contains(line, " players in total)")
}
