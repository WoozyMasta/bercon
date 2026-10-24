// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultMinReconnectDelay is the shortest reconnect backoff window.
	DefaultMinReconnectDelay = time.Second
	// DefaultMaxReconnectDelay is the longest reconnect backoff window.
	DefaultMaxReconnectDelay = 10 * time.Minute
	// DefaultMaxConcurrentDials limits simultaneous login attempts.
	DefaultMaxConcurrentDials = 4
	// DefaultStableSessionDuration resets backoff after a healthy session.
	DefaultStableSessionDuration = time.Minute
)

const (
	// TargetStateConnecting means the fleet is opening or authenticating a session.
	TargetStateConnecting TargetState = "connecting"
	// TargetStateOnline means an authenticated session is running its handler.
	TargetStateOnline TargetState = "online"
	// TargetStateRetrying means the last session ended and a reconnect is scheduled.
	TargetStateRetrying TargetState = "retrying"
	// TargetStateExpired means the continuous outage passed ExpireAfter.
	TargetStateExpired TargetState = "expired"
	// TargetStateStopped means the fleet context ended for this target.
	TargetStateStopped TargetState = "stopped"
)

// TargetState describes the latest connection lifecycle state for one target.
type TargetState string

// Target configures one independently authenticated RCon endpoint.
type Target struct {
	// Client options; nil selects DefaultOptions.
	Options *Options `json:"options,omitempty" yaml:"options,omitempty"`

	// Stable identifier used to retrieve this target's status.
	ID string `json:"id" yaml:"id"`

	// Hostname or IP address and UDP port of the RCon endpoint.
	Address string `json:"address" yaml:"address"`

	// Password retained for future login attempts and excluded from serialization.
	Password string `json:"-" yaml:"-"`
}

// FleetOptions controls reconnect timing and concurrent login attempts.
type FleetOptions struct {
	// Capacity of the fleet-wide message queue; zero uses the sum of target buffers.
	MessageBuffer int `json:"message_buffer,omitempty" yaml:"message_buffer,omitempty"`

	// When the fleet message queue is full, discard its oldest item instead of the new item.
	DropOldestMessages bool `json:"drop_oldest_messages,omitempty" yaml:"drop_oldest_messages,omitempty"`

	// Lower bound for reconnect delays; zero selects the default.
	MinReconnectDelay time.Duration `json:"min_reconnect_delay,omitempty" yaml:"min_reconnect_delay,omitempty"`

	// Upper bound for exponential reconnect delays; zero selects the default.
	MaxReconnectDelay time.Duration `json:"max_reconnect_delay,omitempty" yaml:"max_reconnect_delay,omitempty"`

	// Maximum simultaneous Dial calls; zero selects the default.
	MaxConcurrentDials int `json:"max_concurrent_dials,omitempty" yaml:"max_concurrent_dials,omitempty"`

	// Session lifetime after which a failure resets backoff; zero selects the default.
	StableSessionDuration time.Duration `json:"stable_session_duration,omitempty" yaml:"stable_session_duration,omitempty"`

	// Continuous outage duration after which a target stops retrying; zero never expires.
	ExpireAfter time.Duration `json:"expire_after,omitempty" yaml:"expire_after,omitempty"`
}

// TargetStatus is a point-in-time view of one target's connection lifecycle.
type TargetStatus struct {
	// Time when the most recent login succeeded.
	LastConnected *time.Time `json:"last_connected,omitempty" yaml:"last_connected,omitempty"`

	// Start of the current continuous outage; remains stable across failed dials.
	OfflineSince *time.Time `json:"offline_since,omitempty" yaml:"offline_since,omitempty"`

	// Most recent reconnect attempt start.
	LastAttempt *time.Time `json:"last_attempt,omitempty" yaml:"last_attempt,omitempty"`

	// Scheduled next attempt, absent while online, stopped, or expired.
	NextAttempt *time.Time `json:"next_attempt,omitempty" yaml:"next_attempt,omitempty"`

	// Stable target identifier.
	ID string `json:"id" yaml:"id"`

	// Configured remote address.
	Address string `json:"address" yaml:"address"`

	// Current lifecycle state.
	State TargetState `json:"state" yaml:"state"`

	// Most recent dial or session-handler error, if any.
	LastError string `json:"last_error,omitempty" yaml:"last_error,omitempty"`

	// Number of consecutive dial/session failures in the current outage.
	ConsecutiveFailures uint64 `json:"consecutive_failures" yaml:"consecutive_failures"`

	// Selected delay before the currently scheduled reconnect.
	ReconnectDelay time.Duration `json:"reconnect_delay" yaml:"reconnect_delay"`
}

