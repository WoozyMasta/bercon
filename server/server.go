// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/woozymasta/bercon/internal/wire"
)

var (
	// ErrClosed is returned when an operation uses a stopped server.
	ErrClosed = errors.New("RCon server is closed")

	// ErrSessionInactive is returned when an expired or deauthenticated session is used.
	ErrSessionInactive = errors.New("RCon session is inactive")

	// ErrMessageTooLarge is returned when a server event exceeds the configured datagram limit.
	ErrMessageTooLarge = errors.New("RCon server message exceeds datagram limit")

	// ErrMessageAckTimeout means all configured message acknowledgements were lost.
	ErrMessageAckTimeout = errors.New("RCon server message acknowledgement timed out")
)

// Options controls server limits and authentication.
type Options struct {
	// Optional hook called asynchronously after a new endpoint authenticates.
	OnSessionOpen SessionOpenHandler `json:"-" yaml:"-"`

	// Password required for login; excluded from serialization.
	Password string `json:"-" yaml:"-"`

	// Command inactivity before authentication expires.
	SessionTimeout time.Duration `json:"session_timeout" yaml:"session_timeout"`

	// Maximum simultaneous authenticated endpoints.
	MaxClients int `json:"max_clients" yaml:"max_clients"`

	// Maximum accepted command body size.
	MaxCommandBytes int `json:"max_command_bytes" yaml:"max_command_bytes"`

	// Maximum outbound response datagram size.
	MaxDatagramBytes int `json:"max_datagram_bytes" yaml:"max_datagram_bytes"`

	// Number of command handler workers.
	MaxConcurrentHandlers int `json:"max_concurrent_handlers" yaml:"max_concurrent_handlers"`

	// Number of sends before a server message fails.
	MessageAttempts int `json:"message_attempts" yaml:"message_attempts"`

	// Total retry window for each server message.
	MessageRetryWindow time.Duration `json:"message_retry_window" yaml:"message_retry_window"`
}

// SessionOpenHandler observes one authenticated generation.
// Its context ends when that generation expires, is replaced, or the server closes.
type SessionOpenHandler func(context.Context, *Session)

// Request is one authenticated command received from a client.
type Request struct {
	Session  *Session `json:"-"        yaml:"-"`        // Authenticated endpoint; excluded from serialization.
	Command  []byte   `json:"command"  yaml:"command"`  // Independent copy of the raw command body.
	Sequence byte     `json:"sequence" yaml:"sequence"` // Command sequence copied from the wire packet.
}

// Response is the correlated command response and optional asynchronous output.
type Response struct {
	// Correlated 0x01 body; empty is a valid response.
	Data []byte `json:"data" yaml:"data"`

	// Independent 0x02 events queued after the command response.
	// Excess events are dropped when the bounded delivery queue is full;
	// [Server.DroppedMessages] reports the count.
	PostMessages [][]byte `json:"post_messages,omitempty" yaml:"post_messages,omitempty"`
}

// Handler processes one RCon command.
type Handler interface {
	// HandleRCon receives the lifetime context of the authenticated session.
	HandleRCon(context.Context, Request) Response
}

// HandlerFunc adapts a function into a Handler.
type HandlerFunc func(context.Context, Request) Response

// HandleRCon calls f.
func (f HandlerFunc) HandleRCon(ctx context.Context, r Request) Response {
	return f(ctx, r)
}

// Server is a UDP BattlEye RCon endpoint.
type Server struct {
	handler         Handler             // Application command handler.
	ctx             context.Context     // Internal lifetime context.
	serveErr        error               // Fatal reader error returned from Serve.
	conn            *net.UDPConn        // Bound UDP socket shared by the reader and writers.
	sessions        map[string]*Session // Sessions keyed by remote UDP endpoint.
	cancel          context.CancelFunc  // Stops server-owned workers and message senders.
	requests        chan request        // Bounded queue between packet reader and handlers.
	deliveries      chan delivery       // Bounded queue for post-response server messages.
	ready           chan struct{}       // Closed once the UDP reader is serving.
	opts            Options             // Immutable server configuration.
	workers         sync.WaitGroup      // Tracks command handler workers.
	deliverers      sync.WaitGroup      // Tracks asynchronous message delivery workers.
	reader          sync.WaitGroup      // Tracks the UDP reader loop.
	expirer         sync.WaitGroup      // Tracks the inactive-session expiry loop.
	dropped         atomic.Uint64       // Post-response events discarded at delivery queue capacity.
	droppedCommands atomic.Uint64       // Authenticated commands discarded at request queue capacity.
	mu              sync.Mutex          // Protects lifecycle and authenticated sessions.
	started         bool                // Prevents multiple Serve loops.
	closed          bool                // Records idempotent shutdown state.
}

