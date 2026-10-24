// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef_test

import (
	"fmt"

	"github.com/woozymasta/bercon/parser/ef"
)

func ExampleParsePlayers() {
	players, err := ef.ParsePlayers([]byte("Players on server: [Player#] ; [Player UID] ; [Player Name]\n" +
		"1 ; 8e0a172c-9b12-4f40-85c1-7218dd42a612 ; Example Player\n"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("id=%d identity=%s name=%s\n",
		players[0].ID, players[0].IdentityID, players[0].Name)

	// Output: id=1 identity=8e0a172c-9b12-4f40-85c1-7218dd42a612 name=Example Player
}

func ExampleParseBans() {
	response := []byte("Total bans: 1 | Page: 1/1\n" +
		"- Identity Id | Banned name\n" +
		"- 8e0a172c-9b12-4f40-85c1-7218dd42a612 | Example Player\n")
	bans, err := ef.ParseBans(response)
	if err != nil {
		panic(err)
	}

	fmt.Printf("identity=%s name=%s\n", bans[0].IdentityID, bans[0].BannedName)

	// Output: identity=8e0a172c-9b12-4f40-85c1-7218dd42a612 name=Example Player
}