// TargetInfo exposes endpoint identity to a handler without its credentials.
type TargetInfo struct {
	// Stable target identifier.
	ID string `json:"id" yaml:"id"`

	// Configured remote address.
	Address string `json:"address" yaml:"address"`
}

// SessionHandler uses one authenticated client until it returns or ctx ends.
// A returned error ends only that session and causes the fleet to reconnect.
// Handlers should return on command or connection errors and honor ctx.
type SessionHandler func(ctx context.Context, target TargetInfo, client *Client) error

// Fleet supervises independent RCon clients, each with its own UDP socket.
// Construct it with NewFleet and run it until its context is canceled.
type Fleet struct {
	dialSlots chan struct{}           // Bounds simultaneous login handshakes.
	messages  chan FleetMessage       // Bounded stream combining events from all target sessions.
	statuses  map[string]TargetStatus // Latest lifecycle state keyed by target ID.
	clients   map[string]*Client      // Current clients owned by target supervisors.
	targets   []Target                // Immutable target and credential copies.
	options   FleetOptions            // Validated reconnect and concurrency limits.
	dropped   atomic.Uint64           // Events discarded by the fleet-wide message queue.
	statusMu  sync.RWMutex            // Protects status snapshots during updates.
	messageMu sync.Mutex              // Serializes producers while applying the overflow policy.
	running   atomic.Bool             // Prevents overlapping Run calls.
}

// NewFleet validates and copies targets for independent supervised sessions.
// Credentials remain in memory for the fleet lifetime so reconnects can log in.
func NewFleet(targets []Target, options FleetOptions) (*Fleet, error) {
	if len(targets) == 0 {
		return nil, errors.New("RCon fleet requires at least one target")
	}

	options = defaultFleetOptions(options)
	if err := validateFleetOptions(options); err != nil {
		return nil, err
	}

	ownedTargets := make([]Target, len(targets))
	statuses := make(map[string]TargetStatus, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for index, target := range targets {
		if target.ID == "" {
			return nil, errors.New("RCon fleet target ID must not be empty")
		}
		if _, exists := seen[target.ID]; exists {
			return nil, fmt.Errorf("duplicate RCon fleet target ID %q", target.ID)
		}
		if err := validateTargetAddress(target.Address); err != nil {
			return nil, fmt.Errorf("RCon fleet target %q: %w", target.ID, err)
		}

		clientOptions := DefaultOptions()
		if target.Options != nil {
			clientOptions = *target.Options
		}
		if err := validateClientOptions(clientOptions); err != nil {
			return nil, fmt.Errorf("RCon fleet target %q: %w", target.ID, err)
		}

		// Copy pointed-to options so caller mutation cannot change reconnect behavior.
		target.Options = &clientOptions
		ownedTargets[index] = target
		seen[target.ID] = struct{}{}
		statuses[target.ID] = TargetStatus{
			ID:      target.ID,
			Address: target.Address,
			State:   TargetStateStopped,
		}
	}
	if options.MessageBuffer == 0 {
		// Match the combined capacity of the per-target queues by default,
		// so adding targets does not shrink the fleet-wide burst allowance.
		for _, target := range ownedTargets {
			if target.Options.MessageBuffer > int(^uint(0)>>1)-options.MessageBuffer {
				return nil, errors.New("RCon fleet message buffer capacity overflows int")
			}

			options.MessageBuffer += target.Options.MessageBuffer
		}
	}

	return &Fleet{
		targets:   ownedTargets,
		options:   options,
		dialSlots: make(chan struct{}, min(options.MaxConcurrentDials, len(targets))),
		messages:  make(chan FleetMessage, options.MessageBuffer),
		statuses:  statuses,
		clients:   make(map[string]*Client, len(targets)),
	}, nil
}

// Run starts one supervisor per target and returns when ctx is canceled.
// A handler return closes that target's client and schedules a fresh login.
// Run returns ctx.Err after all target workers have stopped.
func (f *Fleet) Run(ctx context.Context, handler SessionHandler) error {
	if f == nil {
		return errors.New("RCon fleet is nil")
	}
	if ctx == nil {
		return errors.New("RCon fleet context is nil")
	}
	if !f.running.CompareAndSwap(false, true) {
		return ErrFleetRunning
	}
	defer f.running.Store(false)

	if err := ctx.Err(); err != nil {
		return err
	}

	var workers sync.WaitGroup
	workers.Add(len(f.targets))
	// Give each target one owner for connection state and retry timing.
	// The shared Fleet only coordinates status and limits concurrent login handshakes.
	for _, target := range f.targets {
		go f.runTarget(ctx, target, handler, &workers)
	}

	<-ctx.Done()
	workers.Wait()

	return ctx.Err()
}

// Status returns a copy of the latest state for targetID.
func (f *Fleet) Status(targetID string) (TargetStatus, bool) {
	if f == nil {
		return TargetStatus{}, false
	}

	f.statusMu.RLock()
	defer f.statusMu.RUnlock()

	status, exists := f.statuses[targetID]

	return cloneTargetStatus(status), exists
}

// Statuses returns detached status snapshots in the same order as configured targets.
func (f *Fleet) Statuses() []TargetStatus {
	if f == nil {
		return nil
	}

	f.statusMu.RLock()
	defer f.statusMu.RUnlock()

	statuses := make([]TargetStatus, 0, len(f.targets))
	for _, target := range f.targets {
		statuses = append(statuses, cloneTargetStatus(f.statuses[target.ID]))
	}

	return statuses
}

// Messages returns the bounded stream of server events from every target session.
// The channel stays open across Run calls; stop readers with the Run context.
// Receives are shared between consumers, so use one reader and fan events out explicitly.
func (f *Fleet) Messages() <-chan FleetMessage {
	if f == nil {
		return nil
	}

	return f.messages
}

// DroppedMessages reports events discarded by the fleet-wide message queue.
func (f *Fleet) DroppedMessages() uint64 {
	if f == nil {
		return 0
	}

	return f.dropped.Load()
}

// Command sends one command through the target's current authenticated client.
// A command is never replayed when the session changes during the call.
func (f *Fleet) Command(ctx context.Context, targetID, command string) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: %s", ErrTargetUnavailable, targetID)
	}
	if ctx == nil {
		return nil, errors.New("RCon fleet command context is nil")
	}

	f.statusMu.RLock()
	client := f.clients[targetID]
	_, exists := f.statuses[targetID]
	f.statusMu.RUnlock()
	if !exists || client == nil {
		return nil, fmt.Errorf("%w: %s", ErrTargetUnavailable, targetID)
	}

	return client.Command(ctx, command)
}

