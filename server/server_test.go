// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/woozymasta/bercon/internal/wire"
)

func TestLoginAndCommand(t *testing.T) {
	handled := make(chan struct{}, 1)
	s, err := New("127.0.0.1:0", HandlerFunc(func(_ context.Context, r Request) Response {
		handled <- struct{}{}
		return Response{Data: append([]byte("reply:"), r.Command...)}
	}), &Options{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		_ = s.Close()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned during cleanup: %v", err)
			}

		case <-time.After(time.Second):
			t.Fatal("Serve did not stop")
		}
	}()

	c, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	_ = c.SetDeadline(time.Now().Add(time.Second))
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = c.Write(login)
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}

	p, err := wire.Decode(buf[:n])
	if err != nil || p.Kind != 0 || len(p.Data) != 1 || p.Data[0] != 1 {
		t.Fatalf("login response %#v %v", p, err)
	}

	cmd, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 9, Data: []byte("status")})
	_, _ = c.Write(cmd)
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler did not receive the command")
	}

	n, err = c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}

	p, err = wire.Decode(buf[:n])
	if err != nil || p.Kind != 1 || p.Seq != 9 || string(p.Data) != "reply:status" {
		t.Fatalf("command response %#v %v", p, err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s, err := New(
		"127.0.0.1:0",
		HandlerFunc(func(context.Context, Request) Response { return Response{} }),
		&Options{Password: "secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()
	c, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	_ = c.SetDeadline(time.Now().Add(time.Second))
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("wrong")})
	_, _ = c.Write(login)
	buf := make([]byte, 128)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	p, err := wire.Decode(buf[:n])
	if err != nil || len(p.Data) != 1 || p.Data[0] != 0 {
		t.Fatalf("response %#v %v", p, err)
	}
}

func TestWorkerDropsRequestFromReplacedSession(t *testing.T) {
	var handled atomic.Int32
	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		handled.Add(1)
		return Response{}
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2305}
	oldSession := &Session{server: s, addr: addr, active: true, done: make(chan struct{})}
	currentSession := &Session{server: s, addr: addr, active: true, done: make(chan struct{})}
	s.sessions[addr.String()] = currentSession

	s.workers.Add(1)
	go s.worker()

	// Reuse the same endpoint but replace its session before the queued request runs.
	// The command must not inherit authentication from the new login.
	s.requests <- request{
		addr:    cloneUDP(addr),
		session: oldSession,
		packet:  wire.Packet{Kind: 1, Seq: 1, Data: []byte("status")},
	}
	close(s.requests)
	s.workers.Wait()

	if got := handled.Load(); got != 0 {
		t.Fatalf("handler ran for request from a replaced session: %d times", got)
	}
}

// TestServerRetransmitsMessagesUntilAcknowledged proves retries reuse one sequence.
func TestServerRetransmitsMessagesUntilAcknowledged(t *testing.T) {
	var handled atomic.Int32
	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		if handled.Add(1) == 1 {
			return Response{Data: []byte("ok"), PostMessages: [][]byte{[]byte("event")}}
		}
		return Response{Data: []byte("ok")}
	}), &Options{
		Password:           "secret",
		MessageAttempts:    3,
		MessageRetryWindow: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	readPacket := func() wire.Packet {
		t.Helper()
		n, readErr := peer.Read(buf)
		if readErr != nil {
			t.Fatal(readErr)
		}

		packet, decodeErr := wire.Decode(buf[:n])
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}

		return packet
	}

	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	if packet := readPacket(); packet.Kind != 0 || len(packet.Data) != 1 || packet.Data[0] != 1 {
		t.Fatalf("login response %#v", packet)
	}
	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 1, Data: []byte("status")})
	_, _ = peer.Write(command)
	if packet := readPacket(); packet.Kind != 1 || string(packet.Data) != "ok" {
		t.Fatalf("command response %#v", packet)
	}

	first := readPacket()
	if first.Kind != 2 || string(first.Data) != "event" {
		t.Fatalf("initial server message %#v", first)
	}
	// Leave the first event unacknowledged to force a retry with the same ID.
	retry := readPacket()
	if retry.Kind != 2 || retry.Seq != first.Seq || string(retry.Data) != "event" {
		t.Fatalf("server message retry %#v, first %#v", retry, first)
	}

	ack, _ := wire.Encode(wire.Packet{Kind: 2, Seq: retry.Seq})
	_, _ = peer.Write(ack)
	// Waiting beyond the retry window proves the ACK stopped retransmission.
	time.Sleep(350 * time.Millisecond)
	command, _ = wire.Encode(wire.Packet{Kind: 1, Seq: 2, Data: []byte("status")})
	_, _ = peer.Write(command)
	response := readPacket()
	if response.Kind != 1 || response.Seq != 2 || string(response.Data) != "ok" {
		t.Fatalf("session did not remain active after ACK: %#v", response)
	}
	if got := handled.Load(); got != 2 {
		t.Fatalf("handler ran %d times, want 2", got)
	}
}

