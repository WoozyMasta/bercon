# bercon

![logo][]

`./bercon` is the command line interface for the **BattlEye RCON** protocol

<!-- rule: current lang, other langs sorted by alpha -->
> [!NOTE]  
> This document is available in the languages:
> [eng 🇬🇧][], [cz 🇨🇿][], [rus 🇷🇺][], [ua 🇺🇦][]

## Description

`bercon` provides a convenient way to interact with the server using the
BattlEye RCON (Remote Console) protocol.
This tool allows you to execute various commands,
control the server, and track responses from the server.

It is suitable for such servers as Arma2, Arma3, DayZ, etc. using the
protocol [BERConProtocol][], with a full list of games you can
check out the full list of games on the [BattlEye][] website

## Installation

You can download the latest version of the programme by following the links
[Linux] or [Windows]

For Linux you can also use the command

```bash
curl -#SfLo /usr/bin/bercon \
  https://github.com/WoozyMasta/bercon/releases/latest/download/bercon
chmod +x /usr/bin/bercon
bercon -h && bercon -V
```

Or do the build yourself

```bash
git clone https://github.com/WoozyMasta/bercon
cd bercon
cargo build
```

## Parameters

```txt
BattlEye RCON CLI

Usage: bercon [OPTIONS] --password <PASSWORD> <COMMAND>

Commands:
  cli     Open interactive CLI
  exec    Execute a command (e.g. commands or players)
  listen  Listen for server responses
  help    Print this message or the help of the given subcommand(s)

Options:
  -i, --ip <IP>                Server IPv4 address [env: BERCON_IP=] [default: 127.0.0.1]
  -p, --port <PORT>            Server UDP port [env: BERCON_PORT=] [default: 2305]
  -P, --password <PASSWORD>    Password [env: BERCON_PASSWORD]
  -t, --timeout <TIMEOUT>      Timeout in seconds [env: BERCON_TIMEOUT=] [default: 45]
  -k, --keepalive <KEEPALIVE>  Keepalive in seconds [env: BERCON_KEEPALIVE=] [default: 30]
  -d, --debug                  Print debug messages and data
  -h, --help                   Print help
  -V, --version                Print version
```

> [!NOTE]  
> If the timeout is set to less than the connection control,
> then the connection control value will also change to the set value.  
> Connection control cannot be set for more than 45 seconds,
> because it makes no sense, and all larger values will be set to 45 seconds.

## Usage Examples

You can use arguments, variables, or a combination of both

```bash
bercon -p 2306 -P myPass exec players
BERCON_PASSWORD=myPass BERCON_PORT=2306 bercon exec players
BERCON_PASSWORD=myPass bercon -p 2306 exec players
```

The argument value has the highest priority over the environment variable

```bash
# pas$$word will be used
BERCON_PASSWORD='strong' bercon -P 'pas$$word' exec players
```

Don't forget to use inverted commas, as passwords and commands may contain
command line control characters.
When you use arguments with `-`,
such as `say -1`, you should specify `--` to tell the program,
that the argument analysis is complete

```bash
bercon --ip 192.168.0.10 --port 2306 --password 'pas$$word' exec -- '#lock'
bercon -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- '#unlock'
bercon -t1 -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- say -1 'Hello world!'
```

## Additional linux examples

You can also use variables to store parameters for
different servers in different files

```bash
# in the ~/.server-1.env file
BERCON_IP=192.168.0.10
BERCON_PORT=2306
BERCON_PASSWORD='pas$$word'.

# read the file and execute the command
. .server-1.env && bercon exec players
```

An example function that will allow you to execute commands on several of your
DayZ servers at the same time

> [!TIP]  
> Functions can be placed in `~/.bashrc` for quick access to them

```bash
export DAYZ_SERVERS_COUNT=5

dayz-all-rcon() {
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    printf '[%s] ' "Server-$i"
    . "~/.server-$i.env".
    bercon -t1 exec -- "$1";
    echo
  done
}

# show players on all servers
dayz-all-rcon players
# ban GUIDs permanently on all servers
dayz-all-rcon addBan "$GUID" 0 cheater
```

This example will allow you to conveniently perform a delayed restart on all
DayZ servers at the same time, notifying players that a restart is imminent

> [!TIP]  
> This example recycles the function from the previous example

```bash
dayz-all-restart() {
  local timer="${1:-120}" step="${2:-10}"
  dayz-all-rcon '#lock'
  dayz-all-rcon say -1 "Server locked for new connection, restart after $timer seconds"
  for i in $(seq "$timer" "-$step" 0); do
    sleep "$step"
    dayz-all-rcon say -1 "Restart server after $timer seconds"
  done
  dayz-all-rcon '#shutdown'
}

# restart all servers after 120 (default) seconds
dayz-all-restart
# restart all servers after 360 seconds
dayz-all-restart 360
```

> [!CAUTION]  
> At the time of writing, the modified version of the DayZ server for Linux
> has a problem with server stopping ([T179734]), you may need to additionally
> monitor the state of the process. As one solution for example
> script [DayZ Linux Server watchdog]

With this example, you can stop and shut down all DayZ servers
before maintaining your server

> [!TIP]  
> This example utilises the function from the previous example

```bash
dayz-all-shutdown() {
  dayz-all-restart "${1:-120}" "${2:-10}"
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    systemctl --user stop "dayz-server@$i.service" &
    systemctl --user disable "dayz-server@$i.service"
  done
  wait
}

# shutdown all servers after 360 seconds
dayz-all-shutdown 360
```

<!-- Links -->
[eng 🇬🇧]: README.md
[ua 🇺🇦]: docs/README.ua.md
[rus 🇷🇺]: docs/README.ru.md
[cz 🇨🇿]: docs/README.cz.md
[logo]: assets/logo.png

[Linux]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon> "Linux x86 gcc binary"
[Windows]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon.exe> "Windows exe file"
[BattlEye]: <https://www.battleye.com/> "BattlEye – The Anti-Cheat Gold Standard"
[BERConProtocol]: <https://www.battleye.com/downloads/BERConProtocol.txt> "BattlEye RCON Protocol Specification"
[T179734]: https://feedback.bistudio.com/T179734 "linux modded server shutdown bug"
[DayZ Linux Server watchdog]: https://gist.github.com/WoozyMasta/3c3aaf8d1b1517e9ee47c6b2a96fee96 "DayZ Linux Server watchdog"
