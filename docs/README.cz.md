# bercon

![logo][]

`./bercon` je rozhraní příkazového řádku pro protokol **BattlEye RCON**.

<!-- pravidlo: aktuální jazyk, ostatní jazyky seřazeny podle alfy -->
> [!NOTE]  
> Tento dokument je k dispozici v jazycích:
> [eng 🇬🇧][], [cz 🇨🇿][], [rus 🇷🇺][], [ua 🇺🇦][].

## Popis

`bercon` poskytuje pohodlný způsob interakce se serverem pomocí příkazu
BattlEye RCON (Remote Console) protokolu.
Tento nástroj umožňuje provádět různé příkazy,
ovládat server a sledovat odpovědi serveru.

Je vhodný pro servery jako Arma2, Arma3, DayZ atd. využívající protokol
protokol [BERConProtocol][], přičemž v úplném seznamu her můžete
podívat na úplný seznam her na webové stránce [BattlEye][].

## Instalace

Nejnovější verzi programu si můžete stáhnout pomocí následujících odkazů
[Linux] nebo [Windows]

Pro Linux můžete také použít příkaz

```bash
curl -#SfLo /usr/bin/bercon \
  https://github.com/WoozyMasta/bercon/releases/latest/download/bercon
chmod +x /usr/bin/bercon
bercon -h && bercon -V
```

Nebo si sestavení proveďte sami

```bash
git clone https://github.com/WoozyMasta/bercon
cd bercon
cargo build
```

## Parametry

```txt
BattlEye RCON CLI

Příkaz: bercon [OPTIONS] --password <HESLO> <PŘÍKAZ>

Příkazy:
  cli      Otevře interaktivní rozhraní příkazového řádku
  execute  Spustí příkaz (např. příkazy nebo hráče).
  listen   Poslouchat odpovědi serveru
  help     zobrazí tuto zprávu nebo nápovědu k zadaným dílčím příkazům

Možnosti:
  -i, --ip <IP>                IPv4 adresa serveru [env: BERCON_IP=] [výchozí: 127.0.0.1]
  -p, --port <PORT>            UDP port serveru [env: BERCON_PORT=] [výchozí: 2305]
  -P, --password <PASSWORD>    Heslo [env: BERCON_HESLO]
  -t, --timeout <TIMEOUT>      Časový limit v sekundách [env: BERCON_TIMEOUT=] [výchozí: 45]
  -k, --keepalive <KEEPALIVE>  Sledování spojení v sekundách [env: BERCON_KEEPALIVE=] [výchozí: 30]
  -d, --debug                  Výstup ladicích zpráv a dat
  -h, --help                   Výpis nápovědy
  -V, --version                Výpis verze
```

> [!NOTE]  
> Pokud je časový limit nastaven na hodnotu menší než řízení spojení,
> pak se na nastavenou hodnotu změní i hodnota řízení připojení.  
> Řízení připojení nelze nastavit na více než 45 sekund,
> protože to nemá smysl,
> a všechny větší hodnoty budou nastaveny na 45 sekund.

## Příklady použití

Můžete použít argumenty, proměnné nebo kombinaci obojího

```bash
bercon -p 2306 -P myPass exec players
BERCON_PASSWORD=myPass BERCON_PORT=2306 bercon exec players
BERCON_PASSWORD=myPass bercon -p 2306 exec players
```

Hodnota argumentu má nejvyšší prioritu před proměnnou prostředí

```bash
# pas$$word bude použito
BERCON_PASSWORD='strong' bercon -P 'pas$$word' exec players
```

Nezapomeňte použít uvozovky, protože hesla a příkazy mohou obsahovat řídicí znaky příkazového řádku.
řídicí znaky příkazového řádku.
Při použití argumentů s `-`, jako je `say -1`, byste měli
zadat `--`, abyste programu sdělili, že analýza argumentů je dokončena.

```bash
bercon --ip 192.168.0.10 --port 2306 --heslo 'pas$$word' exec -- '#lock'
bercon -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- '#unlock'
bercon -t1 -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- say -1 'Hello world!'
```

## Další linux příklady

Proměnné můžete také použít k uložení parametrů pro
různé servery v různých souborech

```bash
# v souboru ~/.server-1.env
BERCON_IP=192.168.0.10
BERCON_PORT=2306
BERCON_PASSWORD='pas$$word'.

# načtěte soubor a proveďte příkaz
. .server-1.env && bercon exec players
```

Příklad funkce, která vám umožní provádět příkazy na několika vašich
DayZ serverů současně

> [!TIP]  
> Funkce lze umístit do `~/.bashrc` pro rychlý přístup k nim.

```bash
export DAYZ_SERRVER_COUNT=5

dayz-all-rcon() {
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    printf '[%s] ' "Server-$i"
    . "~/.server-$i.env".
    bercon -t1 exec -- "$1";
    echo
  done
}

# zobrazí hráče na všech serverech
dayz-all-rcon players
# trvale zakáže GUID na všech serverech
dayz-all-rcon addBan "$GUID" 0 cheater
```

Tento příklad vám umožní pohodlně provést odložený restart na všech serverech DayZ současně.
serverů DayZ ve stejnou dobu a upozorní hráče, že se blíží restart.

> [!TIP]  
> Tento příklad recykluje funkci z předchozího příkladu

```bash
dayz-all-restart() {
  dayz-all-rcon '#lock'
  dayz-all-rcon say -1 "Server locked for new connection, restart after ${1:-120} seconds"
  while (( --timer >= 0 )); do
    sleep 1s
    dayz-all-rcon say -1 "Restart server after ${1:-120} seconds"
  done
  dayz-all-rcon '#shutdown'
}

#restartovat všechny servery po 120 (výchozí) sekundách
dayz-all-restart
#restartovat všechny servery po 360 sekundách
dayz-all-restart 360
```

> [!CAUTION]  
> V době psaní tohoto článku má upravená verze serveru DayZ pro Linux
> problém se zastavováním serveru ([T179734]), možná budete chtít sledovat
> stav procesu. Jako jedno z řešení slouží například skript
> [DayZ Linux Server watchdog].

Pomocí tohoto příkladu můžete zastavit a vypnout všechny servery DayZ
před údržbou svého serveru

> [!TIP]  
> > Tento příklad využívá funkci z předchozího příkladu.

```bash
dayz-all-shutdown() {
  dayz-all-restart "${1:-120}"
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    systemctl --user stop "dayz-server@$i.service" &
    systemctl --user disable "dayz-server@$i.service"
  done
  wait
}

# vypnutí všech serverů po 360 sekundách
dayz-all-shutdown 360
```

<!-- Links -->
[eng 🇬🇧]: ../README.md
[ua 🇺🇦]: README.ua.md
[rus 🇷🇺]: README.ru.md
[cz 🇨🇿]: README.cz.md
[logo]: ../assets/logo.png

[Linux]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon> "Linux x86 gcc binary"
[Windows]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon.exe> "Windows exe soubor"
[BattlEye]: <https://www.battleye.com/> "BattlEye - zlatý standard pro boj s podvody"
[BERConProtocol]: <https://www.battleye.com/downloads/BERConProtocol.txt> "BattlEye RCON Protocol Specification"
[T179734]: https://feedback.bistudio.com/T179734 "linux modded server shutdown bug"
[DayZ Linux Server watchdog]: https://gist.github.com/WoozyMasta/3c3aaf8d1b1517e9ee47c6b2a96fee96 "DayZ Linux Server watchdog"
