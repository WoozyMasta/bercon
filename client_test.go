// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/woozymasta/bercon/internal/testnet"
	"github.com/woozymasta/bercon/internal/wire"
	"github.com/woozymasta/bercon/server"
)

func TestClientCommandAndClose(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(_ context.Context, r server.Request) server.Response {
		return server.Response{Data: append([]byte("ok:"), r.Command...)}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	c, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.Command(context.Background(), "status")
	if err != nil || string(got) != "ok:status" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-c.Done():
	default:
		t.Fatal("Done did not close after explicit Close")
	}
	if c.Err() != nil {
		t.Fatalf("normal Close recorded terminal error %v", c.Err())
	}
	if _, err = c.Command(context.Background(), "again"); err != ErrClosed {
		t.Fatalf("got %v", err)
	}
}

// TestClientKeepalivePreservesIdleSession keeps a session alive past its expiry.
func TestClientKeepalivePreservesIdleSession(t *testing.T) {
	var commands atomic.Int32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		commands.Add(1)
		return server.Response{Data: []byte("user-response")}
	}), &server.Options{
		Password:       "secret",
		SessionTimeout: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 500 * time.Millisecond
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// Idle longer than the server timeout so only keepalive traffic can preserve the session.
	time.Sleep(2300 * time.Millisecond)
	response, err := client.Command(context.Background(), "status")
	if err != nil || string(response) != "user-response" {
		t.Fatalf("response %q, err %v", response, err)
	}
	if got := commands.Load(); got != 1 {
		t.Fatalf("application handler received %d commands, want only the user command", got)
	}
}

// TestClientRetriesDroppedLoginPackets covers retryable setup traffic only.
func TestClientRetriesDroppedLoginPackets(t *testing.T) {
	for _, fault := range []struct {
		name      string
		direction testnet.Direction
	}{
		{name: "request", direction: testnet.ClientToServer},
		{name: "response", direction: testnet.ServerToClient},
	} {
		t.Run(fault.name, func(t *testing.T) {
			runDroppedLoginPacketCase(t, fault.direction)
		})
	}
}

