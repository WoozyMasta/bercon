// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon_test

import (
	"context"
	"fmt"
	"time"

	"github.com/woozymasta/bercon"
)

func ExampleDial() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := bercon.Dial(ctx, "127.0.0.1:2305", "secret", nil)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	_, _ = client.Command(ctx, "players")
}

func ExampleFleet_Statuses() {
	fleet, err := bercon.NewFleet([]bercon.Target{
		{ID: "eu-1", Address: "127.0.0.1:2305", Password: "secret"},
		{ID: "eu-2", Address: "127.0.0.1:2405", Password: "secret"},
	}, bercon.FleetOptions{})
	if err != nil {
		panic(err)
	}

	for _, status := range fleet.Statuses() {
		fmt.Println(status.ID, status.State)
	}

	// Output:
	// eu-1 stopped
	// eu-2 stopped
}

func ExampleFleet_Messages() {
	fleet, err := bercon.NewFleet([]bercon.Target{
		{ID: "eu-1", Address: "127.0.0.1:2305", Password: "secret"},
	}, bercon.FleetOptions{
		MessageBuffer:      512,
		DropOldestMessages: true,
	})
	if err != nil {
		panic(err)
	}

	select {
	case event := <-fleet.Messages():
		fmt.Printf("%s: %s\n", event.TargetID, event.Message.Data)
	default:
		fmt.Println("waiting for server events")
	}

	// Output:
	// waiting for server events
}

func ExampleNewFleet() {
	targets := []bercon.Target{
		{ID: "eu-1", Address: "127.0.0.1:2305", Password: "secret"},
		{ID: "eu-2", Address: "127.0.0.1:2306", Password: "secret"},
	}
	fleet, err := bercon.NewFleet(targets, bercon.FleetOptions{
		MaxConcurrentDials: 2,
		ExpireAfter:        30 * time.Minute,
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fleet.Run(ctx, nil) }()

	_, _ = fleet.Command(ctx, "eu-1", "players")
	_, _ = fleet.Status("eu-1")

	cancel()
	if err := <-done; err != context.Canceled {
		fmt.Printf("Fleet.Run returned %v after cancellation\n", err)
		return
	}
}
