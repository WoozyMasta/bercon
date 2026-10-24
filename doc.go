// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

/*
Package bercon implements BattlEye RCon v2 transport and supervises connections to multiple servers.
It handles UDP framing, authentication, correlated multipart responses, and acknowledged server events.

Use [Dial] for one endpoint. Its context bounds socket setup and authentication;
the resulting [Client] lives until [Client.Close] or a terminal transport failure.
Commands are sent once. If cancellation happens after transmission,
[ErrCommandOutcomeUnknown] means the server may still have executed the command.
Command responses and unsolicited server events use separate APIs:
[Client.Command] returns the response, while [Client.Messages] streams events.

For collectors, [NewFleet] supervises one independent Client and UDP socket per [Target].
[Fleet.Run] limits simultaneous logins and retries failed sessions with bounded jittered backoff.
[Fleet.Command] uses the current session,
while [Fleet.Status] and [Fleet.Statuses] expose connection and outage state.

[Fleet.Messages] combines events from all targets and identifies their source.
Use one reader and fan events out in the application.

Client and Fleet event queues are bounded so slow consumers cannot block protocol processing.
They drop new events by default.
Set [Options.DropOldestMessages] or [FleetOptions.DropOldestMessages] to keep newer events instead.
Read [Client.DroppedMessages] or [Fleet.DroppedMessages] to observe queue loss.
*/
package bercon
