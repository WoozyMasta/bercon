// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package rv parses the Real Virtuality response format observed in Arma 3 and DayZ.
Arma 2 uses the same engine family,
but its response compatibility has not been independently verified.

[ParsePlayers] returns address, ping, and GUID rows.
[ParseAdmins] parses connected administrator rows,
and [ParseBans] keeps GUID and IPv4 sections separate.
Malformed rows with useful data are retained;
callers can inspect validity fields and use [parser.ErrMalformedRows] to detect partial results.
An unrecognized payload returns [parser.ErrUnrecognized].
Empty player, admin, and ban lists are valid responses and return nil error.
*/
package rv