// request is one already-authenticated command waiting for a handler worker.
type request struct {
	addr    *net.UDPAddr // Remote endpoint receiving the response.
	session *Session     // Exact session that authenticated the queued packet.
	packet  wire.Packet  // Decoded request packet.
}

// delivery is one asynchronous event tied to the session that created it.
type delivery struct {
	ctx     context.Context // Caller or session lifetime that bounds this delivery.
	session *Session        // Exact authenticated generation receiving the event.
	result  chan error      // Optional completion channel used by synchronous broadcasts.
	data    []byte          // Owned event bytes retained by the bounded delivery queue.
}

// New binds addr and prepares a server. Call Serve to process datagrams.
func New(addr string, h Handler, opts *Options) (*Server, error) {
	if h == nil {
		return nil, errors.New("RCon handler is required")
	}

	o := Options{
		SessionTimeout:        45 * time.Second,
		MaxClients:            64,
		MaxCommandBytes:       wire.MaxCommandBodyBytes,
		MaxDatagramBytes:      wire.CanonicalMaxDatagramBytes,
		MaxConcurrentHandlers: 32,
		MessageAttempts:       5,
		MessageRetryWindow:    10 * time.Second,
	}

	if opts != nil {
		o = *opts
		if o.SessionTimeout == 0 {
			o.SessionTimeout = 45 * time.Second
		}
		if o.MaxClients == 0 {
			o.MaxClients = 64
		}
		if o.MaxCommandBytes == 0 {
			o.MaxCommandBytes = wire.MaxCommandBodyBytes
		}
		if o.MaxDatagramBytes == 0 {
			o.MaxDatagramBytes = wire.CanonicalMaxDatagramBytes
		}
		if o.MaxConcurrentHandlers == 0 {
			o.MaxConcurrentHandlers = 32
		}
		if o.MessageAttempts == 0 {
			o.MessageAttempts = 5
		}
		if o.MessageRetryWindow == 0 {
			o.MessageRetryWindow = 10 * time.Second
		}
	}

	// Both queues use twice the worker count; reject multiplication overflow before allocation.
	// A zero per-attempt retry interval would send all configured retries as a burst.
	if o.SessionTimeout <= 0 ||
		o.MaxClients < 1 ||
		o.MaxCommandBytes < 0 ||
		o.MaxDatagramBytes < wire.MultipartHeaderBytes+1 ||
		o.MaxDatagramBytes > wire.MaxUDPDatagramBytes ||
		o.MaxCommandBytes > wire.MaxUDPDatagramBytes-wire.CommandHeaderBytes ||
		o.MaxConcurrentHandlers < 1 ||
		o.MaxConcurrentHandlers > int(^uint(0)>>1)/2 ||
		o.MessageAttempts < 1 ||
		o.MessageRetryWindow <= 0 ||
		o.MessageRetryWindow/time.Duration(o.MessageAttempts) <= 0 {
		return nil, errors.New("invalid RCon server options")
	}

	pc, e := net.ListenPacket("udp", addr)
	if e != nil {
		return nil, fmt.Errorf("listen RCon UDP: %w", e)
	}

	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, errors.New("UDP listener has unexpected type")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		conn:       conn,
		opts:       o,
		handler:    h,
		sessions:   make(map[string]*Session),
		ctx:        ctx,
		cancel:     cancel,
		requests:   make(chan request, o.MaxConcurrentHandlers*2),
		deliveries: make(chan delivery, o.MaxConcurrentHandlers*2),
		ready:      make(chan struct{}),
	}

	return s, nil
}

// Addr returns the bound UDP address, including an ephemeral port selected by the OS.
func (s *Server) Addr() net.Addr {
	return s.conn.LocalAddr()
}

// Ready closes when Serve has started its UDP reader.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// DroppedMessages reports post-response events discarded when delivery is overloaded.
func (s *Server) DroppedMessages() uint64 {
	return s.dropped.Load()
}

// DroppedCommands reports authenticated commands discarded because the handler queue was full.
func (s *Server) DroppedCommands() uint64 {
	return s.droppedCommands.Load()
}

