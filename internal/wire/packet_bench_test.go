// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package wire

import (
	"strconv"
	"testing"
)

var (
	benchmarkPacket  []byte // benchmarkPacket prevents command encoding from being optimized away.
	benchmarkDecoded Packet // benchmarkDecoded retains the latest decoded value during benchmarks.
)

func BenchmarkEncodeCommand(b *testing.B) {
	for _, size := range []int{12, MaxCommandBodyBytes} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			p := Packet{Kind: commandKind, Seq: 1, Data: make([]byte, size)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkPacket, _ = Encode(p)
			}
		})
	}
}

// BenchmarkEncodeLogin measures the small setup packet used by each connection.
func BenchmarkEncodeLogin(b *testing.B) {
	packet := Packet{Kind: loginKind, Data: []byte("benchmark-password")}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkPacket, _ = Encode(packet)
	}
}

func BenchmarkDecodeResponse(b *testing.B) {
	for _, size := range []int{16, MaxSingleResponsePayload} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			packet, _ := Encode(Packet{Kind: commandKind, Seq: 2, Data: make([]byte, size)})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkDecoded, _ = Decode(packet)
			}
		})
	}
}

// BenchmarkDecodeMultipart measures checksum validation and payload copying at the wire limit.
func BenchmarkDecodeMultipart(b *testing.B) {
	packet, err := Encode(Packet{
		Kind: commandKind, Seq: 2, Parts: 2, Part: 1,
		Data: make([]byte, CanonicalMaxDatagramBytes-MultipartHeaderBytes),
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDecoded, _ = Decode(packet)
	}
}
