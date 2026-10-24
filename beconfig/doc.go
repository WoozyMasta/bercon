// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package beconfig reads BattlEye server configuration files without modifying them.

It extracts RConPassword, RConPort, RConIP, and RestrictRCon;
unknown directives are ignored. RConIP retains wildcard addresses as configured.
Use [Config.LocalAddrPort] only for a client running on the same machine,
where wildcard addresses are mapped to the matching loopback address.

[Find] and [Resolve] search only the directory supplied by the caller.
They recognize canonical BEServer filenames and runtime copies containing an `_active_<suffix>` segment.
Active copies take precedence; modification time orders files within each group.
This discovery rule is a filesystem heuristic, not part of the RCon protocol.
Use [Resolve] to accept either a file or directory, then pass its result to [Load].
*/
package beconfig
