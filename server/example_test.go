// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package server_test

import (
	"context"
	"fmt"

	"github.com/woozymasta/bercon/server"
)

func ExampleNew() {
	srv, err := server.New("127.0.0.1:0", server.HandlerFunc(
		func(_ context.Context, request server.Request) server.Response {
			return server.Response{Data: append([]byte("ok: "), request.Command...)}
		},
	), &server.Options{Password: "secret"})
	if err != nil {
		panic(err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	<-srv.Ready()
	fmt.Println(srv.Addr().Network())
	cancel()
	if err := <-done; err != nil {
		panic(err)
	}

	// Output: udp
}
