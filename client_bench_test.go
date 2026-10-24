// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/woozymasta/bercon/server"
)

// BenchmarkClientServer measures serial and concurrent loopback command flows.
func BenchmarkClientServer(b *testing.B) {
	for _, workers := range []int{1, 8, 64} {
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			benchmarkClientCommands(b, workers, 32)
		})
	}

	b.Run("multipart", func(b *testing.B) {
		benchmarkClientCommands(b, 1, 2000)
	})

	b.Run("server-message", benchmarkServerMessages)
}

// benchmarkClientCommands runs a bounded local server for the benchmark lifetime.
func benchmarkClientCommands(b *testing.B, workers, responseBytes int) {
	b.Helper()
	response := []byte(strings.Repeat("x", responseBytes))
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: response}
	}), &server.Options{Password: "benchmark", MaxConcurrentHandlers: workers})
	if err != nil {
		b.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	defer func() {
		cancel()
		_ = s.Close()
		waitForTestServer(b, serveDone)
	}()

	client, err := Dial(context.Background(), s.Addr().String(), "benchmark", nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	defer b.StopTimer()

	if workers == 1 {
		for range b.N {
			if _, err := client.Command(context.Background(), "status"); err != nil {
				b.Fatal(err)
			}
		}
		return
	}

	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := worker; iteration < b.N; iteration += workers {
				if _, err := client.Command(context.Background(), "status"); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		b.Error(err)
	}
}

// benchmarkServerMessages measures message delivery and the corresponding ACK.
func benchmarkServerMessages(b *testing.B) {
	b.Helper()
	s, err := server.New("127.0.0.1:0", server.HandlerFunc(func(
		_ context.Context,
		r server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok"), PostMessages: [][]byte{r.Command}}
	}), &server.Options{Password: "benchmark"})
	if err != nil {
		b.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx) }()
	defer func() {
		cancel()
		_ = s.Close()
		waitForTestServer(b, serveDone)
	}()

	opts := DefaultOptions()
	opts.MessageBuffer = 8
	client, err := Dial(context.Background(), s.Addr().String(), "benchmark", &opts)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	b.ReportAllocs()
	b.ResetTimer()
	defer b.StopTimer()

	for range b.N {
		if _, err := client.Command(context.Background(), "event"); err != nil {
			b.Fatal(err)
		}
		select {
		case <-client.Messages():
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		}
	}
}

// BenchmarkFleetRestartSoak cycles a 19-server fleet while one target stays offline.
func BenchmarkFleetRestartSoak(b *testing.B) {
	b.StopTimer()
	current, stopCurrent := startFleetBenchServer(b, "127.0.0.1:0")
	serverStopped := false
	defer func() {
		if !serverStopped {
			stopCurrent()
		}
	}()
	address := current.Addr().String()
	offline, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	offlineAddress := offline.LocalAddr().String()
	_ = offline.Close()

	clientOptions := DefaultOptions()
	clientOptions.LoginTimeout = 50 * time.Millisecond
	clientOptions.LoginAttempts = 1
	clientOptions.ResponseTimeout = 30 * time.Millisecond
	clientOptions.KeepAliveInterval = 35 * time.Millisecond
	clientOptions.MaxKeepAliveFailures = 1
	targets := make([]Target, 20)

	for index := range 19 {
		targets[index] = Target{
			ID:       fmt.Sprintf("online-%02d", index),
			Address:  address,
			Password: "benchmark",
			Options:  &clientOptions,
		}
	}

	targets[19] = Target{
		ID:       "offline",
		Address:  offlineAddress,
		Password: "benchmark",
		Options:  &clientOptions,
	}
	fleet, err := NewFleet(targets, FleetOptions{
		MinReconnectDelay:  5 * time.Millisecond,
		MaxReconnectDelay:  20 * time.Millisecond,
		MaxConcurrentDials: 4,
	})
	if err != nil {
		stopCurrent()
		b.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- fleet.Run(ctx, nil) }()
	runStopped := false
	defer func() {
		if !runStopped {
			cancel()
			stopCurrent()
			if err := <-runDone; !errors.Is(err, context.Canceled) {
				b.Errorf("Fleet.Run returned %v after cancellation", err)
			}

			runStopped = true
			serverStopped = true
		}
	}()
	if err := waitFleetBenchOnline(fleet, 19, time.Now().Add(3*time.Second)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		b.StopTimer()
		stopCurrent()
		restartedAt := time.Now()
		_, stopCurrent = startFleetBenchServer(b, address)
		b.StartTimer()
		if err = waitFleetBenchReconnected(fleet, 19, restartedAt, time.Now().Add(3*time.Second)); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()
	cancel()
	if err = <-runDone; !errors.Is(err, context.Canceled) {
		b.Errorf("Fleet.Run returned %v after cancellation", err)
	}
	stopCurrent()
	runStopped = true
	serverStopped = true
}

// startFleetBenchServer starts an emulator and waits for its reader readiness.
func startFleetBenchServer(b *testing.B, address string) (*server.Server, func()) {
	b.Helper()
	srv, err := server.New(address, server.HandlerFunc(func(
		_ context.Context,
		_ server.Request,
	) server.Response {
		return server.Response{Data: []byte("ok")}
	}), &server.Options{
		Password:              "benchmark",
		MaxConcurrentHandlers: 32,
	})
	if err != nil {
		b.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	select {
	case <-srv.Ready():
	case <-time.After(time.Second):
		cancel()
		_ = srv.Close()
		waitForTestServer(b, done)
		b.Fatal("benchmark server did not become ready")
	}

	return srv, func() {
		cancel()
		_ = srv.Close()
		waitForTestServer(b, done)
	}
}

// waitFleetBenchOnline waits for all configured available targets to authenticate.
func waitFleetBenchOnline(fleet *Fleet, count int, deadline time.Time) error {
	return waitFleetBenchReconnected(fleet, count, time.Time{}, deadline)
}

// waitFleetBenchReconnected waits for each available target's fresh login timestamp.
func waitFleetBenchReconnected(fleet *Fleet, count int, after, deadline time.Time) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		connected := 0
		for index := range 19 {
			status, exists := fleet.Status(fmt.Sprintf("online-%02d", index))
			if exists &&
				status.State == TargetStateOnline &&
				status.LastConnected != nil &&
				(status.LastConnected.After(after) || after.IsZero()) {
				connected++
			}
		}
		if connected == count {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("only %d of %d targets connected", connected, count)
		}
		<-ticker.C
	}
}
