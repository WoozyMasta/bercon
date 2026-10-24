// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"testing"
)

func TestFixedVectors(t *testing.T) {
	cases := []struct {
		name, hex string
		packet    Packet
	}{
		{
			"login", "42457fab3dacff0074657374",
			Packet{Kind: 0, Data: []byte("test")},
		},
		{
			"login success", "424569ddde36ff0001",
			Packet{Kind: 0, Data: []byte{1}},
		},
		{
			"login failure", "4245ffedd941ff0000",
			Packet{Kind: 0, Data: []byte{0}},
		},
		{
			"command", "4245f93794aeff0100706c6179657273",
			Packet{Kind: 1, Data: []byte("players")},
		},
		{
			"empty command", "424528ecc52fff0101",
			Packet{Kind: 1, Seq: 1},
		},
		{
			"ack", "4245de1a8bedff0207",
			Packet{Kind: 2, Seq: 7},
		},
		{
			"message", "4245ccd64a3aff02076869",
			Packet{Kind: 2, Seq: 7, Data: []byte("hi")},
		},
		{
			"response", "4245269c1522ff01004f4b",
			Packet{Kind: 1, Data: []byte("OK")},
		},
		{
			"empty response", "4245bedcc258ff0100",
			Packet{Kind: 1},
		},
		{
			"part 0", "4245cbd349d3ff0100000200616263",
			Packet{Kind: 1, Parts: 2, Data: []byte("abc")},
		},
		{
			"part 1", "42450d141552ff0100000201646566",
			Packet{Kind: 1, Parts: 2, Part: 1, Data: []byte("def")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, _ := hex.DecodeString(tc.hex)
			got, err := Encode(tc.packet)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != tc.hex {
				t.Fatalf("encode %x", got)
			}

			p, err := Decode(want)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != tc.packet.Kind ||
				p.Seq != tc.packet.Seq ||
				p.Parts != tc.packet.Parts ||
				p.Part != tc.packet.Part ||
				string(p.Data) != string(tc.packet.Data) {
				t.Fatalf("decode %#v", p)
			}
		})
	}
}

func TestDecodeRejectsCorruptionAndShortPackets(t *testing.T) {
	valid, _ := hex.DecodeString("424528ecc52fff0101")
	for n := range len(valid) {
		if _, err := Decode(valid[:n]); err == nil {
			t.Fatalf("accepted length %d", n)
		}
	}

	bad := append([]byte(nil), valid...)
	bad[len(bad)-1] ^= 1
	if _, err := Decode(bad); !errors.Is(err, ErrCRC) {
		t.Fatalf("got %v", err)
	}
}

func TestCommandBoundary(t *testing.T) {
	for _, n := range []int{
		MaxSingleResponsePayload - 1,
		MaxSingleResponsePayload,
		MaxSingleResponsePayload + 1,
		MaxMultipartResponsePayload - 1,
		MaxMultipartResponsePayload,
		MaxMultipartResponsePayload + 1,
	} {
		b, err := Encode(Packet{Kind: 1, Data: make([]byte, n)})
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != CommandHeaderBytes+n {
			t.Fatalf("length %d", len(b))
		}
	}
}

func TestDecodeAcceptsValidDatagramsAboveCanonicalOutboundLimit(t *testing.T) {
	payload := bytes.Repeat([]byte{0x80}, CanonicalMaxDatagramBytes+1-CommandHeaderBytes)
	raw, err := Encode(Packet{Kind: commandKind, Seq: 19, Data: payload})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= CanonicalMaxDatagramBytes {
		t.Fatalf("test packet length=%d, want above canonical limit", len(raw))
	}

	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode rejected a valid non-canonical inbound packet: %v", err)
	}
	if got.Seq != 19 || !bytes.Equal(got.Data, payload) {
		t.Fatal("Decode changed the oversized packet payload")
	}
}

func TestRoundTripPreservesNonUTF8Payload(t *testing.T) {
	want := []byte{0x00, 0x80, 0xff, 0xc3, 0x28}
	raw, err := Encode(Packet{Kind: messageKind, Seq: 7, Data: want})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, want) {
		t.Fatalf("payload changed: got %x want %x", got.Data, want)
	}
}

func TestDecodeRejectsMalformedMultipartMetadata(t *testing.T) {
	valid, err := Encode(Packet{
		Kind:  commandKind,
		Seq:   1,
		Parts: 2,
		Part:  1,
		Data:  []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "zero part count",
			mutate: func(packet []byte) []byte {
				packet[10] = 0
				return packet
			},
		},
		{
			name: "part outside count",
			mutate: func(packet []byte) []byte {
				packet[11] = packet[10]
				return packet
			},
		},
		{
			name: "truncated metadata",
			mutate: func(packet []byte) []byte {
				return packet[:10]
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := tc.mutate(append([]byte(nil), valid...))
			if len(packet) >= 8 {
				// Keep checksum validation out of this test so it targets metadata parsing.
				binary.LittleEndian.PutUint32(packet[2:6], crc32.ChecksumIEEE(packet[6:]))
			}
			if _, err := Decode(packet); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Decode error = %v, want malformed packet", err)
			}
		})
	}
}