func TestMessageACKTimeoutDeauthenticatesSession(t *testing.T) {
	var handled atomic.Int32
	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		handled.Add(1)
		return Response{
			Data:         []byte("ok"),
			PostMessages: [][]byte{[]byte("event")},
		}
	}), &Options{
		Password:           "secret",
		MessageAttempts:    2,
		MessageRetryWindow: 60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() { cancel(); _ = s.Close(); waitForTestServer(t, done) }()

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()

	_ = peer.SetDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1024)
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	if _, err = peer.Read(buf); err != nil {
		t.Fatal(err)
	}

	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 1, Data: []byte("status")})
	_, _ = peer.Write(command)
	for range 3 {
		if _, err = peer.Read(buf); err != nil {
			t.Fatal(err)
		}
	}

	time.Sleep(100 * time.Millisecond)
	_, _ = peer.Write(command)
	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err = peer.Read(buf); err == nil {
		t.Fatal("authenticated command received a response after message ACK exhaustion")
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("handler ran %d times", got)
	}
}

// TestServerSessionExpiryInvalidatesRetainedHandles checks inactivity and message boundaries.
func TestServerSessionExpiryInvalidatesRetainedHandles(t *testing.T) {
	opened := make(chan *Session, 1)
	s, err := New("127.0.0.1:0", HandlerFunc(func(
		_ context.Context,
		_ Request,
	) Response {
		return Response{}
	}), &Options{
		Password:         "secret",
		SessionTimeout:   60 * time.Millisecond,
		MaxDatagramBytes: wire.MultipartHeaderBytes + 1,
		OnSessionOpen:    func(_ context.Context, session *Session) { opened <- session },
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

	conn, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(time.Second))
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	if _, err = conn.Write(login); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}

	reply, err := wire.Decode(buf[:n])
	if err != nil || reply.Kind != 0 || len(reply.Data) != 1 || reply.Data[0] != 1 {
		t.Fatalf("login reply=%#v err=%v", reply, err)
	}

	var session *Session
	select {
	case session = <-opened:
	case <-time.After(time.Second):
		t.Fatal("session-open hook did not run")
	}
	if err = session.SendMessage(context.Background(), make([]byte, 5)); err != ErrMessageTooLarge {
		t.Fatalf("oversized message returned %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for session.IsActive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if session.IsActive() {
		t.Fatal("idle session did not expire")
	}
	remote, ok := session.RemoteAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("RemoteAddr returned %T, want *net.UDPAddr", session.RemoteAddr())
	}
	originalAddress := session.RemoteAddr().String()
	remote.Port++
	if got := session.RemoteAddr().String(); got != originalAddress {
		t.Fatalf("mutating the returned address changed the session endpoint: %s", got)
	}
	if err = session.SendMessage(context.Background(), []byte("late")); err != ErrSessionInactive {
		t.Fatalf("inactive session SendMessage returned %v", err)
	}
}

