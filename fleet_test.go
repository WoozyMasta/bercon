// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/woozymasta/bercon/server"
)

func TestNewFleetValidatesAndCopiesTargets(t *testing.T) {
	options := DefaultOptions()
	options.LoginTimeout = 50 * time.Millisecond
	options.LoginAttempts = 1
	options.ResponseTimeout = 50 * time.Millisecond
	options.KeepAliveInterval = 0
	targets := []Target{{
		ID:       "alpha",
		Address:  "127.0.0.1:2305",
		Password: "secret",
		Options:  &options,
	}}
	fleet, err := NewFleet(targets, FleetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cap(fleet.Messages()); got != DefaultOptions().MessageBuffer {
		t.Fatalf("default fleet message buffer=%d, want %d", got, DefaultOptions().MessageBuffer)
	}

	options.KeepAliveInterval = time.Second
	if got := fleet.targets[0].Options.KeepAliveInterval; got != 0 {
		t.Fatalf("fleet options changed after construction: %s", got)
	}

	encoded, err := json.Marshal(fleet.targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("serialized target exposed its password")
	}

	for _, invalid := range []Target{
		{Address: "127.0.0.1:2305"},
		{ID: "empty-address"},
		{ID: "bad-port", Address: "127.0.0.1:0"},
	} {
		if _, err = NewFleet([]Target{invalid}, FleetOptions{}); err == nil {
			t.Fatalf("NewFleet accepted invalid target %#v", invalid)
		}
	}

	if _, err = NewFleet([]Target{targets[0], targets[0]}, FleetOptions{}); err == nil {
		t.Fatal("NewFleet accepted duplicate target IDs")
	}
	if _, err = NewFleet([]Target{targets[0]}, FleetOptions{MessageBuffer: -1}); err == nil {
		t.Fatal("NewFleet accepted a negative message buffer")
	}
}

func TestFleetStatusesReturnsOrderedDetachedSnapshots(t *testing.T) {
	fleet, err := NewFleet([]Target{
		{ID: "eu-1", Address: "127.0.0.1:2305"},
		{ID: "eu-2", Address: "127.0.0.1:2405"},
	}, FleetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	fleet.statusMu.Lock()
	connected := time.Now()
	status := fleet.statuses["eu-1"]
	status.LastConnected = &connected
	fleet.statuses["eu-1"] = status
	fleet.statusMu.Unlock()

	snapshots := fleet.Statuses()
	if len(snapshots) != 2 || snapshots[0].ID != "eu-1" || snapshots[1].ID != "eu-2" {
		t.Fatalf("statuses are not in target order: %#v", snapshots)
	}
	if snapshots[0].LastConnected == nil {
		t.Fatal("status snapshot omitted LastConnected")
	}

	snapshots[0].ID = "changed"
	*snapshots[0].LastConnected = time.Time{}

	unchanged, ok := fleet.Status("eu-1")
	if !ok || unchanged.ID != "eu-1" || !unchanged.LastConnected.Equal(connected) {
		t.Fatalf("snapshot mutation reached fleet state: %#v", unchanged)
	}
}

// TestFleetMessagesAddsTargetIdentityToServerEvents verifies the aggregate stream.
func TestFleetMessagesAddsTargetIdentityToServerEvents(t *testing.T) {
	s, serveCancel, serveDone := startFleetTestServer(t, "127.0.0.1:0", func(
		_ context.Context,
		request server.Request,
	) server.Response {
		return server.Response{
			Data:         []byte("ok"),
			PostMessages: [][]byte{append([]byte(nil), request.Command...)},
		}
	})
	defer stopFleetTestServer(t, s, serveCancel, serveDone)

	options := DefaultOptions()
	options.KeepAliveInterval = 0
	targets := []Target{
		{ID: "alpha", Address: s.Addr().String(), Password: "secret", Options: &options},
		{ID: "beta", Address: s.Addr().String(), Password: "secret", Options: &options},
	}
	fleet, err := NewFleet(targets, FleetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	clientEvents := make(chan FleetMessage, len(targets))
	go func() {
		runDone <- fleet.Run(ctx, func(
			sessionCtx context.Context,
			target TargetInfo,
			client *Client,
		) error {
			for {
				select {
				case <-sessionCtx.Done():
					return sessionCtx.Err()

				case message, ok := <-client.Messages():
					if !ok {
						return client.Err()
					}
					clientEvents <- FleetMessage{TargetID: target.ID, Message: message}
				}
			}
		})
	}()
	defer func() {
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Fleet.Run returned %v after cancellation", err)
		}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		statuses := fleet.Statuses()
		if statuses[0].State == TargetStateOnline && statuses[1].State == TargetStateOnline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Fleet targets did not connect: %#v", statuses)
		}
		time.Sleep(time.Millisecond)
	}

	for _, targetID := range []string{"alpha", "beta"} {
		if response, commandErr := fleet.Command(ctx, targetID, targetID); commandErr != nil ||
			string(response) != "ok" {
			t.Fatalf("command for %q returned %q, err %v", targetID, response, commandErr)
		}
	}

	seen := make(map[string]bool, len(targets))
	for range len(targets) {
		select {
		case event := <-fleet.Messages():
			if string(event.Message.Data) != event.TargetID {
				t.Fatalf("event for %q contains %q", event.TargetID, event.Message.Data)
			}
			seen[event.TargetID] = true

		case <-time.After(time.Second):
			t.Fatalf("fleet events missing; received targets %v", seen)
		}
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("fleet events came from targets %v", seen)
	}

	for range len(targets) {
		select {
		case event := <-clientEvents:
			if string(event.Message.Data) != event.TargetID {
				t.Fatalf("client event for %q contains %q", event.TargetID, event.Message.Data)
			}

		case <-time.After(time.Second):
			t.Fatal("client message stream lost events consumed by Fleet.Messages")
		}
	}
	if got := fleet.DroppedMessages(); got != 0 {
		t.Fatalf("fleet dropped %d messages without queue pressure", got)
	}
}

// TestFleetMessageQueueOverflowPolicy checks both bounded-queue policies.
func TestFleetMessageQueueOverflowPolicy(t *testing.T) {
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
			fleet, err := NewFleet([]Target{{ID: "alpha", Address: "127.0.0.1:2305"}},
				FleetOptions{MessageBuffer: 2, DropOldestMessages: test.dropOldest})
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"1", "2", "3"} {
				fleet.publishMessage(FleetMessage{
					TargetID: "alpha",
					Message:  Message{Data: []byte(value)},
				})
			}

			if got := fleet.DroppedMessages(); got != 1 {
				t.Fatalf("dropped messages=%d, want 1", got)
			}
			for index, want := range test.want {
				if got := <-fleet.Messages(); !bytes.Equal(got.Message.Data, want) {
					t.Fatalf("message %d=%q, want %q", index, got.Message.Data, want)
				}
			}
		})
	}
}

