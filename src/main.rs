use std::io::stdin;
use std::net::{Ipv4Addr, SocketAddr, UdpSocket};
use std::sync::Arc;
use std::thread;
use std::thread::{sleep, JoinHandle};
use std::time::Duration;

use battleye_rust::remote_console::BERemoteConsole;
use battleye_rust::socket::udp::UdpSocketConnection;
use clap::{CommandFactory, Parser};

mod params;
use params::{Options, Command, ExecArgs};

macro_rules! dbg {
    ($( $args:expr ),*) => {
        if Options::parse().debug {
            eprint!("[DEBUG] ");
            eprintln!($( $args ),* );
        }
    }
}

fn response_processing(be_remote_console: &Arc<BERemoteConsole>, infinity: bool, debug: bool) {
    let mut established = false;
    loop {
        if !established {
            dbg!("Wait for authentication response");
        }

        let response = match be_remote_console.receive_data() {
            Ok(response) => response,
            Err(err) => {
                eprintln!("Failed to receive socket data: {}", err);
                std::process::exit(1);
            }
        };

        if response.is_empty() {
            dbg!("Received empty response");
            if !established {
                eprintln!("Failed to connect to server, no response was received");
                std::process::exit(1);
            };
            continue;
        } else {
            dbg!("{:#04X?}", response);
        }

        // https://www.battleye.com/downloads/BERConProtocol.txt
        // TODO make multiple packets response
        match response[1] {
            // Login packet
            0x00 => {
                // successfully logged in
                if response[2] == 0x01 {
                    dbg!("Authentication accepted");
                    if !established {
                        established = true
                    };
                    continue;
                } else {
                    eprintln!("Password does not match with BattlEye config file");
                    std::process::exit(1);
                }
            }
            // Command packet
            0x01 => {
                if response[2] == 0x00 && (response.len() > 3) {
                    println!(
                        "{}",
                        String::from_utf8(response[3..response.len()].to_owned()).unwrap()
                    );
                }
                if !infinity {
                    std::process::exit(0);
                }
            }
            // Server message packet
            0x02 => {
                println!(
                    "{}",
                    String::from_utf8(response[3..response.len()].to_owned()).unwrap()
                );
            }
            _ => {
                eprintln!("Unknown packet identifier");
                if !debug {
                    std::process::exit(1);
                }
            }
        }

        dbg!("Response processing completed");
    }
}

fn input_processing(be_remote_console: &Arc<BERemoteConsole>) {
    loop {
        let mut input_string = String::new();
        match stdin().read_line(&mut input_string) {
            Ok(_) => {
                // Trim and process input
                let trimmed_input = input_string.trim();
                if trimmed_input.eq_ignore_ascii_case("exit") {
                    println!("Exiting the program...");
                    std::process::exit(0);
                }

                // Send command
                be_remote_console.send_command(trimmed_input).ok();
            }
            Err(err) => {
                // Handle other errors
                eprintln!("Error reading input: {}", err);
                std::process::exit(1);
            }
        }
    }
}

fn keep_alive_processing(be_remote_console: &Arc<BERemoteConsole>, keep_alive: Duration) {
    loop {
        sleep(keep_alive.into());
        dbg!("Send Keep Alive packet");
        be_remote_console.keep_alive().ok();
    }
}

fn send_command(be_remote_console: &Arc<BERemoteConsole>, cmd_args: &ExecArgs, debug: bool) {
    let cmd = cmd_args.val.join(" ");
    if cmd.is_empty() {
        let mut app_command = Options::command();
        app_command.print_help().ok();
        eprintln!("\nExec cannot be empty, use \"commands\" for list all available commands.");
        std::process::exit(0);
    }

    be_remote_console.send_command(cmd.as_str()).ok();
    dbg!("Send command: {}", cmd);

    response_processing(&be_remote_console, false, debug);
    std::process::exit(0);
}

fn spawn_terminal(
    be_remote_console: &Arc<BERemoteConsole>,
    keep_alive: Duration,
    interactive: bool,
    debug: bool,
) {
    // Terminal
    let terminal_socket = Arc::clone(&be_remote_console);
    let terminal_handle: JoinHandle<_> =
        thread::spawn(move || response_processing(&terminal_socket, true, debug));
    dbg!("Spawn terminal thread");

    // Keep Alive
    let keep_alive_socket = Arc::clone(&be_remote_console);
    let keep_alive_handle: JoinHandle<_> =
        thread::spawn(move || keep_alive_processing(&keep_alive_socket, keep_alive));
    dbg!("Spawn keep alive thread");

    // Thread for terminal input
    if interactive {
        let input_socket = Arc::clone(&be_remote_console);
        let input_handle: JoinHandle<_> = thread::spawn(move || input_processing(&input_socket));
        dbg!("Spawn input thread");

        input_handle.join().expect("Failed to join input thread");
        dbg!("Join input thread");
    }

    // Join all threads
    terminal_handle
        .join()
        .expect("Failed to join terminal thread");
    dbg!("Join terminal thread");

    keep_alive_handle
        .join()
        .expect("Failed to join keep alive thread");
    dbg!("Join keep alive thread");
}

fn main() {
    let args = Arc::new(Options::parse());

    if args.password.is_empty() {
        println!("Password cant be blank");
        std::process::exit(1);
    }

    let mut keep_alive = args.keepalive;
    if keep_alive.as_secs() > 45 {
        dbg!("It makes no sense to set the KeepAlive timeout to more than 45 seconds, reset to 45");
        keep_alive = Duration::from_secs(45);
    }
    if args.timeout.as_secs() < keep_alive.as_secs() {
        keep_alive = args.timeout.into();
        dbg!("Change keepalive equal to timeout {:?}", keep_alive);
    }

    // Create a SocketAddr from the IP address and port
    let server_address = SocketAddr::new(args.ip.into(), args.port);
    dbg!("Connect to: {}", server_address);

    // Bind the UDP socket
    let udp_socket = match UdpSocket::bind((Ipv4Addr::UNSPECIFIED, 0)) {
        Ok(socket) => socket,
        Err(err) => {
            eprintln!("Failed to bind UDP socket: {}", err);
            std::process::exit(1);
        }
    };

    // Set timeout
    if let Err(err) = udp_socket.set_read_timeout(Some(args.timeout.into())) {
        eprintln!("Failed to set initial read timeout: {}", err);
        std::process::exit(1);
    }

    // Connect to the server
    if let Err(err) = udp_socket.connect(server_address) {
        eprintln!("Failed to connect to server: {}", err);
        std::process::exit(1);
    } else {
        dbg!("Open connect to BattleEye server");
    }

    // Init BERemoteConsole
    let be_remote_console = Arc::new(BERemoteConsole::new(UdpSocketConnection::new(udp_socket)));
    be_remote_console.authenticate(args.password.clone()).ok();

    // Exec some args
    match &args.command {
        Command::Exec(cmd_args) => send_command(&be_remote_console, cmd_args, args.debug),
        Command::Listen => spawn_terminal(&be_remote_console, keep_alive, false, args.debug),
        Command::Cli => spawn_terminal(&be_remote_console, keep_alive, true, args.debug),
    }
}