// runDroppedLoginPacketCase drops the first packet in one login direction.
func runDroppedLoginPacketCase(t *testing.T, direction testnet.Direction) {
	t.Helper()
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		return server.Response{Data: append([]byte("ok:"), r.Command...)}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, serveDone) }()

	var dropped atomic.Bool
	relay, err := testnet.NewRelay(s.Addr().String(), func(
		gotDirection testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if gotDirection == direction && len(packet) > 7 && packet[7] == 0 &&
			dropped.CompareAndSwap(false, true) {
			return testnet.Drop
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.LoginAttempts = 3
	opts.LoginTimeout = 50 * time.Millisecond
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatalf("login did not recover from dropped %s: %v", faultDirectionName(direction), err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Command(context.Background(), "status")
	if err != nil || string(response) != "ok:status" {
		t.Fatalf("response %q, err %v", response, err)
	}
	if !dropped.Load() {
		t.Fatal("configured login packet was not dropped")
	}
}

// faultDirectionName returns a short direction label for retry failures.
func faultDirectionName(direction testnet.Direction) string {
	if direction == testnet.ClientToServer {
		return "request"
	}

	return "response"
}

func TestClientMultipartAndServerMessages(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(_ context.Context, _ server.Request) server.Response {
		data := make([]byte, 2000)
		for i := range data {
			data[i] = 'x'
		}
		return server.Response{
			Data:         data,
			PostMessages: [][]byte{[]byte("event")},
		}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()
	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) >= 12 && packet[7] == 1 && packet[9] == 0 {
			return testnet.Reorder
		}

		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0

	c, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	got, err := c.Command(context.Background(), "large")
	if err != nil || len(got) != 2000 || string(got) != strings.Repeat("x", 2000) {
		t.Fatalf("response length %d, err %v", len(got), err)
	}

	select {
	case m := <-c.Messages():
		if string(m.Data) != "event" {
			t.Fatalf("message %q", m.Data)
		}

	case <-time.After(time.Second):
		t.Fatal("message not delivered")
	}
}

func TestClientConcurrentCommands(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(_ context.Context, r server.Request) server.Response {
		return server.Response{Data: append([]byte("reply:"), r.Command...)}
	}), &server.Options{Password: "secret", MaxConcurrentHandlers: 64})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	c, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	const count = 64
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := range count {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()
			command := fmt.Sprintf("cmd-%d", i)
			got, e := c.Command(context.Background(), command)
			if e != nil {
				errCh <- e
				return
			}

			if string(got) != "reply:"+command {
				errCh <- fmt.Errorf("unexpected response %q", got)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestClientSequenceWrapStress exercises every wire sequence across ring rotations.
func TestClientSequenceWrapStress(t *testing.T) {
	var seen [256]atomic.Uint32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		seen[r.Sequence].Add(1)
		return server.Response{Data: []byte("ok")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// Four full rotations catch wrap bugs while keeping only one request in flight.
	for range 1024 {
		response, commandErr := client.Command(context.Background(), "wrap")
		if commandErr != nil || string(response) != "ok" {
			t.Fatalf("response %q, err %v", response, commandErr)
		}
	}

	for sequence := range seen {
		if got := seen[sequence].Load(); got != 4 {
			t.Errorf("sequence %d used %d times, want 4", sequence, got)
		}
	}
}

// TestMessageFloodDoesNotBlockCommands verifies ACK and response progress under
// application-queue backpressure.
func TestMessageFloodDoesNotBlockCommands(t *testing.T) {
	const messageCount = 64
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		messages := make([][]byte, messageCount)
		for i := range messages {
			messages[i] = []byte("event")
		}
		return server.Response{Data: append([]byte("reply:"), r.Command...), PostMessages: messages}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	opts.MessageBuffer = 1
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	for _, command := range []string{"first", "second"} {
		response, commandErr := client.Command(context.Background(), command)
		if commandErr != nil || string(response) != "reply:"+command {
			t.Fatalf("command %q response %q, err %v", command, response, commandErr)
		}
	}

	deadline := time.Now().Add(time.Second)
	for client.DroppedMessages() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.DroppedMessages(); got == 0 {
		t.Fatal("message flood did not exercise the bounded application queue")
	}
}

// TestClientMessageQueueOverflowPolicy preserves the configured end of a full queue.
func TestClientMessageQueueOverflowPolicy(t *testing.T) {
	tests := []struct {
		name       string
		dropOldest bool
		want       [][]byte
	}{
		{name: "drop newest", want: [][]byte{[]byte("1"), []byte("2")}},
		{name: "drop oldest", dropOldest: true, want: [][]byte{[]byte("2"), []byte("3")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{
				opts:     Options{DropOldestMessages: test.dropOldest},
				messages: make(chan Message, 2),
			}
			for _, value := range []string{"1", "2", "3"} {
				client.enqueueMessage(Message{Data: []byte(value)})
			}

			if got := client.DroppedMessages(); got != 1 {
				t.Fatalf("dropped messages=%d, want 1", got)
			}
			for index, want := range test.want {
				if got := <-client.Messages(); !bytes.Equal(got.Data, want) {
					t.Fatalf("message %d=%q, want %q", index, got.Data, want)
				}
			}
		})
	}
}

// TestSeededFaultCampaign checks reproducible UDP response faults over new sessions.
func TestSeededFaultCampaign(t *testing.T) {
	const seed int64 = 0x5eedb3
	for session := range 4 {
		sessionSeed := seed + int64(session)
		t.Run(fmt.Sprintf("seed-%d", sessionSeed), func(t *testing.T) {
			runSeededFaultSession(t, sessionSeed)
		})
	}
}

// runSeededFaultSession applies a shuffled fault set to sequential safe commands.
func runSeededFaultSession(t *testing.T, seed int64) {
	t.Helper()
	const commandCount = 8
	var handled atomic.Int32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		handled.Add(1)
		return server.Response{Data: append([]byte("ok:"), r.Command...)}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, serveDone) }()

	// #nosec G404 -- Predictable choices are required for reproducible fault tests.
	rng := rand.New(rand.NewSource(seed))
	actions := []testnet.Action{
		testnet.Pass,
		testnet.Drop,
		testnet.Duplicate,
		testnet.Corrupt,
		testnet.Truncate,
		testnet.Delay,
	}
	faultKinds := append([]testnet.Action(nil), actions...)
	// Shuffle one complete fault set, then add seeded samples for reproducibility.
	rng.Shuffle(len(actions), func(i, j int) {
		actions[i], actions[j] = actions[j], actions[i]
	})
	for len(actions) < commandCount {
		actions = append(actions, faultKinds[rng.Intn(len(faultKinds))])
	}

	responseIndex := 0
	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction != testnet.ServerToClient || len(packet) < 8 || packet[7] != 1 {
			return testnet.Pass
		}

		action := actions[responseIndex]
		responseIndex++
		return action
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	opts.ResponseTimeout = 100 * time.Millisecond
	opts.SequenceReuseDelay = 0
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatalf("seed %d login failed: %v", seed, err)
	}
	defer func() { _ = client.Close() }()

	for i := range commandCount {
		command := fmt.Sprintf("status-%d", i)
		response, commandErr := client.Command(context.Background(), command)
		fault := actions[i]
		faultLog := relay.Events()
		// Invalid or lost datagrams are discarded; delay and duplication preserve a response.
		shouldTimeout := fault == testnet.Drop ||
			fault == testnet.Corrupt || fault == testnet.Truncate

		if shouldTimeout {
			if !errors.Is(commandErr, ErrResponseTimeout) {
				t.Fatalf("seed %d action %v: err %v, relay events %#v", seed, fault, commandErr, faultLog)
			}
		} else if commandErr != nil || string(response) != "ok:"+command {
			t.Fatalf(
				"seed %d action %v: response %q, err %v, relay events %#v",
				seed, fault, response, commandErr, faultLog,
			)
		}
	}

	if got := handled.Load(); got != commandCount {
		t.Fatalf("seed %d: handler ran %d times, want %d", seed, got, commandCount)
	}
}

func TestCommandIsNotRetriedAfterLostResponse(t *testing.T) {
	var calls atomic.Int32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(_ context.Context, _ server.Request) server.Response {
		calls.Add(1)
		return server.Response{Data: []byte("executed")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 1 {
			return testnet.Drop
		}

		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	opts.ResponseTimeout = 100 * time.Millisecond
	c, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.Command(context.Background(), "mutate")
	if !errors.Is(err, ErrResponseTimeout) || !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatalf("timeout did not expose unknown command outcome: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler executed %d times", got)
	}
}

// TestCommandCancellationAfterWriteReturnsUnknownOutcome cancels only after server execution.
func TestCommandCancellationAfterWriteReturnsUnknownOutcome(t *testing.T) {
	executed := make(chan struct{}, 1)
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		executed <- struct{}{}
		return server.Response{Data: []byte("done")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancelServer := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(serverCtx) }()
	defer func() { cancelServer(); _ = s.Close(); waitForTestServer(t, done) }()

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 1 {
			return testnet.Drop
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, commandErr := client.Command(ctx, "side-effect")
		callDone <- commandErr
	}()
	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("server did not execute command before cancellation")
	}
	cancel()
	select {
	case err = <-callDone:
	case <-time.After(time.Second):
		t.Fatal("Command did not return after context cancellation")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatalf("post-write cancellation error=%v", err)
	}
}

func TestClientAcceptsReorderedResponses(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(_ context.Context, r server.Request) server.Response {
		return server.Response{Data: append([]byte("reply:"), r.Command...)}
	}), &server.Options{Password: "secret", MaxConcurrentHandlers: 2})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 1 {
			return testnet.Reorder
		}

		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	c, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var wg sync.WaitGroup
	for _, command := range []string{"first", "second"} {
		wg.Add(1)
		go func(command string) {
			defer wg.Done()
			got, callErr := c.Command(context.Background(), command)
			if callErr != nil {
				t.Errorf("%s: %v", command, callErr)
				return
			}

			if string(got) != "reply:"+command {
				t.Errorf("%s: response %q", command, got)
			}
		}(command)
	}
	wg.Wait()
}

