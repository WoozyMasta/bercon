// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

// Package testnet contains deterministic UDP fault-injection helpers for tests.
package testnet

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/woozymasta/bercon/internal/wire"
)

// Direction identifies which side sent a datagram through the relay.
type Direction uint8

const (
	// ClientToServer marks datagrams forwarded from client to server.
	ClientToServer Direction = iota
	// ServerToClient marks datagrams forwarded from server to client.
	ServerToClient
)

// Action describes one deterministic datagram transformation.
type Action uint8

const (
	Pass      Action = iota // Pass forwards bytes without modification.
	Drop                    // Drop discards the datagram.
	Duplicate               // Duplicate forwards the datagram twice.
	Corrupt                 // Corrupt flips one bit in the final datagram byte.
	Truncate                // Truncate forwards only the first half of the datagram.
	Delay                   // Delay waits briefly before forwarding the datagram.
	Reorder                 // Reorder swaps each selected datagram with the next datagram in that direction.
	Inject                  // Inject reserves a transform result without forwarding the input datagram.
)

// Transform decides what to do with each datagram. The callback receives a copy.
type Transform func(Direction, []byte, int) Action

// Event records one relay decision without retaining packet contents.
type Event struct {
	Index     int       `json:"index"     yaml:"index"`     // Zero-based datagram index within its direction.
	Size      int       `json:"size"      yaml:"size"`      // Datagram length before any transform is applied.
	Direction Direction `json:"direction" yaml:"direction"` // Side that sent the datagram through the relay.
	Action    Action    `json:"action"    yaml:"action"`    // Transform decision recorded for this datagram.
}

// Relay forwards UDP datagrams between one client and one server.
type Relay struct {
	pending   [2]*queuedDatagram // Held packets for deterministic pairwise reordering.
	conn      *net.UDPConn       // Socket used for both relay directions.
	server    *net.UDPAddr       // Destination endpoint for client datagrams.
	transform Transform          // Deterministic per-datagram test action.
	client    *net.UDPAddr       // Most recent client endpoint.
	closed    chan struct{}      // Signals shutdown to the forwarding loop.
	events    []Event            // Recorded decisions, excluding packet payloads.
	wg        sync.WaitGroup     // Tracks the forwarding loop.
	index     [2]int             // Per-direction datagram counter.
	once      sync.Once          // Ensures the socket closes once.
	mu        sync.Mutex         // Protects peer identity, event log, and reorder buffers.
}

// queuedDatagram stores one packet until a later packet can be sent ahead of it.
type queuedDatagram struct {
	destination *net.UDPAddr // Peer that should receive the retained datagram.
	packet      []byte       // Datagram bytes retained for forwarding.
}

// NewRelay creates and starts a UDP relay bound to an ephemeral local port.
func NewRelay(serverAddress string, transform Transform) (*Relay, error) {
	server, err := net.ResolveUDPAddr("udp", serverAddress)
	if err != nil {
		return nil, err
	}

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}

	r := &Relay{
		conn:      pc,
		server:    server,
		transform: transform,
		closed:    make(chan struct{}),
	}
	r.wg.Add(1)
	go r.run()

	return r, nil
}

// Addr returns the address clients should use to reach the server through relay.
func (r *Relay) Addr() net.Addr {
	return r.conn.LocalAddr()
}

// Events returns a copy of the decisions made so far.
func (r *Relay) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Event(nil), r.events...)
}

// Inject sends a saved datagram toward the selected endpoint without applying Transform.
func (r *Relay) Inject(direction Direction, packet []byte) error {
	r.mu.Lock()

	// Reuse the peer most recently observed in this direction;
	// server-to-client injection is impossible until a client has introduced its endpoint.
	destination := r.server
	if direction == ServerToClient {
		destination = cloneAddr(r.client)
	}

	index := r.index[direction]
	r.index[direction]++
	r.events = append(r.events, Event{
		Direction: direction,
		Index:     index,
		Action:    Inject,
		Size:      len(packet),
	})
	r.mu.Unlock()

	if destination == nil {
		return errors.New("relay has no client endpoint yet")
	}

	_, err := r.conn.WriteToUDP(append([]byte(nil), packet...), destination)
	return err
}