// Serve processes packets until ctx is canceled, the server closes,
// or the socket encounters a fatal error.
func (s *Server) Serve(ctx context.Context) error {
	if ctx == nil {
		return errors.New("RCon server context is nil")
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("RCon server already serving")
	}

	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}

	s.started = true
	s.mu.Unlock()

	// Convert caller cancellation into the same shutdown path used by Close,
	// which also closes the socket and wakes ReadFromUDP.
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.ctx.Done():
		}
	}()

	// Start workers before the reader can enqueue commands.
	// The queue and pool are both bounded
	// so slow application handlers cannot create unbounded goroutines.
	for range s.opts.MaxConcurrentHandlers {
		s.workers.Add(1)
		go s.worker()

		s.deliverers.Add(1)
		go s.deliveryWorker()
	}

	s.reader.Add(1)
	go s.readLoop()
	s.expirer.Add(1)
	go s.expireSessions()
	<-s.ctx.Done()

	// Wait for the reader before closing requests: it is the only sender.
	// Workers then drain already-queued requests and exit,
	// avoiding both send-on-closed and leaks.
	s.reader.Wait()

	close(s.requests)
	s.workers.Wait()
	s.deliverers.Wait()
	s.expirer.Wait()

	s.mu.Lock()
	serveErr := s.serveErr
	s.mu.Unlock()
	if serveErr != nil {
		return serveErr
	}

	return nil
}

// Close stops serving and releases the UDP socket. It is safe to call repeatedly.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}

	s.closed = true
	s.cancel()
	for _, session := range s.sessions {
		// Invalidate each generation before releasing the registry lock
		// so no queued handler can write a response after shutdown takes effect.
		session.deactivate()
	}

	s.sessions = make(map[string]*Session)
	s.mu.Unlock()
	// Closing the socket unblocks ReadFromUDP so Serve can finish its shutdown path.
	err := s.conn.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}

// BroadcastMessage sends an independent event to every currently active session.
// Each delivery waits for its protocol ACK and reports all per-session failures.
func (s *Server) BroadcastMessage(ctx context.Context, data []byte) error {
	if ctx == nil {
		return errors.New("RCon broadcast context is nil")
	}
	if s.ctx.Err() != nil {
		return ErrClosed
	}

	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	// Do not hold the registry lock while waiting for network ACKs.
	// Each session validates its own active state and reports failure independently.
	return s.broadcast(ctx, sessions, data)
}

// broadcast queues one copied event per session and joins endpoint-specific delivery errors.
func (s *Server) broadcast(ctx context.Context, sessions []*Session, data []byte) error {
	if len(sessions) == 0 {
		return ctx.Err()
	}

	results := make([]error, len(sessions))
	// pendingDelivery tracks completion for only the events accepted by the queue.
	type pendingDelivery struct {
		addr  *net.UDPAddr // Endpoint included in an asynchronous delivery error.
		done  chan error   // Buffered result from the shared delivery worker.
		index int          // Slot in the aggregate result slice.
	}
	pending := make([]pendingDelivery, 0, len(sessions))
	payload := append([]byte(nil), data...)

schedule:
	for index, session := range sessions {
		completion := make(chan error, 1)
		job := delivery{
			session: session,
			ctx:     ctx,
			data:    payload,
			result:  completion,
		}

		select {
		case s.deliveries <- job:
			pending = append(pending, pendingDelivery{
				index: index,
				addr:  session.addr,
				done:  completion,
			})

		case <-session.done:
			results[index] = fmt.Errorf("send RCon message to %s: %w", session.addr, ErrSessionInactive)

		case <-ctx.Done():
			results[index] = fmt.Errorf("send RCon message to %s: %w", session.addr, ctx.Err())
			for remaining := index + 1; remaining < len(sessions); remaining++ {
				results[remaining] = fmt.Errorf("send RCon message to %s: %w", sessions[remaining].addr, ctx.Err())
			}
			break schedule

		case <-s.ctx.Done():
			results[index] = fmt.Errorf("send RCon message to %s: %w", session.addr, ErrClosed)
			for remaining := index + 1; remaining < len(sessions); remaining++ {
				results[remaining] = fmt.Errorf("send RCon message to %s: %w", sessions[remaining].addr, ErrClosed)
			}
			break schedule
		}
		if ctx.Err() != nil || s.ctx.Err() != nil {
			break
		}
	}

	for _, job := range pending {
		select {
		case err := <-job.done:
			if err != nil {
				results[job.index] = fmt.Errorf("send RCon message to %s: %w", job.addr, err)
			}

		case <-ctx.Done():
			results[job.index] = fmt.Errorf("send RCon message to %s: %w", job.addr, ctx.Err())

		case <-s.ctx.Done():
			results[job.index] = fmt.Errorf("send RCon message to %s: %w", job.addr, ErrClosed)
		}
	}

	return errors.Join(results...)
}