// TestClientDoneAndErrExposeFatalReadFailure checks terminal lifecycle reporting.
func TestClientDoneAndErrExposeFatalReadFailure(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.conn.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("Client.Done did not close after fatal socket read")
	}
	if client.Err() == nil {
		t.Fatal("Client.Err returned nil after fatal socket read")
	}
	if _, ok := <-client.Messages(); ok {
		t.Fatal("Messages remained open after Client.Done")
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestKeepaliveSurvivesOneLostProbe proves a single missed exchange is recoverable.
func TestKeepaliveSurvivesOneLostProbe(t *testing.T) {
	var dropped atomic.Bool
	var keepalives atomic.Int32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	}), &server.Options{Password: "secret", SessionTimeout: 800 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ClientToServer && len(packet) == wire.CommandHeaderBytes &&
			packet[7] == 1 {
			keepalives.Add(1)
			if dropped.CompareAndSwap(false, true) {
				return testnet.Drop
			}
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 120 * time.Millisecond
	opts.ResponseTimeout = 50 * time.Millisecond
	opts.MaxKeepAliveFailures = 2
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	deadline := time.NewTimer(600 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for keepalives.Load() < 3 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("only %d keepalive packets reached the relay", keepalives.Load())
		}
	}
	if !dropped.Load() {
		t.Fatal("the relay did not drop the first keepalive")
	}
	if client.Err() != nil {
		t.Fatalf("one dropped keepalive made the client terminal: %v", client.Err())
	}
}