func TestFleetUsesIndependentSessions(t *testing.T) {
	var sessionsMu sync.Mutex
	sessions := make(map[*server.Session]struct{})
	s, serveCancel, serveDone := startFleetTestServer(t, "127.0.0.1:0", func(
		_ context.Context,
		request server.Request,
	) server.Response {
		sessionsMu.Lock()
		sessions[request.Session] = struct{}{}
		sessionsMu.Unlock()
		return server.Response{Data: append([]byte("ok:"), request.Command...)}
	})
	defer stopFleetTestServer(t, s, serveCancel, serveDone)

	options := DefaultOptions()
	options.LoginTimeout = 50 * time.Millisecond
	options.LoginAttempts = 1
	options.ResponseTimeout = 50 * time.Millisecond
	options.KeepAliveInterval = 0
	fleet, err := NewFleet([]Target{
		{
			ID:       "alpha",
			Address:  s.Addr().String(),
			Password: "secret",
			Options:  &options,
		},
		{
			ID:       "beta",
			Address:  s.Addr().String(),
			Password: "secret",
			Options:  &options,
		},
	}, FleetOptions{MinReconnectDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connected := make(chan string, 2)
	runDone := make(chan error, 1)
	go func() {
		runDone <- fleet.Run(ctx, func(
			sessionCtx context.Context,
			target TargetInfo,
			client *Client,
		) error {
			response, commandErr := client.Command(sessionCtx, "probe")
			if commandErr != nil {
				return commandErr
			}
			if string(response) != "ok:probe" {
				return fmt.Errorf("unexpected response %q", response)
			}

			connected <- target.ID
			<-sessionCtx.Done()
			return sessionCtx.Err()
		})
	}()

	seen := make(map[string]bool, 2)
	for range 2 {
		select {
		case id := <-connected:
			seen[id] = true

		case <-time.After(2 * time.Second):
			t.Fatalf("fleet did not connect both targets: alpha=%#v beta=%#v",
				fleetStatusForTest(fleet, "alpha"), fleetStatusForTest(fleet, "beta"))
		}
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("connected target IDs: %v", seen)
	}

	sessionsMu.Lock()
	if len(sessions) != 2 {
		sessionsMu.Unlock()
		t.Fatalf("server observed %d sessions; want one per target", len(sessions))
	}
	sessionsMu.Unlock()

	for _, id := range []string{"alpha", "beta"} {
		status, ok := fleet.Status(id)
		if !ok || status.State != TargetStateOnline || status.LastConnected == nil {
			t.Fatalf("unexpected status for %q: %#v, exists=%t", id, status, ok)
		}

		*status.LastConnected = time.Time{}
		current, _ := fleet.Status(id)
		if current.LastConnected == nil || current.LastConnected.IsZero() {
			t.Fatalf("status snapshot for %q mutated fleet state", id)
		}
	}
	if err = fleet.Run(
		ctx,
		func(context.Context, TargetInfo, *Client) error { return nil },
	); !errors.Is(err, ErrFleetRunning) {
		t.Fatalf("concurrent Run returned %v", err)
	}

	cancel()
	if err = <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after cancellation", err)
	}
}

func TestFleetReconnectsAfterServerRestart(t *testing.T) {
	first, firstCancel, firstDone := startFleetTestServer(t, "127.0.0.1:0", func(
		_ context.Context,
		request server.Request,
	) server.Response {
		return server.Response{Data: append([]byte("first:"), request.Command...)}
	})

	address := first.Addr().String()
	defer func() {
		if first != nil {
			stopFleetTestServer(t, first, firstCancel, firstDone)
		}
	}()

	options := DefaultOptions()
	options.LoginTimeout = 25 * time.Millisecond
	options.LoginAttempts = 1
	options.ResponseTimeout = 50 * time.Millisecond
	options.KeepAliveInterval = 0
	fleet, err := NewFleet([]Target{{
		ID:       "dayz-1",
		Address:  address,
		Password: "secret",
		Options:  &options,
	}}, FleetOptions{
		MinReconnectDelay:     10 * time.Millisecond,
		MaxReconnectDelay:     30 * time.Millisecond,
		StableSessionDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	var successes atomic.Int32
	var recovered atomic.Int32
	restarted := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- fleet.Run(ctx, func(
			sessionCtx context.Context,
			_ TargetInfo,
			client *Client,
		) error {
			for {
				commandCtx, commandCancel := context.WithTimeout(sessionCtx, 100*time.Millisecond)
				response, commandErr := client.Command(commandCtx, "probe")
				commandCancel()
				if commandErr != nil {
					return commandErr
				}

				switch {
				case strings.HasPrefix(string(response), "first:"):
					if successes.Add(1) == 1 {
						close(restarted)
					}

				case strings.HasPrefix(string(response), "second:"):
					recovered.Add(1)

				default:
					return fmt.Errorf("unexpected response %q", response)
				}

				timer := time.NewTimer(5 * time.Millisecond)
				select {
				case <-timer.C:
				case <-sessionCtx.Done():
					timer.Stop()
					return sessionCtx.Err()
				}
			}
		})
	}()

	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("fleet did not connect to the initial server: %#v",
			fleetStatusForTest(fleet, "dayz-1"))
	}

	stopFleetTestServer(t, first, firstCancel, firstDone)
	first = nil
	second, secondCancel, secondDone := startFleetTestServer(t, address, func(
		_ context.Context,
		request server.Request,
	) server.Response {
		return server.Response{Data: append([]byte("second:"), request.Command...)}
	})
	defer stopFleetTestServer(t, second, secondCancel, secondDone)

	deadline := time.After(3 * time.Second)
	for recovered.Load() < 1 {
		select {
		case <-deadline:
			cancel()
			status, _ := fleet.Status("dayz-1")
			t.Fatalf("fleet did not reconnect after restart: status=%#v", status)

		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	if err = <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after cancellation", err)
	}

	status, ok := fleet.Status("dayz-1")
	if !ok || status.LastConnected == nil {
		t.Fatalf("missing successful reconnect status: %#v, exists=%t", status, ok)
	}
}

// TestFleetDetectsClientDeathWithoutHandlerReturn supervises Client.Done directly.
func TestFleetDetectsClientDeathWithoutHandlerReturn(t *testing.T) {
	first, firstCancel, firstDone := startFleetTestServer(t, "127.0.0.1:0", func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	})
	address := first.Addr().String()
	defer func() {
		if first != nil {
			stopFleetTestServer(t, first, firstCancel, firstDone)
		}
	}()

	options := DefaultOptions()
	options.KeepAliveInterval = 60 * time.Millisecond
	options.ResponseTimeout = 25 * time.Millisecond
	options.MaxKeepAliveFailures = 1
	fleet, err := NewFleet([]Target{{
		ID:       "alpha",
		Address:  address,
		Password: "secret",
		Options:  &options,
	}}, FleetOptions{
		MinReconnectDelay: 5 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	runDone := make(chan error, 1)
	go func() {
		runDone <- fleet.Run(ctx, func(sessionCtx context.Context, _ TargetInfo, _ *Client) error {
			started <- struct{}{}
			<-sessionCtx.Done()
			return sessionCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial session handler did not start")
	}
	stopFleetTestServer(t, first, firstCancel, firstDone)
	first = nil
	second, secondCancel, secondDone := startFleetTestServer(t, address, func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("recovered")}
	})
	defer stopFleetTestServer(t, second, secondCancel, secondDone)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("Fleet did not reconnect after Client failure: %#v", fleetStatusForTest(fleet, "alpha"))
	}

	response, err := fleet.Command(context.Background(), "alpha", "probe")
	if err != nil || string(response) != "recovered" {
		t.Fatalf("Fleet.Command response=%q err=%v", response, err)
	}

	cancel()
	if err = <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after cancellation", err)
	}
}

// TestFleetExpiryRetainsOutageState stops retries without losing the final diagnosis.
func TestFleetExpiryRetainsOutageState(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.LocalAddr().String()
	_ = probe.Close()

	options := DefaultOptions()
	options.LoginTimeout = 10 * time.Millisecond
	options.LoginAttempts = 1
	options.ResponseTimeout = 10 * time.Millisecond
	options.KeepAliveInterval = 0

	fleet, err := NewFleet([]Target{{
		ID:       "offline",
		Address:  address,
		Password: "secret",
		Options:  &options,
	}}, FleetOptions{
		MinReconnectDelay: 5 * time.Millisecond,
		MaxReconnectDelay: 10 * time.Millisecond,
		ExpireAfter:       80 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- fleet.Run(ctx, nil) }()

	deadline := time.After(time.Second)
	var expired TargetStatus
	for expired.State != TargetStateExpired {
		select {
		case <-time.After(5 * time.Millisecond):
			expired, _ = fleet.Status("offline")

		case <-deadline:
			cancel()
			t.Fatalf("target did not expire: %#v", fleetStatusForTest(fleet, "offline"))
		}
	}
	if expired.OfflineSince == nil || expired.NextAttempt != nil || expired.ConsecutiveFailures < 2 {
		t.Fatalf("unexpected expired status: %#v", expired)
	}

	firstOffline := *expired.OfflineSince
	firstAttempt := expired.LastAttempt
	time.Sleep(20 * time.Millisecond)
	later, _ := fleet.Status("offline")
	if later.State != TargetStateExpired ||
		later.OfflineSince == nil ||
		firstAttempt == nil ||
		!later.OfflineSince.Equal(firstOffline) ||
		!later.LastAttempt.Equal(*firstAttempt) {
		t.Fatalf("expired target retried or lost outage state: %#v", later)
	}

	cancel()
	if err = <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v after cancellation", err)
	}
}

// TestReconnectJitterIsBoundedAndDeterministic verifies jitter without wall-clock randomness.
func TestReconnectJitterIsBoundedAndDeterministic(t *testing.T) {
	if got := reconnectDelay(
		100*time.Millisecond,
		func(int64) int64 { return 0 },
	); got != 50*time.Millisecond {
		t.Fatalf("minimum jitter delay=%s", got)
	}

	if got := reconnectDelay(
		100*time.Millisecond,
		func(n int64) int64 { return n - 1 },
	); got != 100*time.Millisecond {
		t.Fatalf("maximum jitter delay=%s", got)
	}

	if got := nextBackoff(
		8*time.Minute,
		DefaultMaxReconnectDelay,
	); got != DefaultMaxReconnectDelay {
		t.Fatalf("backoff exceeded or missed cap: %s", got)
	}
}

func TestFleetSurvivesAcceleratingRestartMatrix(t *testing.T) {
	const (
		serverCount = 6
		stableIndex = serverCount - 1
	)
	targetIDs := []string{"a", "b", "c", "d", "e", "steady"}
	targetIndexes := make(map[string]int, serverCount)
	for index, id := range targetIDs {
		targetIndexes[id] = index
	}

	addresses := make([]string, serverCount)
	servers := make([]*server.Server, serverCount)
	serveCancels := make([]context.CancelFunc, serverCount)
	serveDones := make([]<-chan error, serverCount)
	generations := make([]uint64, serverCount)
	observed := make([]atomic.Uint64, serverCount)
	probeCounts := make([]atomic.Uint64, serverCount)

	startServer := func(index int) {
		t.Helper()

		id := targetIDs[index]
		generation := generations[index]
		emulator, cancel, done := startFleetTestServer(t, addresses[index], func(
			_ context.Context,
			_ server.Request,
		) server.Response {
			return server.Response{Data: []byte(fmt.Sprintf("%s:%d", id, generation))}
		})

		servers[index] = emulator
		serveCancels[index] = cancel
		serveDones[index] = done
		addresses[index] = emulator.Addr().String()
	}

	stopServer := func(index int) {
		t.Helper()
		if servers[index] == nil {
			return
		}

		stopFleetTestServer(t, servers[index], serveCancels[index], serveDones[index])
		servers[index] = nil
	}

	for index := range serverCount {
		addresses[index] = "127.0.0.1:0"
		generations[index] = 1
		startServer(index)
	}

	clientOptions := DefaultOptions()
	clientOptions.LoginTimeout = 20 * time.Millisecond
	clientOptions.LoginAttempts = 1
	clientOptions.ResponseTimeout = 30 * time.Millisecond
	clientOptions.KeepAliveInterval = 0
	targets := make([]Target, serverCount)
	for index, id := range targetIDs {
		targets[index] = Target{
			ID:       id,
			Address:  addresses[index],
			Password: "secret",
			Options:  &clientOptions,
		}
	}

	fleet, err := NewFleet(targets, FleetOptions{
		MinReconnectDelay:     time.Millisecond,
		MaxReconnectDelay:     20 * time.Millisecond,
		MaxConcurrentDials:    3,
		StableSessionDuration: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- fleet.Run(ctx, func(
			sessionCtx context.Context,
			target TargetInfo,
			client *Client,
		) error {
			index, exists := targetIndexes[target.ID]
			if !exists {
				return fmt.Errorf("unknown fleet target %q", target.ID)
			}

			for {
				commandCtx, commandCancel := context.WithTimeout(sessionCtx, 50*time.Millisecond)
				response, commandErr := client.Command(commandCtx, "probe")
				commandCancel()
				if commandErr != nil {
					return commandErr
				}

				parts := strings.SplitN(string(response), ":", 2)
				if len(parts) != 2 || parts[0] != target.ID {
					return fmt.Errorf("unexpected probe response %q", response)
				}

				generation, parseErr := strconv.ParseUint(parts[1], 10, 64)
				if parseErr != nil {
					return fmt.Errorf("parse probe generation: %w", parseErr)
				}

				observed[index].Store(generation)
				probeCounts[index].Add(1)

				timer := time.NewTimer(time.Millisecond)
				select {
				case <-timer.C:
				case <-sessionCtx.Done():
					timer.Stop()
					return sessionCtx.Err()
				}
			}
		})
	}()
	defer func() {
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Fleet.Run returned %v after cancellation", err)
		}

		for index := range serverCount {
			stopServer(index)
		}
	}()

	waitForCurrentGenerations := func() bool {
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		for {
			ready := true
			for index := range serverCount {
				if observed[index].Load() < generations[index] {
					ready = false
					break
				}
			}

			if ready {
				return true
			}

			select {
			case <-ticker.C:
			case <-deadline.C:
				return false
			case <-ctx.Done():
				return false
			}
		}
	}

	if !waitForCurrentGenerations() {
		t.Fatal("fleet did not establish the initial set of sessions")
	}
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()
	steadyProbes := probeCounts[stableIndex].Load()
	phases := []struct {
		name     string
		interval time.Duration
	}{
		{name: "spaced", interval: 25 * time.Millisecond},
		{name: "faster", interval: 8 * time.Millisecond},
		{name: "rapid", interval: 2 * time.Millisecond},
	}

	for cycle := range 2 {
		for _, phase := range phases {
			t.Logf("cycle %d: %s per-target restart wave", cycle+1, phase.name)
			for index := range stableIndex {
				stopServer(index)
				time.Sleep(4 * time.Millisecond)
				generations[index]++
				startServer(index)
				time.Sleep(phase.interval)
			}
			if !waitForCurrentGenerations() {
				t.Fatalf("fleet did not recover after %s wave in cycle %d", phase.name, cycle+1)
			}
		}

		t.Logf("cycle %d: simultaneous restart storm", cycle+1)
		for index := range stableIndex {
			stopServer(index)
		}
		time.Sleep(12 * time.Millisecond)
		for index := range stableIndex {
			generations[index]++
			startServer(index)
		}
		if !waitForCurrentGenerations() {
			t.Fatalf("fleet did not recover after restart storm in cycle %d", cycle+1)
		}
	}

	if got := probeCounts[stableIndex].Load(); got <= steadyProbes {
		t.Fatalf("steady target stopped progressing during restart waves: %d -> %d", steadyProbes, got)
	}
	for index := range stableIndex {
		if got := observed[index].Load(); got != generations[index] {
			t.Fatalf("target %q observed generation %d; want %d", targetIDs[index], got, generations[index])
		}
	}

	waitForGoroutineLimit(t, initialGoroutines+8)
	fleet.statusMu.RLock()
	clientCount, statusCount := len(fleet.clients), len(fleet.statuses)
	fleet.statusMu.RUnlock()
	if clientCount != serverCount || statusCount != serverCount {
		t.Fatalf("fleet retained %d clients and %d statuses, want %d each", clientCount, statusCount, serverCount)
	}
}

// waitForGoroutineLimit detects reconnect worker leaks while allowing runtime cleanup.
func waitForGoroutineLimit(t *testing.T, maximum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		runtime.GC()
		if count := runtime.NumGoroutine(); count <= maximum {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count grew beyond restart baseline: got %d, limit %d",
				runtime.NumGoroutine(), maximum)
		}
		<-ticker.C
	}
}