// cloneTime returns a detached copy for point-in-time status snapshots.
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}

	snapshot := *value
	return &snapshot
}

// cloneTargetStatus detaches all timestamp pointers before exposing a snapshot.
func cloneTargetStatus(status TargetStatus) TargetStatus {
	status.LastConnected = cloneTime(status.LastConnected)
	status.OfflineSince = cloneTime(status.OfflineSince)
	status.LastAttempt = cloneTime(status.LastAttempt)
	status.NextAttempt = cloneTime(status.NextAttempt)

	return status
}

// runTarget owns every client and reconnect delay for one configured target.
func (f *Fleet) runTarget(
	ctx context.Context,
	target Target,
	handler SessionHandler,
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	defer func() {
		if ctx.Err() != nil {
			f.stopTarget(target)
		}
	}()

	backoff := f.options.MinReconnectDelay
	info := TargetInfo{ID: target.ID, Address: target.Address}
	clientOptions := *target.Options
	var offlineSince time.Time
	var failures uint64

	for ctx.Err() == nil {
		// Keep each target's retry loop independent.
		// The shared dial semaphore limits login bursts
		// without coupling an offline server to healthy targets.
		f.setConnecting(target)
		if !f.acquireDialSlot(ctx) {
			return
		}

		// A UDP dial can succeed while the server is down; login confirms session readiness.
		f.setAttempt(target)
		client, err := dial(ctx, target.Address, target.Password, &clientOptions, func(message Message) {
			f.publishMessage(FleetMessage{TargetID: target.ID, Message: message})
		})
		f.releaseDialSlot()
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			// Failures belong to one continuous outage;
			// keep its original start time so ExpireAfter measures unavailability,
			// not just the latest failed attempt.
			now := time.Now()
			if offlineSince.IsZero() {
				offlineSince = now
			}

			failures++
			delay := reconnectDelay(backoff, rand.Int63n)
			f.setRetry(target, offlineSince, failures, delay, err)
			if !waitReconnect(ctx, delay, f.expiryDelay(offlineSince)) {
				if f.expired(offlineSince) {
					f.setExpired(target, err)
				}

				return
			}

			backoff = nextBackoff(backoff, f.options.MaxReconnectDelay)
			continue
		}

		connectedAt := time.Now()
		// Successful authentication ends the previous outage even if the application
		// handler immediately returns and asks the supervisor to reconnect.
		offlineSince = time.Time{}
		f.setConnected(target, client)
		sessionCtx, cancelSession := context.WithCancel(ctx)
		stopClose := context.AfterFunc(sessionCtx, func() { _ = client.Close() })
		handlerDone := make(chan error, 1)
		if handler != nil {
			go func() { handlerDone <- handler(sessionCtx, info, client) }()
		}

		// A nil handler leaves the session under transport supervision
		// until the client fails or the fleet context ends.
		// A non-nil handler owns application work.
		select {
		case <-ctx.Done():
			cancelSession()
			stopClose()
			_ = client.Close()
			f.clearClient(target.ID, client)
			return

		case <-client.Done():
			err = client.Err()
			if err == nil {
				err = ErrClosed
			}

		case err = <-handlerDone:
			err = sessionEndError(err)
		}

		cancelSession()
		stopClose()
		// A new login creates a fresh socket and avoids carrying stale request or sequence state forward.
		// This also makes handler completion use the same reconnect path.
		_ = client.Close()
		f.clearClient(target.ID, client)

		if ctx.Err() != nil {
			return
		}

		now := time.Now()
		offlineSince = now
		failures = 1
		// Repeated short sessions indicate a flapping endpoint and grow backoff.
		// A session that stayed healthy long enough resets it,
		// so one planned restart does not inherit an old outage's maximum delay.
		if time.Since(connectedAt) >= f.options.StableSessionDuration {
			backoff = f.options.MinReconnectDelay
		}

		delay := reconnectDelay(backoff, rand.Int63n)
		f.setRetry(target, offlineSince, failures, delay, err)
		if !waitReconnect(ctx, delay, f.expiryDelay(offlineSince)) {
			if f.expired(offlineSince) {
				f.setExpired(target, err)
			}

			return
		}

		backoff = nextBackoff(backoff, f.options.MaxReconnectDelay)
	}
}

