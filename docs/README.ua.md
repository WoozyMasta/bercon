# bercon

![logo][]

`./bercon` є інтерфейсом командного рядка для протоколу **BattlEye RCON**

<!-- rule: current lang, other langs sorted by alpha -->
> [!NOTE]  
> Цей документ доступний мовами:
> [eng 🇬🇧][], [cz 🇨🇿][], [rus 🇷🇺][], [ua 🇺🇦][]

## Опис

`bercon` надає зручний спосіб взаємодії із сервером, використовуючи
протокол BattlEye RCON (Remote Console).
Цей інструмент дозволяє виконувати різні команди,
керувати сервером і відстежувати відповіді від сервера.

Підійде для таких серверів як Arma2, Arma3, DayZ тощо, які використовують
протокол [BERConProtocol][], з повним переліком ігор ви можете
ознайомитись на сайті [BattlEye][]

## Встановлення

Ви можете завантажити останню версію програми перейшовши за посиланнями
[Linux] або [Windows]

Для linux також можна скористатися командою 

```bash
curl -#SfLo /usr/bin/bercon \
  https://github.com/WoozyMasta/bercon/releases/latest/download/bercon
chmod +x /usr/bin/bercon
bercon -h && bercon -V
```

Або виконати збірку самостійно

```bash
git clone https://github.com/WoozyMasta/bercon
cd bercon
cargo build
```

## Параметри

```txt
BattlEye RCON CLI

Використання: bercon [ОПЦІЇ] --password <ПАРОЛЬ> <КОМАНДА>

Команди:
  cli     Відкрити інтерактивний інтерфейс командного рядка
  exec    Виконати команду (наприклад, commands або players)
  listen  Слухати відповіді сервера
  help    Вивести це повідомлення або допомогу для зазначених підкоманд

Опції:
  -i, --ip <IP>                IPv4 адреса сервера [env: BERCON_IP=] [за замовчуванням: 127.0.0.1]
  -p, --port <PORT>            UDP порт сервера [env: BERCON_PORT=] [за замовчуванням: 2305].
  -P, --password <PASSWORD>    Пароль [env: BERCON_PASSWORD]
  -t, --timeout <TIMEOUT>      Таймаут у секундах [env: BERCON_TIMEOUT=] [за замовчуванням: 45]
  -k, --keepalive <KEEPALIVE>  Контроль підключення в секундах [env: BERCON_KEEPALIVE=] [за замовчуванням: 30]
  -d, --debug                  Виводити налагоджувальні повідомлення і дані
  -h, --help                   Вивести довідку
  -V, --version                Вивести версію
```

> [!NOTE]  
> Якщо встановлено таймаут менший, ніж контроль підключення,
> то значення контролю підключення також зміниться на встановлене.  
> Контроль підключення не може бути встановлений на більше, ніж 45 секунд, тому
> що це не має сенсу, і всі більші значення будуть встановлені на 45 секунд.

## Приклади використання

Можна використовувати аргументи, змінні або їхню комбінацію

```bash
bercon -p 2306 -P myPass exec players
BERCON_PASSWORD=myPass BERCON_PORT=2306 bercon exec players
BERCON_PASSWORD=myPass bercon -p 2306 exec players
```

Значення аргументу має найвищий пріоритет перед змінною середовища

```bash
# буде використано pas$$word
BERCON_PASSWORD='strong' bercon -P 'pas$$word' exec players
```

Не забудьте використовувати лапки, оскільки паролі та команди можуть містити
керуючі символи командного рядка.
Коли ви використовуєте аргументи з `-`, наприклад, `say -1`, слід
вказати `--`, щоб повідомити програмі, що аналіз аргументів завершено

```bash
bercon --ip 192.168.0.10 --port 2306 --password 'pas$$word' exec -- '#lock'
bercon -i 192.168.0.10 -p 2306 -P 'pas$$word' exec -- '#unlock'
bercon -t1 -i 192.168.0.10 -p 2306 -P 'pas$$$word' exec -- say -1 'Hello world!'
```

## Додаткові linux приклади

Ви також можете використовувати змінні для збереження параметрів для
різних серверів у різних файлах

```bash
# у файлі ~/.server-1.env
BERCON_IP=192.168.0.10
BERCON_PORT=2306
BERCON_PASSWORD='pas$$word'

# прочитати файл і виконати команду
. .server-1.env && bercon exec players
```

Приклад функції, яка дозволить виконувати команди на декількох ваших
серверах DayZ одночасно

> [!TIP]  
> Функції можна розмістити в `~/.bashrc` для швидкого доступу до них

```bash
export DAYZ_SERRVER_COUNT=5

dayz-all-rcon() {
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    printf '[%s] ' "Server-$i"
    . "~/.server-$i.env"
    bercon -t1 exec -- "$1";
    echo
  done
}

# показати гравців на всіх серверах
dayz-all-rcon players
# забанити GUID на постійно на всіх серверах
dayz-all-rcon addBan "$GUID" 0 cheater
```

Цей приклад дозволить зручно виконати відкладений рестарт на всіх серверах DayZ одночасно.
DayZ одночасно, попередньо сповістивши гравців про швидкий перезапуск

> [!TIP]  
> Цей приклад утилізує функцію з попереднього прикладу

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

# перезапустити всі сервери через 120 (за замовчуванням) секунд
dayz-all-restart
# перезавантажте всі сервери через 360 секунд
dayz-all-restart 360
```

> [!CAUTION]  
> На момент написання модифікована версія DayZ сервера для Linux має
> проблему із зупинкою сервера ([T179734]), можливо вам знадобиться
> додатково стежити за станом процесу. Як одне з рішень, наприклад
> скрипт [DayZ Linux Server watchdog]

За допомогою цього прикладу ви можете зупинити і відключити всі сервери DayZ
перед обслуговуванням вашого сервера

> [!TIP]  
> Цей приклад утилізує функцію з попереднього прикладу

```bash
dayz-all-shutdown() {
  dayz-all-restart "${1:-120}"
  for i in $(seq 1 "$DAYZ_SERVERS_COUNT"); do
    systemctl --user stop "dayz-server@$i.service" &
    systemctl --user disable "dayz-server@$i.service"
  done
  wait
}

# вимкнути всі сервери через 360 секунд
dayz-all-shutdown 360
```

<!-- Посилання -->
[eng 🇬🇧]: ../README.md
[ua 🇺🇦]: README.ua.md
[rus 🇷🇺]: README.ru.md
[cz 🇨🇿]: README.cz.md
[logo]: ../assets/logo.png

[Linux]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon> "Linux x86 gcc бінарник"
[Windows]: <https://github.com/WoozyMasta/bercon/releases/latest/download/bercon.exe> "Windows exe файл"
[BattlEye]: <https://www.battleye.com/> "BattlEye - The Anti-Cheat Gold Standard"
[BERConProtocol]: <https://www.battleye.com/downloads/BERConProtocol.txt> "Специфікація протоколу BattlEye RCON"
[T179734]: https://feedback.bistudio.com/T179734 "linux modded server shutdown bug"
[DayZ Linux Server watchdog]: https://gist.github.com/WoozyMasta/3c3aaf8d1b1517e9ee47c6b2a96fee96 "DayZ Linux Server watchdog"
