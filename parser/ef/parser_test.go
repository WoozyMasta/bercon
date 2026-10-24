// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef

import (
	"embed"
	"errors"
	"testing"

	"github.com/woozymasta/bercon/parser"
)

// responseFixtures embeds captured Enfusion-format responses for parser tests.
//
//go:embed testdata/*.txt
var responseFixtures embed.FS

func TestParsePlayers(t *testing.T) {
	data, err := responseFixtures.ReadFile("testdata/players.txt")
	if err != nil {
		t.Fatal(err)
	}

	players, err := ParsePlayers(data)
	if err != nil || len(players) != 1 {
		t.Fatalf("players=%#v err=%v; want one player", players, err)
	}
	player := players[0]
	if player.ID != 1 || player.IdentityID != "8e0a172c-9b12-4f40-85c1-7218dd42a612" ||
		!player.IdentityIDValid || player.Name != "Example Player" {
		t.Fatalf("unexpected Enfusion player: %#v", player)
	}
}

func TestParsePlayersPreservesSemicolonsAndMalformedRows(t *testing.T) {
	players, err := ParsePlayers([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n" +
		"2 ; another-identity ; Woozy; Masta\n"))
	if err != nil || len(players) != 1 || players[0].Name != "Woozy; Masta" {
		t.Fatalf("semicolon in player name was split: %#v err=%v", players, err)
	}

	malformed, err := ParsePlayers([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n" +
		"3 ; ; Player Without Identity\n"))
	if !errors.Is(err, parser.ErrMalformedRows) || len(malformed) != 1 || malformed[0].IdentityIDValid {
		t.Fatalf("missing identity was not preserved as malformed: %#v err=%v", malformed, err)
	}
}

func TestParseBans(t *testing.T) {
	data, err := responseFixtures.ReadFile("testdata/bans.txt")
	if err != nil {
		t.Fatal(err)
	}

	bans, err := ParseBans(data)
	if err != nil || len(bans) != 1 {
		t.Fatalf("bans=%#v err=%v; want one ban", bans, err)
	}
	if bans[0].IdentityID != "8e0a172c-9b12-4f40-85c1-7218dd42a612" ||
		!bans[0].IdentityIDValid || bans[0].BannedName != "Example Player" {
		t.Fatalf("unexpected Enfusion ban: %#v", bans[0])
	}
}

func TestParseEmptyBanList(t *testing.T) {
	data, err := responseFixtures.ReadFile("testdata/bans_empty.txt")
	if err != nil {
		t.Fatal(err)
	}

	bans, err := ParseBans(data)
	if err != nil || len(bans) != 0 {
		t.Fatalf("bans=%#v err=%v; want no bans", bans, err)
	}
}

func TestParseMalformedBan(t *testing.T) {
	data := []byte("Total bans: 1 | Page: 1/1\n" +
		"- Identity Id | Banned name\n- invalid-id | Unknown\n")
	bans, err := ParseBans(data)
	if !errors.Is(err, parser.ErrMalformedRows) || len(bans) != 1 || bans[0].IdentityIDValid {
		t.Fatalf("malformed ban was not preserved: %#v err=%v", bans, err)
	}
}

// TestRecognitionContract checks EF schema markers against empty and foreign payloads.
func TestRecognitionContract(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := responseFixtures.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	players := read("players.txt")
	bans := read("bans.txt")
	bansEmpty := read("bans_empty.txt")
	playersEmpty := []byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n")
	rvPlayers := []byte("Players on server:\n[#] [IP Address]:[Port] [Ping] [GUID] [Name]\n")
	rvBans := []byte("GUID Bans:\n[#] [GUID] [Minutes left] [Reason]\n")
	rvAdmins := []byte("Connected RCon admins:\n[#] [IP Address]:[Port]\n")
	message := []byte("Server restarted; welcome back")

	for name, test := range map[string]struct {
		parse func([]byte) error
		valid [][]byte
		bad   [][]byte
	}{
		"players": {
			parse: func(data []byte) error { _, err := ParsePlayers(data); return err },
			valid: [][]byte{players, playersEmpty},
			bad:   [][]byte{bans, bansEmpty, rvPlayers, rvAdmins, rvBans, []byte("OK"), nil, message},
		},
		"bans": {
			parse: func(data []byte) error { _, err := ParseBans(data); return err },
			valid: [][]byte{bans, bansEmpty},
			bad:   [][]byte{players, playersEmpty, rvPlayers, rvAdmins, rvBans, []byte("OK"), nil, message},
		},
	} {
		t.Run(name, func(t *testing.T) {
			for index, data := range test.valid {
				if err := test.parse(data); err != nil {
					t.Errorf("valid response %d: %v", index, err)
				}
			}
			for index, data := range test.bad {
				if err := test.parse(data); !errors.Is(err, parser.ErrUnrecognized) ||
					errors.Is(err, parser.ErrMalformedRows) {
					t.Errorf("unrecognized response %d: %v", index, err)
				}
			}
		})
	}
}

// TestMalformedRecognizedResponses checks partial results for each EF response schema.
func TestMalformedRecognizedResponses(t *testing.T) {
	players, playersErr := ParsePlayers([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n" +
		"3 ; ; Player Without Identity\n"))
	if !errors.Is(playersErr, parser.ErrMalformedRows) ||
		errors.Is(playersErr, parser.ErrUnrecognized) || len(players) != 1 {
		t.Fatalf("players=%#v err=%v; want retained malformed row", players, playersErr)
	}

	bans, bansErr := ParseBans([]byte("Total bans: 1 | Page: 1/1\n- Identity Id | Banned name\n" +
		"- invalid-id | Unknown Player\n"))
	if !errors.Is(bansErr, parser.ErrMalformedRows) ||
		errors.Is(bansErr, parser.ErrUnrecognized) || len(bans) != 1 {
		t.Fatalf("bans=%#v err=%v; want retained malformed row", bans, bansErr)
	}
}

func FuzzParsers(f *testing.F) {
	f.Add([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n"))
	f.Add([]byte("Players on server:\n[#] [IP Address]:[Port] [Ping] [GUID] [Name]\n"))
	f.Add([]byte("Connected RCon admins:\n[#] [IP Address]:[Port]\n"))
	f.Add([]byte("GUID Bans:\n[#] [GUID] [Minutes left] [Reason]\n"))
	f.Add([]byte("OK"))
	f.Add([]byte("Server restarted; welcome back"))
	f.Add([]byte("1 ; 8e0a172c-9b12-4f40-85c1-7218dd42a612 ; Example Player\n"))
	f.Add([]byte("Total bans: 0 | Page: 1/1\nServer has no bans to list.\n"))
	f.Add([]byte("Total bans: 1 | Page: 1/1\n- Identity Id | Banned name\n- invalid | Unknown\n"))
	for _, name := range []string{"testdata/players.txt", "testdata/bans.txt", "testdata/bans_empty.txt"} {
		fixture, err := responseFixtures.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(fixture)
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParsePlayers(data)
		_, _ = ParseBans(data)
	})
}
