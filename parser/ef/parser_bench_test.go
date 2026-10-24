// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package ef

import "testing"

// benchmarkPlayers is a representative Enfusion player response.
var benchmarkPlayers = []byte("1 ; 8e0a172c-9b12-4f40-85c1-7218dd42a612 ; Example Player\n")

// BenchmarkParsePlayers measures parsing one Enfusion player row.
func BenchmarkParsePlayers(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ParsePlayers(benchmarkPlayers)
	}
}
