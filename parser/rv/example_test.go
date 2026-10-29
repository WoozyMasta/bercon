// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package rv_test

import (
	"fmt"

	"github.com/woozymasta/bercon/parser/rv"
)

func ExampleParsePlayers() {
	players, err := rv.ParsePlayers([]byte("Players on server:\n" +
		"1 127.0.0.1:2302 35 0123456789abcdef0123456789abcdef(OK) Alice (Lobby)"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%d %s verified=%t lobby=%t\n",
		players[0].ID, players[0].Name, players[0].Verified, players[0].Lobby)

	// Output:
	// 1 Alice verified=true lobby=true
}

func ExampleParseAdmins() {
	admins, err := rv.ParseAdmins([]byte("Connected RCon admins:\n0 127.0.0.1:2302\n"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("id=%d address=%s\n", admins[0].ID, admins[0].Address)

	// Output: id=0 address=127.0.0.1:2302
}

func ExampleParseBans() {
	bans, err := rv.ParseBans([]byte(
		"GUID Bans:\n0 0123456789abcdef0123456789abcdef perm cheating\n" +
			"IP Bans:\n",
	))
	if err != nil {
		panic(err)
	}

	fmt.Printf("permanent=%t reason=%s\n", bans.GUID[0].Minutes == -1, bans.GUID[0].Reason)

	// Output: permanent=true reason=cheating
}
