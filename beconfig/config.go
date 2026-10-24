// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Config contains the BattlEye directives used to connect through RCon.
type Config struct {
	// RConIP is the configured bind address; an invalid address means the directive was absent.
	RConIP netip.Addr `json:"rcon_ip,omitempty" yaml:"rcon_ip,omitempty"`

	// RestrictRCon is nil when absent and otherwise contains the explicit directive value.
	RestrictRCon *bool `json:"restrict_rcon,omitempty" yaml:"restrict_rcon,omitempty"`

	// RConPassword is the credential used by the RCon client.
	RConPassword string `json:"-" yaml:"-"`

	// RConPort is the configured UDP port.
	RConPort uint16 `json:"rcon_port" yaml:"rcon_port"`
}

// Parse reads the supported RCon directives from a BattlEye server config.
//
// Directive names are case-insensitive, duplicate valid values use the last occurrence,
// and malformed known directives fail immediately.
// Unknown directives are ignored. The password is never included in returned errors.
func Parse(r io.Reader) (Config, error) {
	if r == nil {
		return Config{}, errors.New("read BattlEye config: nil reader")
	}

	var cfg Config
	var passwordSet bool
	var portSet bool
	scanner := bufio.NewScanner(r)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}

		// Keep trailing value bytes intact for passwords,
		// but ignore indentation before directives and comments.
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
		if line == "" || strings.HasPrefix(line, ";") ||
			strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		key, value := splitDirective(line)
		switch strings.ToLower(key) {
		case "rconpassword":
			if strings.TrimSpace(value) == "" {
				return Config{}, directiveError(lineNumber, "RConPassword", "empty value")
			}

			cfg.RConPassword = value
			passwordSet = true

		case "rconport":
			port, err := parsePort(value)
			if err != nil {
				return Config{}, directiveError(lineNumber, "RConPort", "expected a decimal port from 1 to 65535")
			}

			cfg.RConPort = port
			portSet = true

		case "rconip":
			ip, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil {
				return Config{}, directiveError(lineNumber, "RConIP", "invalid IP address")
			}

			cfg.RConIP = ip

		case "restrictrcon":
			var restricted bool

			switch strings.TrimSpace(value) {
			case "0":
				restricted = false

			case "1":
				restricted = true

			default:
				return Config{}, directiveError(lineNumber, "RestrictRCon", "expected 0 or 1")
			}

			cfg.RestrictRCon = &restricted
		}
	}

	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read BattlEye config: %w", err)
	}
	if !passwordSet {
		return Config{}, ErrRConPasswordMissing
	}
	if !portSet {
		return Config{}, ErrRConPortMissing
	}

	return cfg, nil
}

// Load reads and parses one explicit regular config file.
// It does not search for a config; use [Resolve] first when the input may be a directory.
func Load(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("stat BattlEye config %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("BattlEye config %q is not a regular file", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open BattlEye config %q: %w", path, err)
	}

	cfg, parseErr := Parse(file)
	closeErr := file.Close()
	if parseErr != nil {
		return Config{}, fmt.Errorf("parse BattlEye config: %w", parseErr)
	}
	if closeErr != nil {
		return Config{}, fmt.Errorf("close BattlEye config %q: %w", path, closeErr)
	}

	return cfg, nil
}

// LocalAddrPort returns the endpoint to use when the RCon client runs on the same machine.
// It maps an absent or unspecified bind address
// to loopback without changing the address stored in Config.
func (c Config) LocalAddrPort() netip.AddrPort {
	ip := c.RConIP
	switch {
	case !ip.IsValid():
		ip = netip.MustParseAddr("127.0.0.1")

	case ip.IsUnspecified() && ip.Is6():
		ip = netip.MustParseAddr("::1")

	case ip.IsUnspecified():
		ip = netip.MustParseAddr("127.0.0.1")
	}

	return netip.AddrPortFrom(ip, c.RConPort)
}

// splitDirective separates the first whitespace-delimited key from its value.
// It preserves trailing value whitespace for credentials
// and accepts spaces and tabs without introducing quoting or inline-comment syntax.
func splitDirective(line string) (string, string) {
	separator := strings.IndexFunc(line, unicode.IsSpace)
	if separator < 0 {
		return line, ""
	}

	return line[:separator], strings.TrimLeftFunc(line[separator:], unicode.IsSpace)
}

// parsePort accepts only unsigned decimal ports in the nonzero uint16 range.
func parsePort(value string) (uint16, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, strconv.ErrSyntax
	}

	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0, err
	}
	if port == 0 {
		return 0, strconv.ErrRange
	}

	return uint16(port), nil
}

// directiveError reports a location and safe description without echoing values.
func directiveError(line int, name, reason string) error {
	return fmt.Errorf("line %d: %s: %s", line, name, reason)
}
