// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/woozymasta/bercon/internal/wire"
)

// Options configures client timeouts and bounded queues.
type Options struct {
	// Maximum wait for each login attempt.
	LoginTimeout time.Duration `json:"login_timeout" yaml:"login_timeout"`

	// Number of allowed login attempts.
	LoginAttempts int `json:"login_attempts" yaml:"login_attempts"`

	// Maximum wait for one command response.
	ResponseTimeout time.Duration `json:"response_timeout" yaml:"response_timeout"`

	// Empty-command interval; zero disables keepalive.
	KeepAliveInterval time.Duration `json:"keepalive_interval" yaml:"keepalive_interval"`

	// Consecutive failed keepalive exchanges before the client becomes terminal.
	MaxKeepAliveFailures int `json:"max_keepalive_failures" yaml:"max_keepalive_failures"`

	// Capacity shared by the application queue and bounded pre-login message buffer.
	MessageBuffer int `json:"message_buffer" yaml:"message_buffer"`

	// When the message queue is full, discard its oldest item instead of the new item.
	DropOldestMessages bool `json:"drop_oldest_messages,omitempty" yaml:"drop_oldest_messages,omitempty"`

	// Maximum assembled response size.
	MaxResponseBytes int `json:"max_response_bytes" yaml:"max_response_bytes"`

	// Quarantine applied after uncertain command completion.
	SequenceReuseDelay time.Duration `json:"sequence_reuse_delay" yaml:"sequence_reuse_delay"`
}

// Client is an authenticated RCon connection.
type Client struct {
	ctx              context.Context          // Internal lifetime context.
	terminalErr      error                    // Fatal transport or protocol failure; nil on Close.
	onMessage        func(Message)            // Optional fleet observer, called for each authenticated server event.
	conn             *net.UDPConn             // Connected socket owned by the client.
	cancel           context.CancelFunc       // Stops all client-owned goroutines.
	control          chan writeRequest        // Priority queue for protocol control packets.
	normal           chan writeRequest        // Bounded queue for commands and keepalives.
	messages         chan Message             // Bounded application-facing message queue.
	pending          map[byte]*pendingCommand // Active command requests by wire sequence.
	wake             chan struct{}            // Wakes callers waiting for a free sequence.
	closed           chan struct{}            // Closed once when client shutdown begins.
	done             chan struct{}            // Closed after all owned goroutines and messages stop.
	quarantineTimers map[byte]*time.Timer     // At most one timer for each uncertain sequence.
	opts             Options                  // Immutable options copied during Dial.
	wg               sync.WaitGroup           // Tracks all client-owned goroutines.
	dropped          atomic.Uint64            // Count of messages dropped at queue capacity.
	lastWrite        atomic.Int64             // UnixNano time of the last successful command write.
	errMu            sync.RWMutex             // Protects terminalErr from lifecycle readers.
	once             sync.Once                // Makes Close idempotent.
	mu               sync.Mutex               // Protects sequence and request routing state.
	used             [256]bool                // Sequences reserved or quarantined from reuse.
	next             byte                     // Next sequence candidate for round-robin allocation.
}

// commandResult carries one completed command outcome to its caller.
type commandResult struct {
	err  error  // Command failure, if any.
	data []byte // Response bytes for a successful command.
}

// pendingCommand tracks one in-flight response and its multipart fragments.
type pendingCommand struct {
	result    chan commandResult // Buffered completion channel for the caller.
	fragments [][]byte           // Fragments indexed by their wire part number.
	received  int                // Number of distinct fragments received.
	size      int                // Total assembled bytes so far.
	parts     byte               // Declared multipart fragment count.
}

// writeRequest asks the sole socket writer to send one datagram.
type writeRequest struct {
	done    chan error // Buffered write completion channel.
	packet  []byte     // Encoded UDP datagram.
	command bool       // Whether a successful write resets keepalive timing.
}

// DefaultOptions returns conservative BattlEye RCon v2 client defaults.
func DefaultOptions() Options {
	return Options{
		LoginTimeout:         5 * time.Second,
		LoginAttempts:        3,
		ResponseTimeout:      5 * time.Second,
		KeepAliveInterval:    20 * time.Second,
		MaxKeepAliveFailures: 2,
		MessageBuffer:        128,
		MaxResponseBytes:     1 << 20,
		SequenceReuseDelay:   5 * time.Second,
	}
}

