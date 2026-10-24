// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package rv

import "testing"

// benchmarkPlayers is a representative RV players response.
var benchmarkPlayers = []byte(`Players on server:
ID IP PING GUID NAME
1 192.0.2.1:2302 42 0123456789abcdef0123456789abcdef Example Player
`)

// benchmarkAdmins is one representative RV administrator row.
var benchmarkAdmins = []byte(`
Connected RCon admins:
ID IP
0 192.0.2.1:2302
`)

// benchmarkBans contains one representative row from each RV ban section.
var benchmarkBans = []byte(`GUID Bans:
ID GUID Minutes Reason
0 0123456789abcdef0123456789abcdef perm test
IP Bans:\nID IP Address Minutes Reason
1 192.0.2.2 30 test
`)

func BenchmarkParsePlayers(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParsePlayers(benchmarkPlayers)
	}
}

// BenchmarkParseAdmins measures parsing one administrator row.
func BenchmarkParseAdmins(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParseAdmins(benchmarkAdmins)
	}
}

// BenchmarkParseBans measures parsing both RV ban sections.
func BenchmarkParseBans(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParseBans(benchmarkBans)
	}
}