func TestServerCloseCancelsOutstandingHandlerContext(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	opened := make(chan *Session, 1)
	s, err := New("127.0.0.1:0", HandlerFunc(func(ctx context.Context, _ Request) Response {
		close(started)
		<-ctx.Done()
		close(exited)
		return Response{}
	}), &Options{
		Password:              "secret",
		MaxConcurrentHandlers: 1,
		OnSessionOpen:         func(_ context.Context, session *Session) { opened <- session },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	finished := false
	defer func() {
		if finished {
			return
		}

		cancel()
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned during test cleanup: %v", err)
			}

		case <-time.After(time.Second):
			t.Error("Serve did not stop during test cleanup")
		}
	}()

	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		cancel()
		_ = s.Close()
		t.Fatal("server did not become ready")
	}

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		cancel()
		_ = s.Close()
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()

	_ = peer.SetDeadline(time.Now().Add(time.Second))
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	_ = readServerPacket(t, peer, make([]byte, 128))
	session := <-opened
	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 1, Data: []byte("long")})
	_, _ = peer.Write(command)
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		_ = s.Close()
		t.Fatal("handler did not start")
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("server Close did not terminate the session generation")
	}

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("server shutdown did not cancel the handler context")
	}

	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Serve after explicit Close: %v", err)
		}
		finished = true

	case <-time.After(time.Second):
		t.Fatal("Serve did not finish after Close")
	}
	cancel()
}

func TestExpiredSessionCannotWriteResponseToReauthenticatedEndpoint(t *testing.T) {
	oldStarted := make(chan struct{})
	oldCanceled := make(chan struct{})
	releaseOld := make(chan struct{})
	closeOldRelease := func() {
		select {
		case <-releaseOld:
		default:
			close(releaseOld)
		}
	}

	oldReturned := make(chan struct{})
	opened := make(chan *Session, 2)
	s, err := New("127.0.0.1:0", HandlerFunc(func(ctx context.Context, request Request) Response {
		if string(request.Command) == "old" {
			close(oldStarted)
			<-ctx.Done()
			close(oldCanceled)
			<-releaseOld
			close(oldReturned)
			return Response{Data: []byte("stale")}
		}

		return Response{Data: []byte("current")}
	}), &Options{
		Password:       "secret",
		SessionTimeout: 100 * time.Millisecond,
		OnSessionOpen:  func(_ context.Context, session *Session) { opened <- session },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		closeOldRelease()
		cancel()
		_ = s.Close()
		waitForTestServer(t, done)
	}()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 128)
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	if packet := readServerPacket(t, peer, buf); packet.Kind != 0 || packet.Data[0] != 1 {
		t.Fatalf("initial login response: %#v", packet)
	}
	oldSession := <-opened

	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 1, Data: []byte("old")})
	_, _ = peer.Write(command)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old command handler did not start")
	}

	select {
	case <-oldSession.Done():
	case <-time.After(time.Second):
		t.Fatal("old session did not expire")
	}

	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("session context was not canceled on expiry")
	}

	_, _ = peer.Write(login)
	if packet := readServerPacket(t, peer, buf); packet.Kind != 0 || packet.Data[0] != 1 {
		t.Fatalf("replacement login response: %#v", packet)
	}
	newSession := <-opened
	if newSession == oldSession || !newSession.IsActive() || oldSession.IsActive() {
		t.Fatal("re-login did not create an independent active session generation")
	}

	closeOldRelease()
	select {
	case <-oldReturned:
	case <-time.After(time.Second):
		t.Fatal("old handler did not finish after release")
	}
	_ = peer.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	if n, readErr := peer.Read(buf); readErr == nil {
		packet, decodeErr := wire.Decode(buf[:n])
		t.Fatalf("old session wrote a response after re-login: %#v, decode error %v", packet, decodeErr)
	}

	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	command, _ = wire.Encode(wire.Packet{Kind: 1, Seq: 2, Data: []byte("new")})
	_, _ = peer.Write(command)
	if packet := readServerPacket(t, peer, buf); packet.Kind != 1 || packet.Seq != 2 ||
		string(packet.Data) != "current" {
		t.Fatalf("new session response: %#v", packet)
	}
}