// TestClientKeepaliveStopsAfterServerShutdown verifies unanswered probes make the client terminal.
func TestClientKeepaliveStopsAfterServerShutdown(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		cancel()
		_ = s.Close()
		waitForTestServer(t, serveDone)
		t.Fatal("server did not become ready")
	}

	opts := DefaultOptions()
	opts.KeepAliveInterval = 40 * time.Millisecond
	opts.ResponseTimeout = 30 * time.Millisecond
	opts.MaxKeepAliveFailures = 1
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		cancel()
		_ = s.Close()
		waitForTestServer(t, serveDone)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		_ = s.Close()
		waitForTestServer(t, serveDone)
	})

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client stayed live after the configured keepalive failure")
	}

	terminalErr := client.Err()
	var networkErr *net.OpError
	if terminalErr == nil || (!errors.Is(terminalErr, ErrResponseTimeout) &&
		!errors.As(terminalErr, &networkErr)) {
		t.Fatalf("terminal error = %v, want timeout or terminal UDP transport failure", terminalErr)
	}
}

// TestTimedOutCommandDropsMultipartState keeps only sequence quarantine for late fragments.
func TestTimedOutCommandDropsMultipartState(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 1 {
			return testnet.Drop
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	opts.ResponseTimeout = 30 * time.Millisecond
	opts.SequenceReuseDelay = time.Hour
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Command(context.Background(), "large")
	if !errors.Is(err, ErrResponseTimeout) || !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatalf("timeout error does not expose uncertain outcome: %v", err)
	}

	client.mu.Lock()
	if len(client.pending) != 0 || !client.used[0] || len(client.quarantineTimers) != 1 {
		t.Fatalf("pending=%d used=%v timers=%d", len(client.pending), client.used[0], len(client.quarantineTimers))
	}
	client.mu.Unlock()
	client.route(wire.Packet{Kind: 1, Seq: 0, Parts: 2, Part: 0, Data: make([]byte, 60_000)})
	client.mu.Lock()
	if len(client.pending) != 0 {
		t.Fatalf("late multipart fragment recreated pending state: %d", len(client.pending))
	}
	client.mu.Unlock()

	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.quarantineTimers) != 0 {
		t.Fatalf("Close retained %d quarantine timers", len(client.quarantineTimers))
	}
}

