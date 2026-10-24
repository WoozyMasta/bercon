// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package parser defines errors shared by the game-specific response parsers used with BattlEye RCon.

The response layouts differ, so each observed format has its own subpackage:
`parser/rv` parses the format seen in Arma 3 and DayZ, and `parser/ef` parses the format seen in Arma Reforger.
These names identify layouts; they do not promise compatibility with every game using the corresponding engine.

Call a parser from the matching subpackage. [ErrUnrecognized] means the payload does not match its schema.
A recognized response returns nil error when valid, including for an empty list;
[ErrMalformedRows] means the parser kept identifiable rows but found malformed fields.
Check these conditions with `errors.Is`.

RCon server events have no command correlation ID.
A payload matching a parser's schema is therefore not proof that it was caused by a particular command.
*/
package parser