// expireSessions removes idle endpoints and invalidates every retained Session handle.
func (s *Server) expireSessions() {
	defer s.expirer.Done()

	interval := min(s.opts.SessionTimeout/2, time.Second)
	if interval <= 0 {
		interval = s.opts.SessionTimeout
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case now := <-ticker.C:
			s.mu.Lock()
			for key, session := range s.sessions {
				// Take activity snapshots while holding the registry lock
				// so an endpoint cannot be removed between lookup and expiry decision.
				if now.Sub(session.activity()) > s.opts.SessionTimeout {
					delete(s.sessions, key)
					session.deactivate()
				}
			}

			s.mu.Unlock()
		}
	}
}

// readLoop decodes packets and handles protocol control before dispatching commands.
func (s *Server) readLoop() {
	defer s.reader.Done()
	close(s.ready)
	buf := make([]byte, wire.MaxUDPReadBufferBytes)

	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() == nil {
				s.fail(fmt.Errorf("read RCon UDP datagram: %w", err))
			}

			return
		}

		p, err := wire.Decode(buf[:n])
		if err != nil {
			continue
		}

		switch p.Kind {
		case 0:
			s.login(addr, p)

		case 1:
			s.dispatch(addr, p)

		case 2:
			// ACK packets stay on the read loop and never wait behind command handlers.
			s.mu.Lock()
			session := s.sessions[addr.String()]
			s.mu.Unlock()
			if session != nil {
				session.acknowledge(p.Seq)
			}
		}
	}
}

// login authenticates an endpoint and sends its success or failure response.
func (s *Server) login(addr *net.UDPAddr, p wire.Packet) {
	// Compare the complete password without returning at the first mismatching byte.
	success := subtle.ConstantTimeCompare([]byte(s.opts.Password), p.Data) == 1
	var openSession *Session
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	now := time.Now()

	// Expire stale endpoints before applying the limit to a new login.
	for key, session := range s.sessions {
		if now.Sub(session.activity()) > s.opts.SessionTimeout {
			delete(s.sessions, key)
			session.deactivate()
		}
	}

	key := addr.String()
	if success {
		// Re-authentication from an existing endpoint refreshes its session
		// and does not consume another client slot; new endpoints must respect MaxClients.
		if _, ok := s.sessions[key]; !ok && len(s.sessions) >= s.opts.MaxClients {
			success = false
		}

		if success {
			// Every accepted login creates a new generation.
			// Retained handles and in-flight handlers from the previous login become unusable first.
			if old := s.sessions[key]; old != nil {
				old.deactivate()
			}

			sessionCtx, cancel := context.WithCancel(s.ctx)
			session := &Session{
				server: s,
				addr:   cloneUDP(addr),
				last:   now,
				active: true,
				done:   make(chan struct{}),
				ctx:    sessionCtx,
				cancel: cancel,
			}
			s.sessions[key] = session
			openSession = session
		}
	}

	s.mu.Unlock()
	value := byte(0)
	if success {
		value = 1
	}

	raw, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte{value}})

	_, _ = s.conn.WriteToUDP(raw, addr)
	if success && openSession != nil && s.opts.OnSessionOpen != nil {
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.opts.OnSessionOpen(openSession.ctx, openSession)
		}()
	}
}

// dispatch refreshes activity and queues an authenticated command if capacity allows.
func (s *Server) dispatch(addr *net.UDPAddr, p wire.Packet) {
	// Oversized commands are discarded before queueing
	// so application code never receives a payload beyond the configured contract.
	if len(p.Data) > s.opts.MaxCommandBytes {
		return
	}

	key := addr.String()
	s.mu.Lock()
	session := s.sessions[key]
	if session != nil && time.Since(session.activity()) > s.opts.SessionTimeout {
		delete(s.sessions, key)
		session.deactivate()
		session = nil
	}
	if session != nil {
		session.touch()
	}

	s.mu.Unlock()
	if session == nil {
		return
	}

	// Empty commands are protocol keepalives, not application work.
	// Answering them here keeps a saturated handler pool from making a live session expire.
	if len(p.Data) == 0 {
		_ = s.writeResponse(session, p.Seq, nil)
		return
	}

	r := request{
		addr:    cloneUDP(addr),
		session: session,
		packet:  p,
	}

	select {
	case s.requests <- r:
	default:
		// UDP has no receive-side backpressure.
		// Drop when the bounded queue is full rather than spawning unbounded work;
		// a client can then time out explicitly, and the counter exposes the overload.
		s.droppedCommands.Add(1)
	}
}