// publishMessage copies an event into the bounded fleet stream without blocking a socket reader.
func (f *Fleet) publishMessage(message FleetMessage) {
	message.Message.Data = append([]byte(nil), message.Message.Data...)

	f.messageMu.Lock()
	defer f.messageMu.Unlock()

	if !f.options.DropOldestMessages {
		select {
		case f.messages <- message:
		default:
			f.dropped.Add(1)
		}

		return
	}

	select {
	case f.messages <- message:
		return
	default:
	}

	// Keep delivery non-blocking while replacing the oldest queued event with the newest one.
	// The dropped counter reports every event removed by this policy.
	select {
	case <-f.messages:
		f.dropped.Add(1)
	default:
	}

	select {
	case f.messages <- message:
	default:
		f.dropped.Add(1)
	}
}

// expiryDelay returns the remaining duration before an outage expires.
func (f *Fleet) expiryDelay(offlineSince time.Time) time.Duration {
	if f.options.ExpireAfter == 0 {
		return 0
	}

	return time.Until(offlineSince.Add(f.options.ExpireAfter))
}

// expired reports whether the continuous outage reached the configured limit.
func (f *Fleet) expired(offlineSince time.Time) bool {
	return f.options.ExpireAfter > 0 && !time.Now().Before(offlineSince.Add(f.options.ExpireAfter))
}

// acquireDialSlot waits for a bounded login slot or fleet cancellation.
func (f *Fleet) acquireDialSlot(ctx context.Context) bool {
	select {
	case f.dialSlots <- struct{}{}:
		return true

	case <-ctx.Done():
		return false
	}
}

// releaseDialSlot returns the slot held during Dial.
func (f *Fleet) releaseDialSlot() {
	<-f.dialSlots
}

// setConnected records successful authentication and clears the previous error.
func (f *Fleet) setConnected(target Target, client *Client) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()

	status := f.statuses[target.ID]
	status.State = TargetStateOnline
	now := time.Now()
	status.LastConnected = &now
	status.OfflineSince = nil
	status.NextAttempt = nil
	status.ConsecutiveFailures = 0
	status.ReconnectDelay = 0
	status.LastError = ""
	f.statuses[target.ID] = status
	f.clients[target.ID] = client
}

// setConnecting marks a target while its supervisor is opening a session.
func (f *Fleet) setConnecting(target Target) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()

	status := f.statuses[target.ID]
	status.State = TargetStateConnecting
	status.NextAttempt = nil
	f.statuses[target.ID] = status
}

// setAttempt records the start time for an authenticated dial attempt.
func (f *Fleet) setAttempt(target Target) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()

	status := f.statuses[target.ID]
	now := time.Now()
	status.LastAttempt = &now
	f.statuses[target.ID] = status
}

