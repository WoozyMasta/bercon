// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

// Message is an independently sequenced server event.
type Message struct {
	Data     []byte `json:"data"     yaml:"data"`     // Opaque event payload bytes.
	Sequence byte   `json:"sequence" yaml:"sequence"` // Independent sequence from the wire packet.
}

// FleetMessage identifies the target that produced one server event.
type FleetMessage struct {
	TargetID string  `json:"target_id" yaml:"target_id"` // Stable Fleet target identifier.
	Message  Message `json:"message"   yaml:"message"`   // Event payload and wire sequence.
}