// fleetStatusForTest returns the current status or a zero value for diagnostics.
func fleetStatusForTest(fleet *Fleet, id string) TargetStatus {
	status, _ := fleet.Status(id)
	return status
}

// startFleetTestServer starts an emulator on a caller-selected UDP address.
func startFleetTestServer(
	t *testing.T,
	address string,
	handler server.HandlerFunc,
) (*server.Server, context.CancelFunc, <-chan error) {
	t.Helper()

	emulator, err := server.New(address, handler, &server.Options{
		Password: "secret", MaxConcurrentHandlers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- emulator.Serve(ctx) }()
	select {
	case <-emulator.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("test server stopped before readiness: %v", err)

	case <-time.After(time.Second):
		cancel()
		_ = emulator.Close()
		waitForTestServer(t, done)
		t.Fatal("test server did not become ready")
	}

	return emulator, cancel, done
}

// stopFleetTestServer closes an emulator and waits for its workers to exit.
func stopFleetTestServer(
	t *testing.T,
	emulator *server.Server,
	cancel context.CancelFunc,
	done <-chan error,
) {
	t.Helper()

	cancel()
	if err := emulator.Close(); err != nil {
		t.Errorf("close test server: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve test server: %v", err)
		}

	case <-time.After(time.Second):
		t.Error("test server did not stop")
	}
}