// setRetry publishes the outage diagnosis and its next bounded retry deadline.
func (f *Fleet) setRetry(
	target Target,
	offlineSince time.Time,
	failures uint64,
	delay time.Duration,
	err error,
) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()

	status := f.statuses[target.ID]
	status.State = TargetStateRetrying
	status.OfflineSince = cloneTime(&offlineSince)
	next := time.Now().Add(delay)
	status.NextAttempt = &next
	status.ConsecutiveFailures = failures
	status.ReconnectDelay = delay
	status.LastError = err.Error()
	f.statuses[target.ID] = status
}

// setExpired preserves the final outage reason after retries stop.
func (f *Fleet) setExpired(target Target, err error) {
	f.statusMu.Lock()
	defer f.statusMu.Unlock()

	status := f.statuses[target.ID]
	status.State = TargetStateExpired
	status.NextAttempt = nil
	status.ReconnectDelay = 0
	if err != nil {
		status.LastError = err.Error()
	}
	f.statuses[target.ID] = status
}

// clearClient removes a client only if it is still the current target session.
func (f *Fleet) clearClient(targetID string, client *Client) {
	f.statusMu.Lock()
	if f.clients[targetID] == client {
		delete(f.clients, targetID)
	}
	f.statusMu.Unlock()
}

// stopTarget records final Run shutdown and drops its client reference.
func (f *Fleet) stopTarget(target Target) {
	f.statusMu.Lock()
	delete(f.clients, target.ID)
	status := f.statuses[target.ID]
	status.State = TargetStateStopped
	status.NextAttempt = nil
	status.ReconnectDelay = 0
	f.statuses[target.ID] = status
	f.statusMu.Unlock()
}

// sessionEndError gives a normal handler return an explicit reconnect reason.
func sessionEndError(err error) error {
	if err != nil {
		return err
	}

	return errors.New("RCon session handler returned")
}

// defaultFleetOptions fills fields whose zero values select documented defaults.
func defaultFleetOptions(options FleetOptions) FleetOptions {
	if options.MinReconnectDelay == 0 {
		options.MinReconnectDelay = DefaultMinReconnectDelay
	}
	if options.MaxReconnectDelay == 0 {
		options.MaxReconnectDelay = DefaultMaxReconnectDelay
	}
	if options.MaxConcurrentDials == 0 {
		options.MaxConcurrentDials = DefaultMaxConcurrentDials
	}
	if options.StableSessionDuration == 0 {
		options.StableSessionDuration = DefaultStableSessionDuration
	}

	return options
}

// validateFleetOptions rejects invalid reconnect timing and concurrency limits.
func validateFleetOptions(options FleetOptions) error {
	if options.MessageBuffer < 0 ||
		options.MinReconnectDelay < 0 ||
		options.MaxReconnectDelay < options.MinReconnectDelay ||
		options.MaxConcurrentDials < 1 ||
		options.StableSessionDuration < 0 ||
		options.ExpireAfter < 0 {
		return errors.New("invalid RCon fleet options")
	}

	return nil
}

// validateTargetAddress checks host:port syntax without resolving DNS yet.
func validateTargetAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("invalid RCon address %q", address)
	}

	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("invalid RCon port in %q", address)
	}

	return nil
}

// reconnectDelay adds equal jitter so target retries do not synchronize.
func reconnectDelay(window time.Duration, randomInt63n func(int64) int64) time.Duration {
	if window <= 1 {
		return window
	}

	// Equal jitter spreads retries uniformly over the upper half of the current backoff window,
	// avoiding both synchronized reconnects and near-zero retry loops.
	half := window / 2
	span := int64(window-half) + 1
	return half + time.Duration(randomInt63n(span))
}

// nextBackoff doubles the retry window without overflowing or passing its cap.
func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}

	return current * 2
}

// waitReconnect sleeps unless fleet cancellation makes another attempt useless.
func waitReconnect(ctx context.Context, delay, expiry time.Duration) bool {
	if expiry < 0 {
		return false
	}
	if expiry == 0 {
		// Zero means expiry is disabled; use the largest representable timer duration
		//  so the regular reconnect delay still remains interruptible by ctx.
		expiry = time.Duration(1<<63 - 1)
	}

	if expiry <= delay {
		// Do not schedule another dial after the outage deadline.
		// Returning false lets the caller publish Expired
		// without starting a connection attempt past the cap.
		timer := time.NewTimer(expiry)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false

		case <-timer.C:
			return false
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return ctx.Err() == nil

	case <-ctx.Done():
		return false
	}
}