// Dial authenticates a connected UDP socket. The context bounds dialing and login only;
// after success, the returned Client has an independent lifetime controlled by Close.
func Dial(ctx context.Context, address, password string, opts *Options) (*Client, error) {
	return dial(ctx, address, password, opts, nil)
}

// dial optionally duplicates authenticated server events to an owning fleet.
func dial(
	ctx context.Context,
	address string,
	password string,
	opts *Options,
	onMessage func(Message),
) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("RCon dial context is nil")
	}

	o := DefaultOptions()
	if opts != nil {
		o = *opts
	}

	if err := validateClientOptions(o); err != nil {
		return nil, err
	}

	d := net.Dialer{}
	pc, err := d.DialContext(ctx, "udp", address)
	if err != nil {
		return nil, fmt.Errorf("dial RCon: %w", err)
	}

	conn := pc.(*net.UDPConn)
	// Login uses a blocking UDP read, so cancellation must move its deadline forward immediately
	// instead of waiting for LoginTimeout to expire.
	var startupMu sync.Mutex
	callbackDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		startupMu.Lock()
		_ = conn.SetDeadline(time.Now())
		startupMu.Unlock()
		close(callbackDone)
	})
	defer stopCancel()

	login, err := wire.Encode(wire.Packet{Kind: 0, Data: []byte(password)})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	buf := make([]byte, wire.MaxUDPReadBufferBytes)
	loginMessages := make([]Message, 0, o.MessageBuffer)
	var loginDropped uint64
	for attempt := 0; attempt < o.LoginAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			_ = conn.Close()
			return nil, err
		}

		// Login is setup traffic, so retrying it is safe; each attempt has its own deadline.
		_ = conn.SetDeadline(time.Now().Add(o.LoginTimeout))
		if _, err = conn.Write(login); err != nil {
			_ = conn.Close()
			return nil, err
		}

		for {
			n, e := conn.Read(buf)
			if e != nil {
				if ne, ok := e.(net.Error); ok && ne.Timeout() {
					break
				}

				_ = conn.Close()
				return nil, e
			}

			p, e := wire.Decode(buf[:n])
			if e != nil {
				continue
			}

			if p.Kind == 2 {
				// Some servers send events before the login reply.
				// ACK during setup so the server can continue, then queue a copy for the caller after Client starts.
				ack, encodeErr := wire.Encode(wire.Packet{Kind: 2, Seq: p.Seq})
				if encodeErr == nil {
					_, _ = conn.Write(ack)
				}

				// A server may send events before authentication completes. Keep at
				// most the configured application buffer while still ACKing every
				// event so a reordered login exchange can make progress.
				message := Message{
					Sequence: p.Seq,
					Data:     append([]byte(nil), p.Data...),
				}
				if len(loginMessages) < o.MessageBuffer {
					loginMessages = append(loginMessages, message)
				} else {
					if o.DropOldestMessages {
						copy(loginMessages, loginMessages[1:])
						loginMessages[len(loginMessages)-1] = message
					}

					loginDropped++
				}
				continue
			}

			if p.Kind != 0 || len(p.Data) != 1 {
				continue
			}

			if p.Data[0] != 1 {
				_ = conn.Close()
				return nil, ErrLoginFailed
			}

			// Stop the startup callback while holding its deadline lock.
			// If cancellation already started the callback, wait for it before deciding whether login won.
			startupMu.Lock()
			if !stopCancel() {
				startupMu.Unlock()
				<-callbackDone
				_ = conn.Close()
				return nil, ctx.Err()
			}

			if err = ctx.Err(); err != nil {
				startupMu.Unlock()
				_ = conn.Close()
				return nil, err
			}

			_ = conn.SetDeadline(time.Time{})
			startupMu.Unlock()

			// Detach the established connection from the short-lived startup context.
			// Fleet and standalone callers control its lifetime through Client.Close instead.
			life, cancel := context.WithCancel(context.Background())
			c := &Client{
				conn:             conn,
				onMessage:        onMessage,
				opts:             o,
				ctx:              life,
				cancel:           cancel,
				control:          make(chan writeRequest, 256),
				normal:           make(chan writeRequest, 256),
				messages:         make(chan Message, o.MessageBuffer),
				pending:          make(map[byte]*pendingCommand),
				wake:             make(chan struct{}, 1),
				closed:           make(chan struct{}),
				done:             make(chan struct{}),
				quarantineTimers: make(map[byte]*time.Timer),
			}

			c.lastWrite.Store(time.Now().UnixNano())
			c.dropped.Store(loginDropped)
			for _, message := range loginMessages {
				c.enqueueMessage(message)
				if c.onMessage != nil {
					c.onMessage(message)
				}
			}

			c.wg.Add(2)
			go c.writer()
			go c.reader()
			if o.KeepAliveInterval > 0 {
				c.wg.Add(1)
				go c.keepalive()
			}

			return c, nil
		}
	}

	stopCancel()
	_ = conn.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	return nil, ErrLoginTimeout
}

