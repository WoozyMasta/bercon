# bercon

![logo][]

`./bercon` является интерфейсом командной строки для протокола **BattlEye RCON**

<!-- rule: current lang, other langs sorted by alpha -->
> [!NOTE]  
> Этот документ доступен на языках:
> [eng 🇬🇧][], [cz 🇨🇿][], [rus 🇷🇺][], [ua 🇺🇦][]

## Описание

`bercon` предоставляет удобный способ взаимодействия с сервером, используя
протокол BattlEye RCON (Remote Console).
Этот инструмент позволяет выполнять различные команды,
управлять сервером и отслеживать ответы от сервера.

Подойдет для таких серверов как Arma2, Arma3, DayZ и т.п. использующих
протокол [BERConProtocol][], с полным перечнем игр вы можете
ознакомится на сайте [BattlEye][]

## Установка

Вы можете скачать последнюю версию программы перейдя по ссылкам
[Linux] или [Windows]

Для linux также можно воспользоваться командой 

```bash
curl -#SfLo /usr/bin/bercon \
  https://github.com/WoozyMasta/bercon/releases/latest/download/bercon
chmod +x /usr/bin/bercon
```

Или выполнить сборку самостоятельно

```bash
git clone https://github.com/WoozyMasta/bercon
cd bercon
cargo build
```

## Параметры

```txt
BattlEye RCON CLI

Использование: bercon [ОПЦИИ] --password <ПАРОЛЬ> <КОМАНДА>

Команды:
  cli     Открыть интерактивный интерфейс командной строки
  exec    Выполнить команду (например, commands или players)
  listen  Слушать ответы сервера
  help    Вывести это сообщение или помощь для указанных подкоманд

Опции:
  -i, --ip <IP>                IPv4 адрес сервера [env: BERCON_IP=] [по умолчанию: 127.0.0.1]
  -p, --port <PORT>            UDP порт сервера [env: BERCON_PORT=] [по умолчанию: 2305]
  -P, --password <PASSWORD>    Пароль [env: BERCON_PASSWORD]
  -t, --timeout <TIMEOUT>      Таймаут в секундах [env: BERCON_TIMEOUT=] [по умолчанию: 45]
  -k, --keepalive <KEEPALIVE>  Контроль подключения в секундах [env: BERCON_KEEPALIVE=] [по умолчанию: 30]
  -d, --debug                  Выводить отладочные сообщения и данные
  -h, --help                   Вывести справку
  -V, --version                Вывести версию
```

> [!NOTE]  
> Если установлен таймаут меньше, чем контроль подключения,
> то значение контроля подключения также изменится на установленное.  
> Контроль подключения не может быть установлен на более, чем 45 секунд,
> потому что это не имеет смысла,
> и все большие значения будут установлены на 45 секунд.

## Примеры использования

Можно использовать аргументы, переменные или их комбинацию

```bash
bercon -p 2306 -P myPass exec players
BERCON_PASSWORD=myPass BERCON_PORT=2306 bercon exec players
BERCON_PASSWORD=myPass bercon -p 2306 exec players
```

Значение аргумента имеет наивысший приоритет перед переменной среды

```bash
# будет использован pas$$word
BERCON_PASSWORD='strong' bercon -P 'pas$$word' exec players
```

Не забудьте использовать кавычки, так как пароли и команды могут содержать
управляющие символы командной строки.
Когда вы используете аргументы с `-`, например, `say -1`, следует
указать `--`, чтобы сообщить программе, что анализ аргументов завершен

```bash
bercon --ip 192.168.0.10 --port 2306 --password 'pas$$word' exec -- '#lock'
bercon -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- '#unlock'
bercon -t1 -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- say -1 'Hello world!'
```

## Дополнительные примеры

Вы также можете использовать переменные для сохранения параметров для
разных серверов в разных файлах

```bash
# в файле ~/.server-1.env
BERCON_IP=192.168.0.10
BERCON_PORT=2306
BERCON_PASSWORD='pas$$word'

# прочитать файл и выполнить команду
. .server-1.env && bercon exec players
```

Пример функции которая позволит выполнять команды на нескольких ваших
серверах DayZ одновременно

> [!TIP]  
> Функции можно разместить в `~/.bashrc` для быстрого доступа к ним

```bash
export DAYZ_SERRVER_COUNT=5

dayz-all-rcon() {
  for i in {1..$DAYZ_SERRVER_COUNT}; do
    echo "server-$i"
    . "~/.server-$i.env"
    bercon -t1s exec -- "$1";
  done
}

# показать игроков на всех серверах
dayz-all-rcon players
# забанить GUID на постоянно на всех серверах
dayz-all-rcon addBan "$GUID" 0 cheater
```

Данный пример позволит удобно выполнить отложенный рестарт на всех серверах
DayZ одновременно, предварительно оповестив игроков о скором перезапуске

> [!TIP]  
> Данный пример утилизирует функцию из предыдущего примера

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

# перезапустить все серверы через 120 (по умолчанию) секунд
dayz-all-restart
# перезагрузите все серверы через 360 секунд
dayz-all-restart 360
```

При помощи этого примера вы можете остановить и отключить все сервера DayZ
перед обслуживанием вашего сервера

> [!TIP]  
> Данный пример утилизирует функцию из предыдущего примера

```bash
dayz-all-shutdown() {
  dayz-all-restart "${1:-120}"
  for i in {1..$DAYZ_SERRVER_COUNT}; do
    systemctl --user stop "dayz-server@$i.service" &
    systemctl --user disable "dayz-server@$i.service"
  done
  wait
}

# выключить все серверы через 360 секунд
dayz-all-shutdown 360
```

<!-- Links -->
[eng 🇬🇧]: ../README.md
[ua 🇺🇦]: README.ua.md
[rus 🇷🇺]: README.ru.md
[cz 🇨🇿]: README.cz.md
[logo]: ../logo.png

[Linux]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon> "Linux x86 gcc binary"
[Windows]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon> "Windows exe файл"
[BattlEye]: <https://www.battleye.com/> "BattlEye – The Anti-Cheat Gold Standard"
[BERConProtocol]: <https://www.battleye.com/downloads/BERConProtocol.txt> "Спецификация протокола BattlEye RCON"