func TestServerKeepaliveBypassesSaturatedHandlerAndRefreshesActivity(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	handled := make(chan string, 2)
	opened := make(chan *Session, 1)
	s, err := New("127.0.0.1:0", HandlerFunc(func(_ context.Context, request Request) Response {
		handled <- string(request.Command)
		close(entered)
		<-release
		return Response{Data: []byte("command-response")}
	}), &Options{
		Password:              "secret",
		SessionTimeout:        time.Second,
		MaxConcurrentHandlers: 1,
		OnSessionOpen:         func(_ context.Context, session *Session) { opened <- session },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		close(release)
		cancel()
		_ = s.Close()
		waitForTestServer(t, done)
	}()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 128)
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	_ = readServerPacket(t, peer, buf)
	session := <-opened

	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 3, Data: []byte("busy")})
	_, _ = peer.Write(command)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("application worker did not start")
	}

	before := session.activity()
	time.Sleep(10 * time.Millisecond)
	keepalive, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 4})
	_, _ = peer.Write(keepalive)
	packet := readServerPacket(t, peer, buf)
	if packet.Kind != 1 || packet.Seq != 4 || len(packet.Data) != 0 {
		t.Fatalf("keepalive response: %#v", packet)
	}
	if !session.activity().After(before) {
		t.Fatal("keepalive did not refresh session activity")
	}
	if got := len(handled); got != 1 {
		t.Fatalf("handler received %d commands, want only the application command", got)
	}
}

func TestPostMessagesDoNotHoldCommandWorkerWhileWaitingForACK(t *testing.T) {
	var commands atomic.Int32
	s, err := New("127.0.0.1:0", HandlerFunc(func(_ context.Context, request Request) Response {
		commands.Add(1)
		messages := make([][]byte, 8)
		for index := range messages {
			messages[index] = []byte("unacknowledged")
		}

		return Response{
			Data:         []byte("response:" + string(request.Command)),
			PostMessages: messages,
		}
	}), &Options{
		Password:              "secret",
		MaxConcurrentHandlers: 1,
		MessageAttempts:       2,
		MessageRetryWindow:    400 * time.Millisecond,
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

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 128)
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	_, _ = peer.Write(login)
	_ = readServerPacket(t, peer, buf)

	command, _ := wire.Encode(wire.Packet{Kind: 1, Seq: 1, Data: []byte("first")})
	_, _ = peer.Write(command)
	if packet := readServerPacket(t, peer, buf); packet.Kind != 1 || string(packet.Data) != "response:first" {
		t.Fatalf("first command response: %#v", packet)
	}
	if packet := readServerPacket(t, peer, buf); packet.Kind != 2 || string(packet.Data) != "unacknowledged" {
		t.Fatalf("first server message: %#v", packet)
	}

	command, _ = wire.Encode(wire.Packet{Kind: 1, Seq: 2, Data: []byte("second")})
	_, _ = peer.Write(command)
	_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	packet := readServerPacket(t, peer, buf)
	if packet.Kind != 1 || packet.Seq != 2 || string(packet.Data) != "response:second" {
		t.Fatalf("second command response while message ACK is pending: %#v", packet)
	}
	if got := commands.Load(); got != 2 {
		t.Fatalf("handler processed %d commands, want 2", got)
	}
	if got := s.DroppedMessages(); got == 0 {
		t.Fatal("bounded post-message queue overflow was not reported")
	}
}

func TestFatalReadErrorTerminatesServeAndIsReturned(t *testing.T) {
	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		return Response{}
	}), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		cancel()
		_ = s.Close()
		t.Fatal("server did not become ready")
	}

	// Serve never sets a read deadline, so this injected timeout
	// represents an unexpected terminal read failure without needing a socket abstraction.
	if err = s.conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("Serve returned nil after a fatal read error")
		}
	case <-time.After(time.Second):
		t.Fatal("fatal read error did not terminate Serve")
	}

	if err = s.Close(); err != nil {
		t.Fatalf("Close after fatal read error: %v", err)
	}
	cancel()
}

