use std::net::Ipv4Addr;
use std::time::Duration;

use clap::Parser;

#[derive(Parser, Debug)]
#[clap(version, about, long_about = None)]
pub struct Options {
    #[clap(
        short,
        long,
        env = "BERCON_IP",
        default_value = "127.0.0.1",
        help = "Server IPv4 address"
    )]
    pub ip: Ipv4Addr,

    #[clap(
        short,
        long,
        env = "BERCON_PORT",
        default_value = "2305",
        help = "Server UDP port"
    )]
    pub port: u16,

    #[clap(
        short = 'P',
        long,
        env = "BERCON_PASSWORD",
        hide_env_values = true,
        help = "Password"
    )]
    pub password: String,

    #[clap(
        short,
        long,
        env = "BERCON_TIMEOUT",
        default_value = "45",
        value_parser = parse_duration,
        help = "Timeout in seconds"
    )]
    pub timeout: Duration,

    #[clap(
        short,
        long,
        env = "BERCON_KEEPALIVE",
        default_value = "30",
        value_parser = parse_duration,
        help = "Keepalive in seconds"
    )]
    pub keepalive: Duration,

    #[clap(
        short,
        long,
        default_value = "false",
        help = "Print debug messages and data"
    )]
    pub debug: bool,

    #[clap(subcommand)]
    pub command: Command,
}

#[derive(Parser, Debug)]
pub struct ExecArgs {
    pub val: Vec<String>,
}

#[derive(Parser, Debug)]
pub enum Command {
    #[clap(name = "cli", about = "Open interactive CLI")]
    Cli,

    #[clap(name = "exec", about = "Execute a command (e.g. commands or players)")]
    Exec(ExecArgs),

    #[clap(name = "listen", about = "Listen for server responses")]
    Listen,
}

fn parse_duration(arg: &str) -> Result<Duration, std::num::ParseIntError> {
    let seconds = arg.parse()?;
    Ok(Duration::from_secs(seconds))
}