// Messages exposes independently sequenced server messages.
func (c *Client) Messages() <-chan Message {
	return c.messages
}

// DroppedMessages reports events dropped because an application or pre-login queue was full.
func (c *Client) DroppedMessages() uint64 {
	return c.dropped.Load()
}

// Done closes after the client has stopped and all client-owned goroutines exit.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Err returns the terminal transport or protocol failure, or nil for normal Close.
func (c *Client) Err() error {
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	return c.terminalErr
}

// Command sends one application datagram and waits for its correlated response.
func (c *Client) Command(ctx context.Context, command string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("RCon command context is nil")
	}

	select {
	case <-c.closed:
		return nil, c.commandUnavailableError()
	default:
	}

	if len(command) > wire.MaxCommandBodyBytes {
		return nil, ErrCommandTooLong
	}

	seq, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}

	p := &pendingCommand{result: make(chan commandResult, 1)}

	c.mu.Lock()
	// Register before enqueueing:
	// UDP responses can arrive as soon as the writer sends,
	// and the reader must already know where to route one.
	c.pending[seq] = p
	c.mu.Unlock()

	raw, err := wire.Encode(wire.Packet{Kind: 1, Seq: seq, Data: []byte(command)})
	if err != nil {
		c.release(seq)
		return nil, err
	}

	wr := writeRequest{packet: raw, done: make(chan error, 1), command: true}
	select {
	case c.normal <- wr:
	case <-ctx.Done():
		c.remove(seq)
		c.release(seq)
		return nil, ctx.Err()

	case <-c.closed:
		c.release(seq)
		return nil, c.commandUnavailableError()
	}

	// From this point the datagram may have reached the server.
	// Cancellation can no longer promise that the command did not execute,
	// so reserve its sequence briefly.
	select {
	case err = <-wr.done:
		if err != nil {
			c.remove(seq)
			c.release(seq)
			return nil, err
		}

	case <-ctx.Done():
		// Once queued, the writer may still send; quarantine to absorb a late response.
		c.quarantine(seq)
		return nil, fmt.Errorf("%w: %w", ErrCommandOutcomeUnknown, ctx.Err())

	case <-c.closed:
		c.release(seq)
		return nil, c.commandUnavailableError()
	}

	// Start the response deadline only after the writer confirms transmission;
	// time spent waiting in the bounded write queue is not server response latency.
	timer := time.NewTimer(c.opts.ResponseTimeout)
	defer timer.Stop()

	select {
	case r := <-p.result:
		c.release(seq)
		return r.data, r.err

	case <-ctx.Done():
		c.quarantine(seq)
		return nil, fmt.Errorf("%w: %w", ErrCommandOutcomeUnknown, ctx.Err())

	case <-timer.C:
		c.quarantine(seq)
		return nil, ErrResponseTimeout

	case <-c.closed:
		return nil, c.commandUnavailableError()
	}
}

// Close stops the client and all owned goroutines.
func (c *Client) Close() error {
	c.shutdown(nil)
	<-c.done

	return nil
}

// validateClientOptions rejects invalid values before any socket is opened.
func validateClientOptions(options Options) error {
	if options.LoginTimeout <= 0 ||
		options.LoginAttempts < 1 ||
		options.ResponseTimeout <= 0 ||
		options.KeepAliveInterval < 0 ||
		options.KeepAliveInterval >= 45*time.Second ||
		options.MaxKeepAliveFailures < 0 ||
		(options.KeepAliveInterval > 0 && options.MaxKeepAliveFailures < 1) ||
		options.MessageBuffer < 1 ||
		options.MaxResponseBytes < 0 ||
		options.SequenceReuseDelay < 0 {
		return errors.New("invalid RCon client options")
	}

	return nil
}

// commandUnavailableError distinguishes explicit Close from a terminal failure.
func (c *Client) commandUnavailableError() error {
	if err := c.Err(); err != nil {
		return err
	}

	return ErrClosed
}

