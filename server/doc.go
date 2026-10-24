// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package server implements a BattlEye RCon v2 UDP endpoint for applications that expose commands
to RCon clients or need a controlled peer for client tests.

[New] binds the socket. [Server.Serve] starts a bounded command queue and fixed handler pool;
applications provide command behavior with [Handler] or [HandlerFunc].
A [Response] carries the command result and optional independent server events.

Every successful login creates a session generation.
Its context and [Session.Done] end on expiry, re-login, or shutdown,
preventing retained handles from acting as the replacement session.

[Session.SendMessage] and [Server.BroadcastMessage] deliver acknowledged events.
Queue overflow is observable through [Server.DroppedMessages] and [Server.DroppedCommands].
Cancel the Serve context or call [Server.Close] to stop the endpoint.
*/
package server