// TestClientHandlesUnsolicitedMessageBeforeLoginResponse checks reordered 0x02 traffic.
func TestClientHandlesUnsolicitedMessageBeforeLoginResponse(t *testing.T) {
	callbackDone := make(chan error, 1)
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("OK"), PostMessages: [][]byte{[]byte("later")}}
	}), &server.Options{
		Password: "secret",
		OnSessionOpen: func(ctx context.Context, session *server.Session) {
			callbackDone <- session.SendMessage(ctx, []byte("before-login"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 0 {
			return testnet.Reorder
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.LoginTimeout = time.Second
	opts.LoginAttempts = 1
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatalf("Dial failed while ACKing pre-login event: %v", err)
	}
	defer func() { _ = client.Close() }()
	select {
	case err = <-callbackDone:
		if err != nil {
			t.Fatalf("server message was not ACKed during Dial: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server message sender did not observe its ACK")
	}

	select {
	case message := <-client.Messages():
		if string(message.Data) != "before-login" {
			t.Fatalf("first message=%q", message.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-login message was not preserved")
	}
	response, err := client.Command(context.Background(), "status")
	if err != nil || string(response) != "OK" {
		t.Fatalf("response=%q err=%v", response, err)
	}
	select {
	case message := <-client.Messages():
		if string(message.Data) != "later" {
			t.Fatalf("post-command message=%q", message.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("post-command event-stream message was not delivered")
	}
}

func TestClientBoundsMessagesReceivedBeforeLoginCompletes(t *testing.T) {
	const messageCount = 48
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverConn.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, wire.MaxUDPReadBufferBytes)
		_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, clientAddr, readErr := serverConn.ReadFromUDP(buffer)
		if readErr != nil {
			serverDone <- readErr
			return
		}

		login, decodeErr := wire.Decode(buffer[:n])
		if decodeErr != nil || login.Kind != 0 {
			serverDone <- fmt.Errorf("invalid login packet: %#v, %w", login, decodeErr)
			return
		}

		for sequence := range messageCount {
			packet, encodeErr := wire.Encode(wire.Packet{
				Kind: 2,
				Seq:  byte(sequence),
				Data: []byte("early-event"),
			})
			if encodeErr != nil {
				serverDone <- encodeErr
				return
			}

			if _, readErr = serverConn.WriteToUDP(packet, clientAddr); readErr != nil {
				serverDone <- readErr
				return
			}
		}

		seen := make(map[byte]bool, messageCount)
		for len(seen) < messageCount {
			n, _, readErr = serverConn.ReadFromUDP(buffer)
			if readErr != nil {
				serverDone <- fmt.Errorf("read early-event ACKs (%d/%d): %w", len(seen), messageCount, readErr)
				return
			}

			ack, decodeErr := wire.Decode(buffer[:n])
			if decodeErr != nil || ack.Kind != 2 {
				serverDone <- fmt.Errorf("invalid early-event ACK: %#v, %w", ack, decodeErr)
				return
			}

			seen[ack.Seq] = true
		}

		response, encodeErr := wire.Encode(wire.Packet{Kind: 0, Data: []byte{1}})
		if encodeErr == nil {
			_, encodeErr = serverConn.WriteToUDP(response, clientAddr)
		}
		serverDone <- encodeErr
	}()

	options := DefaultOptions()
	options.LoginTimeout = time.Second
	options.LoginAttempts = 1
	options.KeepAliveInterval = 0
	options.MessageBuffer = 2
	client, err := Dial(context.Background(), serverConn.LocalAddr().String(), "secret", &options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	const wantDropped = messageCount - 2
	if got := client.DroppedMessages(); got != wantDropped {
		t.Fatalf("dropped pre-login messages=%d, want %d", got, wantDropped)
	}

	for sequence := range options.MessageBuffer {
		select {
		case message := <-client.Messages():
			if message.Sequence != byte(sequence) || string(message.Data) != "early-event" {
				t.Fatalf("buffered message %d = %#v", sequence, message)
			}
		case <-time.After(time.Second):
			t.Fatalf("buffered message %d was not delivered", sequence)
		}
	}
}

func TestMessageBurstACKsDoNotStarveCommands(t *testing.T) {
	const messageCount = 96
	burstDone := make(chan error, 1)
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("command-ok")}
	}), &server.Options{
		Password:           "secret",
		MessageAttempts:    3,
		MessageRetryWindow: 900 * time.Millisecond,
		OnSessionOpen: func(ctx context.Context, session *server.Session) {
			go func() {
				var sends sync.WaitGroup
				errorsBySend := make(chan error, messageCount)

				for sequence := range messageCount {
					sends.Add(1)
					go func(sequence int) {
						defer sends.Done()
						errorsBySend <- session.SendMessage(ctx, []byte(fmt.Sprintf("event-%d", sequence)))
					}(sequence)
				}

				sends.Wait()
				close(errorsBySend)

				var failures []error
				for sendErr := range errorsBySend {
					if sendErr != nil {
						failures = append(failures, sendErr)
					}
				}
				burstDone <- errors.Join(failures...)
			}()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, serveDone) }()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	options := DefaultOptions()
	options.KeepAliveInterval = 0
	options.MessageBuffer = 1
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	commandDone := make(chan error, 1)
	go func() {
		response, commandErr := client.Command(context.Background(), "during-burst")
		if commandErr == nil && string(response) != "command-ok" {
			commandErr = fmt.Errorf("unexpected response %q", response)
		}
		commandDone <- commandErr
	}()

	select {
	case err = <-commandDone:
		if err != nil {
			t.Fatalf("command failed during ACK burst: %v", err)
		}

	case <-time.After(time.Second):
		t.Fatal("command was starved by protocol ACK traffic")
	}

	select {
	case err = <-burstDone:
		if err != nil {
			t.Fatalf("server message burst was not fully acknowledged: %v", err)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish the ACK burst")
	}

	response, err := client.Command(context.Background(), "after-burst")
	if err != nil || string(response) != "command-ok" {
		t.Fatalf("command after ACK burst: response=%q err=%v", response, err)
	}
	if err = client.Err(); err != nil {
		t.Fatalf("ACK burst made the client terminal: %v", err)
	}
}

// TestClientCommandBoundaryChecksCanonicalPayloadLimits verifies the exact command body limit.
func TestClientCommandBoundaryChecksCanonicalPayloadLimits(t *testing.T) {
	var handled atomic.Int32
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		handled.Add(1)
		return server.Response{Data: []byte(strings.Repeat("x", wire.MaxCommandBodyBytes))}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	response, err := client.Command(context.Background(), strings.Repeat("x", wire.MaxCommandBodyBytes))
	if err != nil || len(response) != wire.MaxCommandBodyBytes {
		t.Fatalf("max command response length=%d err=%v", len(response), err)
	}
	if _, err = client.Command(context.Background(), strings.Repeat("x", wire.MaxCommandBodyBytes+1)); !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("oversized command returned %v", err)
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("oversized command reached server; handled=%d", got)
	}
}

func TestClientOptionsRequireKeepaliveFailureLimitWhenEnabled(t *testing.T) {
	options := DefaultOptions()
	options.KeepAliveInterval = time.Second
	options.MaxKeepAliveFailures = 0
	if err := validateClientOptions(options); err == nil {
		t.Fatal("accepted keepalive without a failure limit")
	}

	options.KeepAliveInterval = 0
	if err := validateClientOptions(options); err != nil {
		t.Fatalf("disabled keepalive rejected zero failure limit: %v", err)
	}

	options.MaxKeepAliveFailures = -1
	if err := validateClientOptions(options); err == nil {
		t.Fatal("accepted a negative keepalive failure limit")
	}
}

// TestClientMultipartAssemblyEdges checks ordering, duplicates, inconsistent metadata, and bounds.
func TestClientMultipartAssemblyEdges(t *testing.T) {
	tests := []struct {
		name    string
		packets []wire.Packet
		limit   int
		want    string
		wantErr error
	}{
		{
			name: "reordered and identical duplicate",
			packets: []wire.Packet{
				{Kind: 1, Seq: 7, Parts: 2, Part: 1, Data: []byte("b")},
				{Kind: 1, Seq: 7, Parts: 2, Part: 1, Data: []byte("b")},
				{Kind: 1, Seq: 7, Parts: 2, Part: 0, Data: []byte("a")},
			},
			limit: 2, want: "ab",
		},
		{
			name: "conflicting duplicate",
			packets: []wire.Packet{
				{Kind: 1, Seq: 7, Parts: 2, Part: 0, Data: []byte("a")},
				{Kind: 1, Seq: 7, Parts: 2, Part: 0, Data: []byte("x")},
			},
			limit: 2, wantErr: ErrProtocol,
		},
		{
			name: "inconsistent part count",
			packets: []wire.Packet{
				{Kind: 1, Seq: 7, Parts: 2, Part: 0, Data: []byte("a")},
				{Kind: 1, Seq: 7, Parts: 3, Part: 1, Data: []byte("b")},
			},
			limit: 3, wantErr: ErrProtocol,
		},
		{
			name:    "maximum response size",
			packets: []wire.Packet{{Kind: 1, Seq: 7, Data: []byte("ab")}},
			limit:   1, wantErr: ErrResponseTooLarge,
		},
		{
			name: "empty fragments count once",
			packets: []wire.Packet{
				{Kind: 1, Seq: 7, Parts: 2, Part: 0},
				{Kind: 1, Seq: 7, Parts: 2, Part: 0},
				{Kind: 1, Seq: 7, Parts: 2, Part: 1, Data: []byte("b")},
			},
			limit: 1, want: "b",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pending := &pendingCommand{result: make(chan commandResult, 1)}
			client := &Client{opts: Options{MaxResponseBytes: test.limit}, pending: map[byte]*pendingCommand{7: pending}}
			for _, packet := range test.packets {
				client.route(packet)
			}
			select {
			case result := <-pending.result:
				if test.wantErr != nil && !errors.Is(result.err, test.wantErr) {
					t.Fatalf("result error=%v, want %v", result.err, test.wantErr)
				}
				if test.wantErr == nil && (result.err != nil || string(result.data) != test.want) {
					t.Fatalf("result=%#v, want data %q", result, test.want)
				}
			default:
				t.Fatal("response assembly did not complete")
			}
		})
	}
}

// TestServerResponseDatagramBoundaries checks the maximum single packet and multipart payload.
func TestServerResponseDatagramBoundaries(t *testing.T) {
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		switch string(r.Command) {
		case "single":
			return server.Response{Data: []byte(strings.Repeat("s", wire.MaxCommandBodyBytes))}
		case "multipart":
			return server.Response{Data: []byte(strings.Repeat("m", wire.MaxCommandBodyBytes+1))}
		default:
			return server.Response{}
		}
	}), &server.Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	var sizesMu sync.Mutex
	var sizes []int
	relay, err := testnet.NewRelay(s.Addr().String(), func(
		direction testnet.Direction,
		packet []byte,
		_ int,
	) testnet.Action {
		if direction == testnet.ServerToClient && len(packet) > 7 && packet[7] == 1 {
			sizesMu.Lock()
			sizes = append(sizes, len(packet))
			sizesMu.Unlock()
		}
		return testnet.Pass
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = relay.Close() }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), relay.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	single, err := client.Command(context.Background(), "single")
	if err != nil || len(single) != wire.MaxCommandBodyBytes {
		t.Fatalf("single response length=%d err=%v", len(single), err)
	}
	empty, err := client.Command(context.Background(), "empty")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty response=%q err=%v", empty, err)
	}
	multipart, err := client.Command(context.Background(), "multipart")
	if err != nil || len(multipart) != wire.MaxCommandBodyBytes+1 {
		t.Fatalf("multipart response length=%d err=%v", len(multipart), err)
	}

	sizesMu.Lock()
	gotSizes := append([]int(nil), sizes...)
	sizesMu.Unlock()
	wantSizes := []int{wire.CanonicalMaxDatagramBytes, wire.CommandHeaderBytes,
		wire.CanonicalMaxDatagramBytes, wire.MultipartHeaderBytes + 4}
	if len(gotSizes) != len(wantSizes) {
		t.Fatalf("response datagram sizes=%v, want %v", gotSizes, wantSizes)
	}
	for index := range wantSizes {
		if gotSizes[index] != wantSizes[index] {
			t.Fatalf("response datagram sizes=%v, want %v", gotSizes, wantSizes)
		}
	}
}