func TestBroadcastMessageFansOutConcurrentlyAndReportsEndpoint(t *testing.T) {
	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		return Response{}
	}), &Options{
		Password:              "secret",
		SessionTimeout:        3 * time.Second,
		MaxConcurrentHandlers: 2,
		MessageAttempts:       2,
		MessageRetryWindow:    500 * time.Millisecond,
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

	connect := func() net.Conn {
		t.Helper()
		peer, dialErr := net.Dial("udp", s.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
		login, encodeErr := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, dialErr = peer.Write(login); dialErr != nil {
			t.Fatal(dialErr)
		}
		packet := readServerPacket(t, peer, make([]byte, 128))
		if packet.Kind != 0 || len(packet.Data) != 1 || packet.Data[0] != 1 {
			t.Fatalf("login response: %#v", packet)
		}
		return peer
	}
	responsivePeer := connect()
	defer func() { _ = responsivePeer.Close() }()
	unresponsivePeer := connect()
	defer func() { _ = unresponsivePeer.Close() }()

	ackDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 128)
		n, readErr := responsivePeer.Read(buffer)
		if readErr != nil {
			ackDone <- readErr
			return
		}
		message, decodeErr := wire.Decode(buffer[:n])
		if decodeErr != nil || message.Kind != 2 {
			ackDone <- errors.Join(decodeErr, errors.New("responsive peer received a non-message packet"))
			return
		}
		ack, encodeErr := wire.Encode(wire.Packet{Kind: 2, Seq: message.Seq})
		if encodeErr == nil {
			_, encodeErr = responsivePeer.Write(ack)
		}
		ackDone <- encodeErr
	}()

	broadcastCtx, stopBroadcast := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopBroadcast()
	started := time.Now()
	err = s.BroadcastMessage(broadcastCtx, []byte("news"))
	elapsed := time.Since(started)
	if err == nil || !errors.Is(err, ErrMessageAckTimeout) {
		t.Fatalf("broadcast error=%v, want per-endpoint ACK timeout", err)
	}
	if !strings.Contains(err.Error(), unresponsivePeer.LocalAddr().String()) {
		t.Fatalf("broadcast error %q omits the unresponsive endpoint", err)
	}
	if elapsed >= 800*time.Millisecond {
		t.Fatalf("broadcast took %s; deliveries appear serialized", elapsed)
	}
	if err = <-ackDone; err != nil {
		t.Fatalf("responsive endpoint did not receive its event: %v", err)
	}
}