// shutdown records the first terminal state and lets a separate finalizer wait for reader/writer exit,
// so a fatal error never waits for its own goroutine.
func (c *Client) shutdown(err error) {
	c.once.Do(func() {
		c.errMu.Lock()
		c.terminalErr = err
		c.errMu.Unlock()

		// Publish terminal state before closing the socket so blocked callers wake.
		// A command racing this close checks closed again before waiting for a reply.
		close(c.closed)
		c.cancel()
		_ = c.conn.Close()

		c.mu.Lock()
		for _, pending := range c.pending {
			select {
			case pending.result <- commandResult{err: c.commandUnavailableError()}:
			default:
			}
		}

		clear(c.pending)
		for seq, timer := range c.quarantineTimers {
			timer.Stop()
			delete(c.quarantineTimers, seq)
			c.used[seq] = false
		}

		c.mu.Unlock()

		// A reader or writer may call shutdown itself;
		// a separate waiter avoids waiting/ on that same goroutine.
		// Messages closes only after both have stopped.
		go func() {
			c.wg.Wait()
			close(c.messages)
			close(c.done)
		}()
	})
}

// acquire reserves a free wire sequence or waits until one is released.
func (c *Client) acquire(ctx context.Context) (byte, error) {
	for {
		c.mu.Lock()

		// A full sequence ring waits on notification instead of polling or spinning.
		for range 256 {
			seq := c.next
			c.next++
			if !c.used[seq] {
				c.used[seq] = true
				c.mu.Unlock()

				return seq, nil
			}
		}

		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()

		case <-c.closed:
			return 0, ErrClosed

		case <-c.wake:
		}
	}
}

// release makes a sequence reusable and wakes one waiting caller.
func (c *Client) release(seq byte) {
	c.mu.Lock()

	if timer := c.quarantineTimers[seq]; timer != nil {
		timer.Stop()
		delete(c.quarantineTimers, seq)
	}
	c.used[seq] = false
	delete(c.pending, seq)

	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// quarantine delays sequence reuse after a command may have reached the server.
func (c *Client) quarantine(seq byte) {
	c.mu.Lock()
	delete(c.pending, seq)
	select {
	case <-c.closed:
		c.used[seq] = false
		c.mu.Unlock()
		return
	default:
	}

	if c.opts.SequenceReuseDelay == 0 {
		c.used[seq] = false
		c.mu.Unlock()
		select {
		case c.wake <- struct{}{}:
		default:
		}
		return
	}

	var timer *time.Timer
	timer = time.AfterFunc(c.opts.SequenceReuseDelay, func() {
		c.mu.Lock()
		// A stopped timer callback can already be runnable.
		// Identity-check it so an old callback cannot release
		// a sequence reserved by a newer quarantine.
		if c.quarantineTimers[seq] != timer {
			c.mu.Unlock()
			return
		}

		delete(c.quarantineTimers, seq)
		c.used[seq] = false
		c.mu.Unlock()
		select {
		case c.wake <- struct{}{}:
		default:
		}
	})

	c.quarantineTimers[seq] = timer
	c.mu.Unlock()
}

// remove detaches a request from response routing without releasing its sequence.
func (c *Client) remove(seq byte) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

// writer serializes socket writes and prioritizes protocol control traffic.
func (c *Client) writer() {
	defer c.wg.Done()
	var deferred *writeRequest
	for {
		var wr writeRequest
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		select {
		case wr = <-c.control:
		default:
			if deferred != nil {
				wr = *deferred
				deferred = nil
			} else {
				// If normal work wins the blocking select, check control once more before writing.
				// This closes the ready-channel select race without spinning
				// and lets a queued ACK overtake that command.
				select {
				case wr = <-c.control:
				case normal := <-c.normal:
					select {
					case wr = <-c.control:
						// Keep the command locally until queued control packets drain.
						deferred = &normal

					default:
						wr = normal
					}

				case <-c.ctx.Done():
					return
				}
			}
		}

		_, err := c.conn.Write(wr.packet)
		if wr.command && err == nil {
			c.lastWrite.Store(time.Now().UnixNano())
		}

		wr.done <- err
		if err != nil {
			if c.ctx.Err() == nil {
				c.shutdown(fmt.Errorf("write RCon datagram: %w", err))
			}
			return
		}
	}
}

// reader validates incoming datagrams and routes responses, ACKs, and messages.
func (c *Client) reader() {
	defer c.wg.Done()
	buf := make([]byte, wire.MaxUDPReadBufferBytes)

	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			if c.ctx.Err() == nil {
				c.shutdown(fmt.Errorf("read RCon datagram: %w", err))
			}
			return
		}

		p, err := wire.Decode(buf[:n])
		if err != nil {
			continue
		}

		switch p.Kind {
		case 1:
			c.route(p)

		case 2:
			// ACK through the priority writer before attempting bounded application delivery.
			raw, e := wire.Encode(wire.Packet{Kind: 2, Seq: p.Seq})
			if e == nil {
				select {
				case c.control <- writeRequest{packet: raw, done: make(chan error, 1)}:
				case <-c.ctx.Done():
					return
				}
			}

			m := Message{Sequence: p.Seq, Data: append([]byte(nil), p.Data...)}
			c.enqueueMessage(m)
			if c.onMessage != nil {
				c.onMessage(m)
			}
		}
	}
}