// TestSequenceExhaustionWaitsAndWakes checks all 256 values and release notification.
func TestSequenceExhaustionWaitsAndWakes(t *testing.T) {
	client := &Client{closed: make(chan struct{}), wake: make(chan struct{}, 1), pending: make(map[byte]*pendingCommand)}
	for index := range client.used {
		client.used[index] = true
	}
	result := make(chan byte, 1)
	go func() {
		seq, err := client.acquire(context.Background())
		if err != nil {
			result <- 0xff
			return
		}
		result <- seq
	}()
	select {
	case seq := <-result:
		t.Fatalf("acquire returned sequence %d while all slots were occupied", seq)
	case <-time.After(10 * time.Millisecond):
	}

	client.release(42)
	select {
	case seq := <-result:
		if seq != 42 {
			t.Fatalf("acquired sequence %d after releasing 42", seq)
		}
	case <-time.After(time.Second):
		t.Fatal("sequence release did not wake blocked caller")
	}
}

// TestCanceledBeforeEnqueueIsKnownNotSent distinguishes pre-write cancellation.
func TestCanceledBeforeEnqueueIsKnownNotSent(t *testing.T) {
	client := &Client{
		normal: make(chan writeRequest, 1), closed: make(chan struct{}),
		wake: make(chan struct{}, 1), pending: make(map[byte]*pendingCommand),
		quarantineTimers: make(map[byte]*time.Timer),
	}
	client.normal <- writeRequest{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Command(ctx, "status")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatalf("pre-enqueue cancellation error=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.pending) != 0 || client.used[0] {
		t.Fatalf("pre-enqueue cancellation leaked state: pending=%d used=%v", len(client.pending), client.used[0])
	}
}

// TestCloseCompletesAllInflightCommands verifies terminal shutdown wakes every waiter.
func TestCloseCompletesAllInflightCommands(t *testing.T) {
	const commandCount = 24
	started := make(chan struct{}, commandCount)
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		ctx context.Context,
		_ server.Request,
	) server.Response {
		started <- struct{}{}
		<-ctx.Done()
		return server.Response{}
	}), &server.Options{Password: "secret", MaxConcurrentHandlers: commandCount})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Serve(serverCtx) }()
	defer func() { cancelServer(); _ = s.Close(); waitForTestServer(t, serverDone) }()

	opts := DefaultOptions()
	opts.KeepAliveInterval = 0
	client, err := Dial(context.Background(), s.Addr().String(), "secret", &opts)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, commandCount)
	for index := range commandCount {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, callErr := client.Command(context.Background(), fmt.Sprintf("close-%d", index))
			errorsCh <- callErr
		}(index)
	}
	for range commandCount {
		select {
		case <-started:
		case <-time.After(time.Second):
			_ = client.Close()
			t.Fatal("server did not receive all in-flight commands")
		}
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errorsCh)
	for callErr := range errorsCh {
		if !errors.Is(callErr, ErrClosed) {
			t.Errorf("in-flight command returned %v after Close", callErr)
		}
	}
}
