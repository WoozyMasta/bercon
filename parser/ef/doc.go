// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package ef parses the Enfusion response format observed in Arma Reforger.
It recognizes semicolon-separated player identity rows and paged identity/name ban lists.
The package name identifies this observed response family;
it does not guarantee that every Enfusion game uses the same layout.

[ParsePlayers] and [ParseBans] return format-specific structures for their response layouts.
Identifiable malformed rows are retained and reported with [parser.ErrMalformedRows].
Unrecognized payloads return [parser.ErrUnrecognized].
Empty player lists and the explicit no-bans response are valid and return nil error.
*/
package ef
