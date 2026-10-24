// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"errors"
	"fmt"
)

var (
	// ErrClosed means the client has been closed.
	ErrClosed = errors.New("RCon client is closed")

	// ErrCommandOutcomeUnknown means a command may have reached the server,
	// but the client cannot confirm whether it was executed.
	ErrCommandOutcomeUnknown = errors.New("RCon command outcome is unknown")

	// ErrLoginFailed means the server rejected the supplied credentials.
	ErrLoginFailed = errors.New("RCon login failed")

	// ErrLoginTimeout means all configured login attempts timed out.
	ErrLoginTimeout = errors.New("RCon login timed out")

	// ErrResponseTimeout means the command may have executed without a response.
	ErrResponseTimeout = fmt.Errorf("RCon response timed out: %w", ErrCommandOutcomeUnknown)

	// ErrCommandTooLong means the command exceeds the compatibility datagram limit.
	ErrCommandTooLong = errors.New("RCon command exceeds datagram limit")

	// ErrResponseTooLarge means the response exceeds MaxResponseBytes.
	ErrResponseTooLarge = errors.New("RCon response exceeds configured limit")

	// ErrProtocol means the server returned a conflicting or invalid response.
	ErrProtocol = errors.New("invalid RCon protocol response")

	// ErrFleetRunning means Run is already active for this fleet.
	ErrFleetRunning = errors.New("RCon fleet is already running")

	// ErrTargetUnavailable means no authenticated client is available for a target.
	ErrTargetUnavailable = errors.New("RCon fleet target is unavailable")
)