func TestServerResourceStabilityAcrossFleetCycles(t *testing.T) {
	const peerCount = 20
	const cycles = 3
	baselineGoroutines := runtime.NumGoroutine()
	s, err := New("127.0.0.1:0", HandlerFunc(func(_ context.Context, request Request) Response {
		return Response{Data: append([]byte("ok:"), request.Command...)}
	}), &Options{
		Password:              "secret",
		SessionTimeout:        2 * time.Second,
		MaxClients:            peerCount,
		MaxConcurrentHandlers: 8,
		MessageAttempts:       2,
		MessageRetryWindow:    120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		cancel()
		_ = s.Close()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("Serve returned during resource-cycle cleanup: %v", err)
			}

		case <-time.After(time.Second):
			t.Error("Serve did not stop during resource-cycle cleanup")
		}
	}()

	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	peers := make([]net.Conn, peerCount)
	for index := range peers {
		peers[index], err = net.Dial("udp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = peers[index].SetDeadline(time.Now().Add(2 * time.Second))
		defer func(peer net.Conn) { _ = peer.Close() }(peers[index])
	}

	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	loginAll := func() {
		t.Helper()
		for _, peer := range peers {
			_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
			if _, writeErr := peer.Write(login); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		for index, peer := range peers {
			packet := readServerPacket(t, peer, make([]byte, 128))
			if packet.Kind != 0 || len(packet.Data) != 1 || packet.Data[0] != 1 {
				t.Fatalf("peer %d login response: %#v", index, packet)
			}
		}
	}

	for cycle := range cycles {
		// Re-authentication replaces all retained session handles each cycle.
		loginAll()

		for batchStart := 0; batchStart < peerCount; batchStart += 8 {
			batchEnd := min(batchStart+8, peerCount)
			for index := batchStart; index < batchEnd; index++ {
				command, encodeErr := wire.Encode(wire.Packet{
					Kind: 1,
					Seq:  byte(cycle*peerCount + index),
					Data: []byte(fmt.Sprintf("cycle-%d-target-%d", cycle, index)),
				})
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if _, err = peers[index].Write(command); err != nil {
					t.Fatal(err)
				}
			}
			for index := batchStart; index < batchEnd; index++ {
				packet := readServerPacket(t, peers[index], make([]byte, 256))
				want := fmt.Sprintf("ok:cycle-%d-target-%d", cycle, index)
				if packet.Kind != 1 || string(packet.Data) != want {
					t.Fatalf("cycle %d target %d response: %#v", cycle, index, packet)
				}
			}
		}

		ackDone := make(chan error, peerCount-1)
		for index, peer := range peers[:peerCount-1] {
			go func(index int, peer net.Conn) {
				buffer := make([]byte, 256)
				for {
					_ = peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
					n, readErr := peer.Read(buffer)
					if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
						ackDone <- nil
						return
					}

					if readErr != nil {
						ackDone <- readErr
						return
					}

					message, decodeErr := wire.Decode(buffer[:n])
					if decodeErr != nil || message.Kind != 2 || string(message.Data) != fmt.Sprintf("event-%d", cycle) {
						ackDone <- errors.Join(decodeErr, fmt.Errorf("peer %d received invalid event %#v", index, message))
						return
					}

					ack, encodeErr := wire.Encode(wire.Packet{Kind: 2, Seq: message.Seq})
					if encodeErr == nil {
						_, encodeErr = peer.Write(ack)
					}
					if encodeErr != nil {
						ackDone <- encodeErr
						return
					}
				}
			}(index, peer)
		}

		broadcastCtx, stopBroadcast := context.WithTimeout(context.Background(), time.Second)
		started := time.Now()
		err = s.BroadcastMessage(broadcastCtx, []byte(fmt.Sprintf("event-%d", cycle)))
		elapsed := time.Since(started)
		stopBroadcast()
		if !errors.Is(err, ErrMessageAckTimeout) {
			t.Fatalf("cycle %d broadcast error=%v, want dead-peer ACK timeout", cycle, err)
		}
		if elapsed >= 800*time.Millisecond {
			t.Fatalf("cycle %d broadcast took %s for one dead peer", cycle, elapsed)
		}
		if !strings.Contains(err.Error(), peers[peerCount-1].LocalAddr().String()) {
			t.Fatalf("cycle %d broadcast error omits dead endpoint: %v", cycle, err)
		}
		for range peerCount - 1 {
			if ackErr := <-ackDone; ackErr != nil {
				t.Fatalf("cycle %d live peer did not ACK: %v", cycle, ackErr)
			}
		}

		s.mu.Lock()
		activeSessions := len(s.sessions)
		s.mu.Unlock()
		if activeSessions > peerCount || activeSessions < peerCount-1 {
			t.Fatalf("cycle %d active sessions=%d, want %d or %d", cycle, activeSessions, peerCount, peerCount-1)
		}
		if cycle+1 < cycles {
			// The dead client's old socket would still have unacknowledged event datagrams queued.
			// Replacing it models the process restart that clears that receive queue
			// before the endpoint authenticates again.
			_ = peers[peerCount-1].Close()
			peers[peerCount-1], err = net.Dial("udp", s.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func(peer net.Conn) { _ = peer.Close() }(peers[peerCount-1])
		}
	}

	for _, peer := range peers {
		_ = peer.Close()
	}
	cancel()
	_ = s.Close()
	if err = <-serveDone; err != nil {
		t.Fatalf("Serve returned during normal shutdown: %v", err)
	}
	stopped = true

	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baselineGoroutines+4 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baselineGoroutines+4 {
		t.Fatalf("goroutines after resource cycles=%d, baseline=%d", got, baselineGoroutines)
	}
}

// readServerPacket reads and decodes one packet from a connected test peer.
func readServerPacket(t *testing.T, conn net.Conn, buffer []byte) wire.Packet {
	t.Helper()
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}

	packet, err := wire.Decode(buffer[:n])
	if err != nil {
		t.Fatal(err)
	}

	return packet
}