// enqueueMessage applies the configured overflow policy to the client event queue.
func (c *Client) enqueueMessage(message Message) {
	if !c.opts.DropOldestMessages {
		select {
		case c.messages <- message:
		default:
			c.dropped.Add(1)
		}

		return
	}

	select {
	case c.messages <- message:
		return
	default:
	}

	// The reader must never block on application delivery: it also routes command replies.
	// Make room by discarding the oldest queued event, then retain this one.
	select {
	case <-c.messages:
		c.dropped.Add(1)
	default:
	}

	select {
	case c.messages <- message:
	default:
		c.dropped.Add(1)
	}
}

// route completes a command response or stores one multipart fragment.
func (c *Client) route(p wire.Packet) {
	c.mu.Lock()
	req := c.pending[p.Seq]
	if req == nil {
		c.mu.Unlock()
		return
	}

	if p.Parts == 0 {
		if len(p.Data) > c.opts.MaxResponseBytes {
			delete(c.pending, p.Seq)
			req.result <- commandResult{err: ErrResponseTooLarge}
			c.mu.Unlock()
			return
		}

		delete(c.pending, p.Seq)
		req.result <- commandResult{data: append([]byte(nil), p.Data...)}
		c.mu.Unlock()
		return
	}

	if req.parts != 0 && req.parts != p.Parts {
		// A changing part count makes the response ambiguous; fail this request closed.
		delete(c.pending, p.Seq)
		req.result <- commandResult{err: ErrProtocol}
		c.mu.Unlock()
		return
	}

	if req.parts == 0 {
		req.parts = p.Parts
		req.fragments = make([][]byte, int(p.Parts))
	}

	old := req.fragments[p.Part]
	if old != nil {
		// Retransmitted identical parts are harmless; conflicting bytes invalidate assembly.
		if !bytes.Equal(old, p.Data) {
			delete(c.pending, p.Seq)
			req.result <- commandResult{err: ErrProtocol}
		}

		c.mu.Unlock()
		return
	}

	if req.size+len(p.Data) > c.opts.MaxResponseBytes {
		delete(c.pending, p.Seq)
		req.result <- commandResult{err: ErrResponseTooLarge}
		c.mu.Unlock()
		return
	}

	// Keep empty fragments distinguishable from parts that have not arrived yet.
	req.fragments[p.Part] = append([]byte{}, p.Data...)
	req.received++
	req.size += len(p.Data)

	if req.received == int(req.parts) {
		// Slots are ordered by wire index, independent of packet arrival order.
		data := make([]byte, 0, req.size)
		for _, part := range req.fragments {
			data = append(data, part...)
		}

		delete(c.pending, p.Seq)
		req.result <- commandResult{data: data}
	}

	c.mu.Unlock()
}

// keepalive probes an idle session and terminates it after repeated failed exchanges.
func (c *Client) keepalive() {
	defer c.wg.Done()
	failures := 0

	for {
		lastWrite := time.Unix(0, c.lastWrite.Load())
		wait := max(time.Until(lastWrite.Add(c.opts.KeepAliveInterval)), 0)
		timer := time.NewTimer(wait)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return

		case <-timer.C:
		}

		// Commands and successful keepalive writes reset the inactivity deadline;
		// inbound packets and message ACKs intentionally do not.
		// Command owns the response timer; a second deadline here would race it.
		_, err := c.Command(c.ctx, "")
		if err == nil {
			failures = 0
			continue
		}
		if c.ctx.Err() != nil {
			return
		}

		failures++
		if failures >= c.opts.MaxKeepAliveFailures {
			c.shutdown(fmt.Errorf("keepalive failed %d consecutive times: %w", failures, err))
			return
		}
	}
}
