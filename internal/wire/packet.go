// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

// Package wire implements the private BattlEye RCon v2 datagram codec.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	// CanonicalMaxDatagramBytes follows observed BattlEye RCon packet limits.
	CanonicalMaxDatagramBytes = 1400
	// CommandHeaderBytes includes the fixed header and command sequence.
	CommandHeaderBytes = 9
	// MultipartHeaderBytes includes the command header and multipart metadata.
	MultipartHeaderBytes = 12
	// MaxCommandBodyBytes is the largest canonical command payload.
	MaxCommandBodyBytes = CanonicalMaxDatagramBytes - CommandHeaderBytes
	// MaxSingleResponsePayload is the largest canonical unfragmented response body.
	MaxSingleResponsePayload = CanonicalMaxDatagramBytes - CommandHeaderBytes
	// MaxMultipartResponsePayload is the largest canonical multipart fragment body.
	MaxMultipartResponsePayload = CanonicalMaxDatagramBytes - MultipartHeaderBytes
	// MaxUDPDatagramBytes is the largest IPv4 UDP payload size.
	MaxUDPDatagramBytes = 65507
	// MaxUDPReadBufferBytes can receive the largest UDP datagram without truncation.
	MaxUDPReadBufferBytes = 65535
)

const (
	loginKind   byte = 0    // loginKind identifies authentication packets.
	commandKind byte = 1    // commandKind identifies commands and correlated responses.
	messageKind byte = 2    // messageKind identifies independently sequenced server events and ACKs.
	marker      byte = 0xff // marker separates the fixed BattlEye header from the packet kind.
	multipart   byte = 0    // multipart marks fragment metadata after a command sequence.
	headerSize       = 7    // headerSize is the fixed prefix length before the packet kind.
)

var (
	// ErrMalformed marks invalid packet structure.
	ErrMalformed = errors.New("malformed RCon packet")
	// ErrCRC marks a packet whose checksum does not match its bytes.
	ErrCRC = errors.New("RCon packet CRC mismatch")
)

// Packet is a decoded wire packet. Payload bytes are preserved verbatim.
type Packet struct {
	Data  []byte `json:"data"            yaml:"data"`            // Opaque payload bytes.
	Kind  byte   `json:"kind"            yaml:"kind"`            // Login, command, or server-message packet kind.
	Seq   byte   `json:"sequence"        yaml:"sequence"`        // Command or message sequence where required.
	Parts byte   `json:"parts,omitempty" yaml:"parts,omitempty"` // Multipart fragment count.
	Part  byte   `json:"part,omitempty"  yaml:"part,omitempty"`  // Zero-based multipart fragment index.
}

// Encode builds a canonical packet. Parts != 0 selects multipart framing.
func Encode(p Packet) ([]byte, error) {
	// Login has no sequence; multipart framing only applies to command responses.
	if p.Kind > messageKind ||
		(p.Kind == loginKind && (p.Parts != 0 || p.Seq != 0)) ||
		(p.Kind == messageKind && p.Parts != 0) {
		return nil, ErrMalformed
	}

	if p.Parts != 0 && p.Part >= p.Parts {
		return nil, ErrMalformed
	}

	extra := 0
	if p.Kind != loginKind {
		extra++
	}
	if p.Parts != 0 {
		extra += 3
	}

	out := make([]byte, headerSize+1+extra+len(p.Data))
	out[0], out[1], out[6], out[7] = 'B', 'E', marker, p.Kind
	i := 8
	if p.Kind != loginKind {
		out[i] = p.Seq
		i++
	}

	if p.Parts != 0 {
		out[i], out[i+1], out[i+2] = multipart, p.Parts, p.Part
		i += 3
	}

	copy(out[i:], p.Data)

	// The checksum covers marker, kind, framing, and payload but excludes its own four-byte field.
	// Captured packets encode that value in little-endian order.
	binary.LittleEndian.PutUint32(out[2:6], crc32.ChecksumIEEE(out[6:]))

	return out, nil
}

// Decode validates framing and CRC before exposing a packet.
func Decode(raw []byte) (Packet, error) {
	// Check fixed framing before reading the kind or any variable-length fields.
	if len(raw) < 8 || raw[0] != 'B' || raw[1] != 'E' || raw[6] != marker {
		return Packet{}, ErrMalformed
	}
	if binary.LittleEndian.Uint32(raw[2:6]) != crc32.ChecksumIEEE(raw[6:]) {
		return Packet{}, ErrCRC
	}

	p := Packet{Kind: raw[7]}
	if p.Kind > messageKind {
		return Packet{}, fmt.Errorf("%w: unknown kind %d", ErrMalformed, p.Kind)
	}

	i := 8
	if p.Kind != loginKind {
		if i >= len(raw) {
			return Packet{}, ErrMalformed
		}
		p.Seq = raw[i]
		i++
	}

	// A zero byte after command sequence denotes multipart metadata on the wire.
	if p.Kind == commandKind && i < len(raw) && raw[i] == multipart {
		if len(raw)-i < 3 {
			return Packet{}, ErrMalformed
		}

		p.Parts, p.Part = raw[i+1], raw[i+2]
		if p.Parts == 0 || p.Part >= p.Parts {
			return Packet{}, ErrMalformed
		}
		i += 3
	}

	p.Data = append([]byte(nil), raw[i:]...)
	return p, nil
}
