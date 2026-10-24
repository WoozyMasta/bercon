// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/woozymasta/bercon/internal/wire"
)

// errMessageSequenceExhausted means no message sequence can be safely reused yet.
var errMessageSequenceExhausted = errors.New("all RCon message sequences are awaiting acknowledgement")

// Session represents one authenticated remote endpoint.
type Session struct {
	last    time.Time              // Time of the last authenticated command.
	server  *Server                // Owning server and shared UDP socket.
	addr    *net.UDPAddr           // Remote endpoint for this authenticated session.
	ctx     context.Context        // Lifetime context unique to this authentication generation.
	cancel  context.CancelFunc     // Cancels handlers and message senders when this generation ends.
	pending map[byte]chan struct{} // ACK waiters keyed by active message sequence.
	done    chan struct{}          // Closed once the session becomes inactive.
	sendMu  sync.Mutex             // Serializes socket writes with generation deactivation.
	mu      sync.Mutex             // Protects activity and message sequence state.
	active  bool                   // False after expiry, deauthentication, or server shutdown.
	msgSeq  byte                   // Next candidate server-message sequence.
}

// SendMessage sends a server event and waits for its matching protocol ACK.
func (s *Session) SendMessage(ctx context.Context, data []byte) error {
	if ctx == nil {
		return errors.New("RCon message context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > s.server.opts.MaxDatagramBytes-wire.CommandHeaderBytes {
		return ErrMessageTooLarge
	}

	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return ErrSessionInactive
	}

	if s.pending == nil {
		s.pending = make(map[byte]chan struct{})
	}

	seq, ok := s.nextSequenceLocked()
	if !ok {
		s.mu.Unlock()
		return errMessageSequenceExhausted
	}

	// Buffer one ACK because it can arrive between the UDP write and the select below.
	// A non-blocking send from the reader must not discard that valid early ACK.
	ack := make(chan struct{}, 1)
	s.pending[seq] = ack
	s.mu.Unlock()

	defer func() { s.mu.Lock(); delete(s.pending, seq); s.mu.Unlock() }()

	packet, err := encodeMessage(seq, data)
	if err != nil {
		return err
	}

	interval := s.server.opts.MessageRetryWindow / time.Duration(s.server.opts.MessageAttempts)
	for attempt := 0; attempt < s.server.opts.MessageAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}

		if attempt > 0 {
			// An ACK and retry timer can become ready together.
			// Consume a queued ACK before retransmitting so an accepted event is never sent again.
			select {
			case <-ack:
				return nil
			default:
			}
		}

		// Retries retain the same sequence so duplicate datagrams still have one ACK target.
		// The fixed attempt count and interval bound total retry time.
		if err = s.writePacket(packet); err != nil {
			return err
		}

		timer := time.NewTimer(interval)
		select {
		case <-ack:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil

		case <-ctx.Done():
			// Caller cancellation stops only this send; it does not invalidate the login.
			timer.Stop()
			return ctx.Err()

		case <-s.server.ctx.Done():
			timer.Stop()
			return ErrClosed

		case <-s.done:
			timer.Stop()
			return ErrSessionInactive

		case <-timer.C:
			// Give an ACK that raced the timer precedence over another write.
			select {
			case <-ack:
				return nil
			default:
			}
		}
	}

	// No ACK after the bounded retry window means the endpoint can no longer be trusted as active;
	// remove it so later commands must authenticate again.
	s.server.deauthenticate(s)
	return ErrMessageAckTimeout
}

// IsActive reports whether the owning server still recognizes this session.
func (s *Session) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Done closes when this authentication generation expires or is deauthenticated.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// RemoteAddr returns a copy of the endpoint authenticated by this generation.
func (s *Session) RemoteAddr() net.Addr {
	return cloneUDP(s.addr)
}

// touch records authenticated command activity for session expiry.
func (s *Session) touch() {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
}

// deactivate invalidates the session and wakes all pending message senders.
func (s *Session) deactivate() {
	// A response write that acquired sendMu first completes before termination;
	// otherwise the active check prevents the write from starting afterward.
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	s.mu.Lock()
	if s.active {
		s.active = false
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
	}
	s.mu.Unlock()
}

// writePacket sends one datagram only while this exact session is active.
func (s *Session) writePacket(packet []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if !active {
		return ErrSessionInactive
	}

	if s.server.ctx.Err() != nil {
		return ErrClosed
	}

	_, err := s.server.conn.WriteToUDP(packet, s.addr)
	return err
}

// activity returns the last authenticated command time.
func (s *Session) activity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// nextSequenceLocked finds a sequence that has no outstanding ACK waiter.
func (s *Session) nextSequenceLocked() (byte, bool) {
	// The sequence is one byte with no generation field, so reserve active values.
	// Scanning the full ring both handles wraparound and detects total exhaustion.
	for range 256 {
		seq := s.msgSeq
		s.msgSeq++
		if _, exists := s.pending[seq]; !exists {
			return seq, true
		}
	}

	return 0, false
}

// encodeMessage frames an event using the supplied independent message sequence.
func encodeMessage(seq byte, data []byte) ([]byte, error) {
	return wire.Encode(wire.Packet{Kind: 2, Seq: seq, Data: data})
}

// acknowledge completes the pending delivery for seq without blocking the reader.
func (s *Session) acknowledge(seq byte) {
	s.mu.Lock()
	ch := s.pending[seq]
	active := s.active
	s.mu.Unlock()

	if ch != nil && active {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
