// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package wire

import (
	"bytes"
	"testing"
)

func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		{0x42, 0x45, 0x28, 0xec, 0xc5, 0x2f, 0xff, 0x01, 0x01},
		{0x42, 0x45, 0x7f, 0xab, 0x3d, 0xac, 0xff, 0x00, 't', 'e', 's', 't'},
		{0x42, 0x45, 0xcb, 0xd3, 0x49, 0xd3, 0xff, 0x01, 0x00, 0x00, 0x02, 0x00, 'a', 'b', 'c'},
		{},
	}
	for _, size := range []int{
		0,
		MaxCommandBodyBytes,
		MaxCommandBodyBytes + 1,
		MaxSingleResponsePayload,
		MaxSingleResponsePayload + 1,
	} {
		payload := bytes.Repeat([]byte{0x80}, size)
		if packet, err := Encode(Packet{Kind: commandKind, Seq: 255, Data: payload}); err == nil {
			seeds = append(seeds, packet)
		}
	}
	for _, packet := range []Packet{
		{Kind: commandKind, Seq: 0, Parts: 1, Part: 0},
		{Kind: commandKind, Seq: 255, Parts: 255, Part: 254, Data: []byte{0, 0xff}},
		{Kind: messageKind, Seq: 255, Data: []byte{0x00, 0x80, 0xff}},
	} {
		if raw, err := Encode(packet); err == nil {
			seeds = append(seeds, raw)
		}
	}

	for _, seed := range seeds {
		f.Add(seed)
		for end := range len(seed) {
			f.Add(append([]byte(nil), seed[:end]...))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := Decode(raw)
		if err != nil {
			return
		}
		if p.Kind > messageKind || (p.Parts != 0 && (p.Kind != commandKind || p.Part >= p.Parts)) {
			t.Fatalf("invalid decoded packet: %#v", p)
		}
	})
}

// FuzzEncodeDecodeRoundTrip preserves arbitrary bytes for all representable packet kinds.
func FuzzEncodeDecodeRoundTrip(f *testing.F) {
	f.Add(uint8(0), uint8(0), uint8(0), uint8(0), []byte{0, 0xff, 'x'})
	f.Add(uint8(1), uint8(7), uint8(0), uint8(0), []byte{0x80, 0xff, 'x'})
	f.Add(uint8(1), uint8(8), uint8(3), uint8(2), []byte("fragment"))
	f.Add(uint8(2), uint8(9), uint8(0), uint8(0), []byte{0, 0xff})
	f.Fuzz(func(t *testing.T, kind, seq, parts, part uint8, data []byte) {
		packetKind := kind % 3
		packet := Packet{Kind: packetKind, Seq: seq, Data: data}
		if packetKind == loginKind {
			packet.Seq = 0
		} else if packetKind == commandKind && parts != 0 {
			packet.Parts = parts
			packet.Part = part % packet.Parts
		}

		// A single command response beginning with 0x00 is ambiguous
		// with the protocol's multipart marker and is not structurally representable.
		if packet.Kind == commandKind && packet.Parts == 0 && len(data) > 0 && data[0] == multipart {
			return
		}

		raw, err := Encode(packet)
		if err != nil {
			t.Fatalf("valid packet was rejected: %#v: %v", packet, err)
		}
		decoded, err := Decode(raw)
		if err != nil {
			t.Fatalf("encoded packet did not decode: %v", err)
		}
		if decoded.Kind != packet.Kind || decoded.Seq != packet.Seq ||
			decoded.Parts != packet.Parts || decoded.Part != packet.Part ||
			!bytes.Equal(decoded.Data, packet.Data) {
			t.Fatalf("round trip changed packet: got %#v want %#v", decoded, packet)
		}
	})
}
