# bercon

[bercon][] is a Go client and server for the BattlEye RCon protocol.
It handles UDP framing, authentication, response assembly,
and reconnects using only the Go standard library.
See the [protocol specification][].

Use it for a single administrative client, a service polling many game servers,
or a controlled RCon endpoint for integration tests.
Each managed target has an independent authenticated client and UDP socket.

* **Client and fleet:** [bercon][]
  sends RCon commands, receives server messages,
  and supervises connections to multiple servers.
* **Server:** [bercon/server][]
  accepts RCon commands and sends server messages
  through a reusable UDP endpoint.
* **Response parsers:** [bercon/parser][]
  provides shared parser errors and groups parsers by response format.
  * [bercon/parser/rv][]
    parses player, admin, and ban responses from Arma 3 and DayZ.
  * [bercon/parser/ef][]
    parses player and ban responses from Arma Reforger.
* **BattlEye config:** [bercon/beconfig][]
  reads server settings and locates BattlEye configuration files.

## Install

```sh
go get github.com/woozymasta/bercon
```

## Minimal client

Pass the RCon address as `host:port` and provide the password.
This example runs `players` and prints the response:

```go
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()

client, err := bercon.Dial(
    ctx, "127.0.0.1:2305", os.Getenv("BERCON_PASSWORD"), nil,
)
if err != nil {
    return err
}
defer func() { _ = client.Close() }()

response, err := client.Command(ctx, "players")
if err != nil {
    return err
}
fmt.Println(string(response))
```

## Important behavior

* Fleet retries unavailable targets with jittered backoff.
  Retries continue until `Fleet.Run`'s context is canceled,
  unless `ExpireAfter` is set.
* Commands are sent once.
  If cancellation or timeout occurs after transmission,
  `ErrCommandOutcomeUnknown` means the server may still execute the command.

<!-- links -->
[bercon]: https://pkg.go.dev/github.com/woozymasta/bercon
[bercon/server]: https://pkg.go.dev/github.com/woozymasta/bercon/server
[bercon/beconfig]: https://pkg.go.dev/github.com/woozymasta/bercon/beconfig
[bercon/parser]: https://pkg.go.dev/github.com/woozymasta/bercon/parser
[bercon/parser/ef]: https://pkg.go.dev/github.com/woozymasta/bercon/parser/ef
[bercon/parser/rv]: https://pkg.go.dev/github.com/woozymasta/bercon/parser/rv
[protocol specification]: ./spec/bercon-protocol.md