// deauthenticate removes session only if it is still the active endpoint session.
func (s *Server) deauthenticate(session *Session) {
	s.mu.Lock()
	if s.sessions[session.addr.String()] == session {
		delete(s.sessions, session.addr.String())
	}
	session.deactivate()
	s.mu.Unlock()
}

// worker runs handlers outside the UDP read loop and writes their results.
func (s *Server) worker() {
	defer s.workers.Done()
	for r := range s.requests {
		s.mu.Lock()
		session := s.sessions[r.addr.String()]
		s.mu.Unlock()

		// A queued command may outlive its login.
		// Endpoint reuse alone is not enough:
		// require the exact session that authenticated this packet to still be current.
		if session == nil || session != r.session {
			continue
		}

		// Application code runs outside the socket reader so ACKs and logins stay responsive.
		response := s.handler.HandleRCon(r.session.ctx, Request{
			Sequence: r.packet.Seq,
			Command:  append([]byte(nil), r.packet.Data...),
			Session:  session,
		})

		if err := s.writeResponse(r.session, r.packet.Seq, response.Data); err != nil {
			continue
		}

		// The correlated response is already on the wire.
		// Queue independent events separately so waiting for their ACKs does not hold a command worker.
	postMessages:
		for _, msg := range response.PostMessages {
			job := delivery{
				session: r.session,
				ctx:     r.session.ctx,
				data:    append([]byte(nil), msg...),
			}

			select {
			case s.deliveries <- job:
			case <-r.session.done:
				break postMessages

			case <-s.ctx.Done():
				break postMessages

			default:
				// The command response is already delivered.
				// Drop excess events rather than retaining the command worker behind an overloaded queue.
				s.dropped.Add(1)
			}
		}
	}
}

// deliveryWorker retries queued events without occupying command workers.
func (s *Server) deliveryWorker() {
	defer s.deliverers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		select {
		case <-s.ctx.Done():
			return

		case job := <-s.deliveries:
			err := job.session.SendMessage(job.ctx, job.data)
			if job.result != nil {
				job.result <- err
			}
		}
	}
}

// writeResponse emits a response only while its originating session generation is active.
func (s *Server) writeResponse(session *Session, seq byte, data []byte) error {
	// Deactivation takes the same lock, making generation validation
	// and all multipart writes one atomic lifetime operation relative to expiry/re-login.
	session.sendMu.Lock()
	defer session.sendMu.Unlock()

	session.mu.Lock()
	active := session.active
	session.mu.Unlock()
	if !active {
		return ErrSessionInactive
	}
	if s.ctx.Err() != nil {
		return ErrClosed
	}

	maxDatagram := s.opts.MaxDatagramBytes
	if len(data) <= maxDatagram-wire.CommandHeaderBytes {
		raw, _ := wire.Encode(wire.Packet{
			Kind: 1,
			Seq:  seq,
			Data: data,
		})

		_, err := s.conn.WriteToUDP(raw, session.addr)
		return err
	}

	chunk := maxDatagram - wire.MultipartHeaderBytes
	count := (len(data)-1)/chunk + 1
	// The fragment count is one byte; larger responses cannot be represented canonically.
	if count > 255 {
		return errors.New("RCon response exceeds multipart limit")
	}

	for i := range count {
		end := (i + 1) * chunk
		end = min(end, len(data))
		// count was bounded to one byte above; index remains below count.
		raw, e := wire.Encode(wire.Packet{
			Kind:  1,
			Seq:   seq,
			Parts: byte(count), // #nosec G115 -- count is limited to 255 above.
			Part:  byte(i),     // #nosec G115 -- index is below the validated count.
			Data:  data[i*chunk : end],
		})
		if e != nil {
			return e
		}

		if _, e = s.conn.WriteToUDP(raw, session.addr); e != nil {
			return e
		}
	}

	return nil
}

// fail records a fatal socket error and starts the idempotent shutdown path.
func (s *Server) fail(err error) {
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}

	s.serveErr = err
	s.mu.Unlock()

	_ = s.Close()
}

// cloneUDP copies an address so caller-owned packet data cannot be reused.
func cloneUDP(a *net.UDPAddr) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   append(net.IP(nil), a.IP...),
		Port: a.Port,
		Zone: a.Zone,
	}
}