// TestServerValidatesPhysicalDatagramBoundaries prevents zero-sized fragment chunks.
func TestServerValidatesPhysicalDatagramBoundaries(t *testing.T) {
	for _, size := range []int{wire.MultipartHeaderBytes, wire.MaxUDPDatagramBytes + 1} {
		_, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
			return Response{}
		}), &Options{Password: "secret", MaxDatagramBytes: size})
		if err == nil {
			t.Fatalf("accepted MaxDatagramBytes=%d", size)
		}
	}

	s, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
		return Response{}
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.opts.MaxCommandBytes != wire.MaxCommandBodyBytes {
		t.Fatalf("default command limit=%d, want %d", s.opts.MaxCommandBytes, wire.MaxCommandBodyBytes)
	}
	_ = s.Close()
}

func TestServerRejectsInvalidResourceAndRetryOptions(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name    string  // Short reason the option set must be rejected.
		options Options // Candidate configuration passed to New.
	}{
		{name: "negative handler workers", options: Options{MaxConcurrentHandlers: -1}},
		{name: "handler queue capacity overflow", options: Options{MaxConcurrentHandlers: maxInt}},
		{name: "negative clients", options: Options{MaxClients: -1}},
		{name: "negative command limit", options: Options{MaxCommandBytes: -1}},
		{name: "impossible command limit", options: Options{MaxCommandBytes: wire.MaxUDPDatagramBytes}},
		{name: "datagram smaller than fragment header", options: Options{MaxDatagramBytes: wire.MultipartHeaderBytes}},
		{name: "negative message attempts", options: Options{MessageAttempts: -1}},
		{
			name: "retry interval rounds to zero",
			options: Options{
				MessageAttempts:    2,
				MessageRetryWindow: time.Nanosecond,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New("127.0.0.1:0", HandlerFunc(func(context.Context, Request) Response {
				return Response{}
			}), &test.options)
			if err == nil {
				t.Fatal("invalid options were accepted")
			}
		})
	}
}

// TestServerOverloadKeepsHandlerQueueBounded verifies packet floods cannot grow queued work.
func TestServerOverloadKeepsHandlerQueueBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var handled atomic.Int32
	s, err := New("127.0.0.1:0", HandlerFunc(func(
		_ context.Context,
		_ Request,
	) Response {
		if handled.Add(1) == 1 {
			close(entered)
			<-release
		}
		return Response{}
	}), &Options{Password: "secret", MaxConcurrentHandlers: 1})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		cancel()
		_ = s.Close()
		waitForTestServer(t, done)
		t.Fatal("server did not become ready")
	}
	defer func() {
		close(release)
		cancel()
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned during cleanup: %v", err)
			}

		case <-time.After(time.Second):
			t.Fatal("Serve did not stop")
		}
	}()

	peer, err := net.Dial("udp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()

	_ = peer.SetDeadline(time.Now().Add(time.Second))
	login, _ := wire.Encode(wire.Packet{Kind: 0, Data: []byte("secret")})
	if _, err = peer.Write(login); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	if _, err = peer.Read(buf); err != nil {
		t.Fatal(err)
	}

	for sequence := range 32 {
		packet, encodeErr := wire.Encode(wire.Packet{
			Kind: 1,
			Seq:  byte(sequence),
			Data: []byte("probe"),
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, err = peer.Write(packet); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not receive the first command")
	}

	deadline := time.Now().Add(time.Second)
	for len(s.requests) < cap(s.requests) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, capacity := len(s.requests), cap(s.requests); got != capacity || capacity != 2 {
		t.Fatalf("request queue length/capacity=%d/%d, want 2/2", got, capacity)
	}
	dropDeadline := time.Now().Add(time.Second)
	for s.DroppedCommands() == 0 && time.Now().Before(dropDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := s.DroppedCommands(); got == 0 {
		t.Fatal("full request queue did not increment DroppedCommands")
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("blocked handler ran %d times before release, want 1", got)
	}
}
