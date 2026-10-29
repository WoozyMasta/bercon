// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package rv

import (
	"embed"
	"errors"
	"testing"

	"github.com/woozymasta/bercon/parser"
)

// responseFixtures embeds sample RCon responses used by parser tests.
//
//go:embed testdata/*.txt
var responseFixtures embed.FS

func TestParsePlayers(t *testing.T) {
	guid := "0123456789abcdef0123456789ABCDEF"
	data := []byte("Players on server:\r\n Id IP Address Ping GUID Name\r\n" +
		" 1 127.0.0.1:2302 42 " + guid + "(OK) Player Name (Lobby)\r\n" +
		" 2 127.0.0.2 0 " + guid + " Another players in total name\r\n" +
		" 3 127.0.0.3:2302 5 " + guid + "(PENDING) Unverified marker\r\n" +
		"(3 players in total)\r\n")

	players, err := ParsePlayers(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 3 {
		t.Fatalf("got %d players", len(players))
	}
	if players[0].Name != "Player Name" || !players[0].Verified || !players[0].Lobby || players[0].Ping != 42 {
		t.Fatalf("unexpected player: %#v", players[0])
	}
	if players[1].Name != "Another players in total name" || players[1].Address.Port() != 0 {
		t.Fatalf("unexpected player: %#v", players[1])
	}
	if !players[2].GUIDValid || players[2].Verified || players[2].GUID != guid {
		t.Fatalf("non-OK status marker changed GUID validity: %#v", players[2])
	}
}

func TestParsePartialRowsAndAdmins(t *testing.T) {
	admins, err := ParseAdmins([]byte("Connected RCon admins:\n ID IP\n0 192.0.2.1:2302\n1 bad-address\n"))
	if !errors.Is(err, parser.ErrMalformedRows) || len(admins) != 2 || admins[0].ID != 0 ||
		admins[1].AddressValid || admins[1].AddressText != "bad-address" {
		t.Fatalf("admins=%#v err=%v", admins, err)
	}
}

func TestParseBans(t *testing.T) {
	guid := "0123456789abcdef0123456789abcdef"
	data := []byte("GUID Bans:\nID GUID TIME Reason\n2 " + guid + " perm rule violation\n" +
		"\nIP Bans:\nID IP TIME Reason\n7 192.0.2.5 - griefing\n")
	bans, err := ParseBans(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(bans.GUID) != 1 || bans.GUID[0].Minutes != -1 || bans.GUID[0].Reason != "rule violation" {
		t.Fatalf("GUID bans: %#v", bans.GUID)
	}
	if len(bans.IP) != 1 || bans.IP[0].ID != 7 || bans.IP[0].Reason != "griefing" {
		t.Fatalf("IP bans: %#v", bans.IP)
	}
}

// TestResponseFixtures checks clean RV response formats
// against captured DayZ samples and populated parser cases.
func TestResponseFixtures(t *testing.T) {
	playersData, err := responseFixtures.ReadFile("testdata/players.txt")
	if err != nil {
		t.Fatal(err)
	}
	players, err := ParsePlayers(playersData)
	if err != nil || len(players) != 1 {
		t.Fatalf("players=%#v err=%v; want one valid player", players, err)
	}
	if players[0].Name != "Player Name" || players[0].Ping != 42 || !players[0].Verified || !players[0].Lobby {
		t.Fatalf("unexpected player: %#v", players[0])
	}

	emptyPlayersData, err := responseFixtures.ReadFile("testdata/players_empty.txt")
	if err != nil {
		t.Fatal(err)
	}
	emptyPlayers, err := ParsePlayers(emptyPlayersData)
	if err != nil || len(emptyPlayers) != 0 {
		t.Fatalf("empty players=%#v err=%v", emptyPlayers, err)
	}

	adminsData, err := responseFixtures.ReadFile("testdata/admins.txt")
	if err != nil {
		t.Fatal(err)
	}
	admins, err := ParseAdmins(adminsData)
	if err != nil || len(admins) != 1 || !admins[0].AddressValid || admins[0].ID != 0 {
		t.Fatalf("admins=%#v err=%v", admins, err)
	}

	bansData, err := responseFixtures.ReadFile("testdata/bans.txt")
	if err != nil {
		t.Fatal(err)
	}
	bans, err := ParseBans(bansData)
	if err != nil || len(bans.GUID) != 1 || len(bans.IP) != 1 {
		t.Fatalf("bans=%#v err=%v", bans, err)
	}
	if bans.GUID[0].Subject != "00000000000000000000000000000001" ||
		bans.GUID[0].Minutes != 60 || bans.GUID[0].Reason != "bercon fixture GUID ban" {
		t.Fatalf("unexpected GUID ban: %#v", bans.GUID[0])
	}
	if bans.IP[0].Subject != "192.0.2.1" || bans.IP[0].Minutes != -1 ||
		bans.IP[0].Reason != "Test IP ban" {
		t.Fatalf("unexpected IP ban: %#v", bans.IP[0])
	}

	emptyBansData, err := responseFixtures.ReadFile("testdata/bans_empty.txt")
	if err != nil {
		t.Fatal(err)
	}
	emptyBans, err := ParseBans(emptyBansData)
	if err != nil || len(emptyBans.GUID) != 0 || len(emptyBans.IP) != 0 {
		t.Fatalf("empty bans=%#v err=%v", emptyBans, err)
	}
}

// TestRecognitionContract checks RV schema markers against empty and foreign payloads.
func TestRecognitionContract(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := responseFixtures.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	adminsEmpty := []byte("Connected RCon admins:\n[#] [IP Address]:[Port]\n-----------------------------\n")
	message := []byte("Server restarted; welcome back")
	players := read("players.txt")
	playersEmpty := read("players_empty.txt")
	admins := read("admins.txt")
	bans := read("bans.txt")
	bansEmpty := read("bans_empty.txt")
	efPlayers := []byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n" +
		"1 ; 8e0a172c-9b12-4f40-85c1-7218dd42a612 ; Example Player\n")
	efBans := []byte("Total bans: 1 | Page: 1/1\n- Identity Id | Banned name\n" +
		"- 8e0a172c-9b12-4f40-85c1-7218dd42a612 | Example Player\n")

	tests := []struct {
		name  string
		parse func([]byte) error
		valid [][]byte
		bad   [][]byte
	}{
		{
			name:  "players",
			parse: func(data []byte) error { _, err := ParsePlayers(data); return err },
			valid: [][]byte{players, playersEmpty},
			bad:   [][]byte{admins, bans, efPlayers, efBans, []byte("OK"), nil, message},
		},
		{
			name:  "admins",
			parse: func(data []byte) error { _, err := ParseAdmins(data); return err },
			valid: [][]byte{admins, adminsEmpty},
			bad:   [][]byte{players, playersEmpty, bans, efPlayers, efBans, []byte("OK"), nil, message},
		},
		{
			name:  "bans",
			parse: func(data []byte) error { _, err := ParseBans(data); return err },
			valid: [][]byte{bans, bansEmpty},
			bad:   [][]byte{players, admins, efPlayers, efBans, []byte("OK"), nil, message},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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

// TestMalformedRecognizedResponses checks partial results for each RV response schema.
func TestMalformedRecognizedResponses(t *testing.T) {
	players, playersErr := ParsePlayers([]byte("Players on server:\n[#] [IP Address]:[Port] [Ping] [GUID] [Name]\n" +
		"1 invalid-endpoint nope invalid-guid Broken Player\n"))
	if !errors.Is(playersErr, parser.ErrMalformedRows) || errors.Is(playersErr, parser.ErrUnrecognized) || len(players) != 1 {
		t.Fatalf("players=%#v err=%v; want retained malformed row", players, playersErr)
	}

	admins, adminsErr := ParseAdmins([]byte("Connected RCon admins:\n[#] [IP Address]:[Port]\n" +
		"1 invalid-endpoint\n"))
	if !errors.Is(adminsErr, parser.ErrMalformedRows) || errors.Is(adminsErr, parser.ErrUnrecognized) || len(admins) != 1 {
		t.Fatalf("admins=%#v err=%v; want retained malformed row", admins, adminsErr)
	}

	bans, bansErr := ParseBans([]byte("GUID Bans:\n[#] [GUID] [Minutes left] [Reason]\n" +
		"1 invalid-guid invalid-time test reason\n"))
	if !errors.Is(bansErr, parser.ErrMalformedRows) || errors.Is(bansErr, parser.ErrUnrecognized) || len(bans.GUID) != 1 {
		t.Fatalf("bans=%#v err=%v; want retained malformed row", bans, bansErr)
	}
}

// TestParseBansDetectsTruncatedSections keeps incomplete RCon ban lists from appearing complete.
func TestParseBansDetectsTruncatedSections(t *testing.T) {
	tests := []struct {
		name string
		data string
		guid int
		ip   int
	}{
		{
			name: "missing IP section",
			data: "GUID Bans:\n[#] [GUID] [Minutes left] [Reason]\n" +
				"0 00000000000000000000000000000001 perm test\n",
			guid: 1,
		},
		{
			name: "partial final row",
			data: "GUID Bans:\n[#] [GUID] [Minutes left] [Reason]\n" +
				"0 00000000000000000000000000000001 perm test\n" +
				"IP Bans:\n[#] [IP Address] [Minutes left] [Reason]\n" +
				"1 192.0.2.1 perm test\n2 192.0.2.",
			guid: 1,
			ip:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bans, err := ParseBans([]byte(test.data))
			if !errors.Is(err, parser.ErrMalformedRows) {
				t.Fatalf("ParseBans() error = %v, want malformed response", err)
			}
			if len(bans.GUID) != test.guid || len(bans.IP) != test.ip {
				t.Fatalf("ParseBans() retained %d GUID and %d IP rows, want %d and %d",
					len(bans.GUID), len(bans.IP), test.guid, test.ip)
			}
		})
	}
}

func FuzzParsers(f *testing.F) {
	f.Add([]byte("Players on server:\n"))
	f.Add([]byte("GUID Bans:\n"))
	f.Add([]byte("Connected RCon admins:\n"))
	f.Add([]byte("OK"))
	f.Add([]byte("Server restarted; welcome back"))
	f.Add([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n"))
	f.Add([]byte("Server has no bans to list."))
	f.Add([]byte("0 0 0"))
	f.Add([]byte{0xff, 0, 1})
	// Keep malformed samples in the fuzz corpus rather than normal fixtures so
	// response files remain representative of data callers can receive normally.
	f.Add([]byte("Connected RCon admins:\nID IP\nц 192.0.2.1:2302\n" +
		"1 bad-address\n"))
	f.Add([]byte("GUID Bans:\nID GUID TIME Reason\nц invalid - bad row\n" +
		"1 11111111111122222222222223333333 - some bad guy\n" +
		"IP Bans:\nID IP TIME Reason\n2 8.8.8.8 invalid reason\n" +
		"3 10.10.10.10 - bad address\n"))
	f.Add([]byte("Players on server:\n" +
		"3 192.0.2.1:2 x 48032258807176771690632755883357(OK) NonNumericPing\n" +
		"4 192.0.2.2:3 99999 48032258807176771690632755883357(OK) OverflowPing\n" +
		"5 bad:port 10 4803225880717677169063275588335(OK) ShortGuid\n"))
	for _, name := range []string{
		"testdata/players.txt",
		"testdata/players_empty.txt",
		"testdata/admins.txt",
		"testdata/bans.txt",
		"testdata/bans_empty.txt",
		"testdata/commands.txt",
		"testdata/missions_empty.txt",
		"testdata/version.txt",
	} {
		fixture, err := responseFixtures.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(fixture)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParsePlayers(data)
		_, _ = ParseAdmins(data)
		_, _ = ParseBans(data)
	})
}