// Close stops the relay and unblocks its UDP reader.
func (r *Relay) Close() error {
	var err error
	r.once.Do(func() { close(r.closed); err = r.conn.Close(); r.wg.Wait() })
	if errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}

// run reads datagrams, applies the selected fault, and forwards the result.
func (r *Relay) run() {
	defer r.wg.Done()
	buf := make([]byte, wire.MaxUDPReadBufferBytes)
	for {
		n, addr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		// The relay's bound socket receives both directions.
		// Only the configured server endpoint is server-originated;
		// every other sender is the client.
		direction := ServerToClient
		if addr.String() != r.server.String() {
			direction = ClientToServer
			r.mu.Lock()
			r.client = cloneAddr(addr)
			r.mu.Unlock()
		}

		// ReadFromUDP reuses buf on the next iteration;
		// retain a packet-owned copy before releasing control to the transform or another read.
		packet := append([]byte(nil), buf[:n]...)

		r.mu.Lock()
		i := r.index[direction]
		r.index[direction]++
		r.mu.Unlock()

		action := Pass
		// Run user-controlled test logic without the relay mutex
		// so Inject and Events remain usable even when a transform deliberately stalls.
		if r.transform != nil {
			action = r.transform(direction, append([]byte(nil), packet...), i)
		}

		r.mu.Lock()
		r.events = append(r.events, Event{
			Direction: direction,
			Index:     i,
			Action:    action,
			Size:      len(packet),
		})
		client := cloneAddr(r.client)
		r.mu.Unlock()

		if action == Drop {
			continue
		}

		// No client address exists until the relay has seen a client datagram.
		if direction == ServerToClient && client == nil {
			continue
		}

		destination := r.server
		if direction == ServerToClient {
			destination = client
		}

		if action == Delay {
			// A short real delay is enough to force packet reordering in tests;
			// shutdown remains immediate because the timer wait also observes closed.
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.closed:
				timer.Stop()
				return
			}
		}

		if action == Inject {
			continue
		}

		if action == Corrupt && len(packet) > 0 {
			packet[len(packet)-1] ^= 0xff
		}
		if action == Truncate && len(packet) > 1 {
			packet = packet[:len(packet)/2]
		}

		if action == Reorder {
			// Hold the first selected packet independently per direction.
			// When the next selected packet arrives, send it first and then flush the held datagram.
			r.mu.Lock()

			previous := r.pending[direction]
			r.pending[direction] = nil
			if previous == nil {
				r.pending[direction] = &queuedDatagram{packet: packet, destination: cloneAddr(destination)}
			}

			r.mu.Unlock()
			if previous == nil {
				continue
			}

			_, _ = r.conn.WriteToUDP(packet, destination)
			_, _ = r.conn.WriteToUDP(previous.packet, previous.destination)
			continue
		}

		r.mu.Lock()
		previous := r.pending[direction]
		r.pending[direction] = nil
		r.mu.Unlock()

		_, _ = r.conn.WriteToUDP(packet, destination)
		if action == Duplicate {
			_, _ = r.conn.WriteToUDP(packet, destination)
		}

		if previous != nil {
			// Never strand a held packet just because the next packet was not selected for reordering;
			// preserve it by flushing after the current datagram.
			_, _ = r.conn.WriteToUDP(previous.packet, previous.destination)
		}
	}
}

// cloneAddr returns an independent copy of a UDP endpoint.
func cloneAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}

	return &net.UDPAddr{
		IP:   append(net.IP(nil), a.IP...),
		Port: a.Port,
		Zone: a.Zone,
	}
}
