// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		password   string
		port       uint16
		ip         string
		restricted *bool
		wantErr    error
		wantLine   string
	}{
		{
			name:     "normal",
			input:    "RConPassword example\nRConPort 2306\n",
			password: "example",
			port:     2306,
		},
		{
			name: "case whitespace comments and BOM",
			input: "\uFEFF\t rCoNpAsSwOrD\tpassword with spaces  \r\n" +
				"; comment\r\n# comment\r\n// comment\r\n\r\n" +
				" RCONPORT\t65535 \r\nRConIP 2001:db8::1\r\n" +
				"RestrictRCon 0\r\nUnknownDirective anything\r\n",
			password:   "password with spaces  ",
			port:       65535,
			ip:         "2001:db8::1",
			restricted: boolPointer(false),
		},
		{
			name: "last duplicate wins",
			input: "RConPassword first\nRConPort 1\nRConIP 192.0.2.1\n" +
				"RestrictRCon 1\nRConPassword last value\nRConPort 42\n" +
				"RConIP 0.0.0.0\nRestrictRCon 0\n",
			password:   "last value",
			port:       42,
			ip:         "0.0.0.0",
			restricted: boolPointer(false),
		},
		{
			name:     "minimum port",
			input:    "RConPassword example\nRConPort 1\n",
			password: "example",
			port:     1,
		},
		{
			name:     "explicit ipv4",
			input:    "RConPassword example\nRConPort 2306\nRConIP 192.0.2.10\n",
			password: "example",
			port:     2306,
			ip:       "192.0.2.10",
		},
		{
			name:       "restrict true",
			input:      "RConPassword example\nRConPort 2306\nRestrictRCon 1\n",
			password:   "example",
			port:       2306,
			restricted: boolPointer(true),
		},
		{
			name:    "missing password",
			input:   "RConPort 2306\n",
			wantErr: ErrRConPasswordMissing,
		},
		{
			name:    "missing port",
			input:   "RConPassword example\n",
			wantErr: ErrRConPortMissing,
		},
		{
			name:     "invalid port syntax",
			input:    "RConPassword example\nRConPort 23x\n",
			wantLine: "line 2: RConPort",
		},
		{
			name:     "zero port",
			input:    "RConPassword example\nRConPort 0\n",
			wantLine: "line 2: RConPort",
		},
		{
			name:     "port overflow",
			input:    "RConPassword example\nRConPort 65536\n",
			wantLine: "line 2: RConPort",
		},
		{
			name:     "signed port",
			input:    "RConPassword example\nRConPort +1\n",
			wantLine: "line 2: RConPort",
		},
		{
			name:     "invalid ip",
			input:    "RConPassword example\nRConPort 2306\nRConIP host.example\n",
			wantLine: "line 3: RConIP",
		},
		{
			name:     "invalid restrict value",
			input:    "RConPassword example\nRConPort 2306\nRestrictRCon 2\n",
			wantLine: "line 3: RestrictRCon",
		},
		{
			name:     "empty password directive",
			input:    "RConPassword secret-value\nRConPassword\nRConPort 2306\n",
			wantLine: "line 2: RConPassword",
		},
		{
			name:     "empty port directive",
			input:    "RConPassword example\nRConPort\nRConPort 2306\n",
			wantLine: "line 2: RConPort",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Parse(strings.NewReader(test.input))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Parse() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if test.wantLine != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantLine) {
					t.Fatalf("Parse() error = %v, want it to contain %q", err, test.wantLine)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Fatalf("Parse() error exposed the password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if cfg.RConPassword != test.password || cfg.RConPort != test.port {
				t.Fatalf("Parse() returned incorrect password or port: port=%d", cfg.RConPort)
			}
			if test.ip == "" {
				if cfg.RConIP.IsValid() {
					t.Fatalf("Parse() IP = %s, want absent", cfg.RConIP)
				}
			} else if cfg.RConIP.String() != test.ip {
				t.Fatalf("Parse() IP = %s, want %s", cfg.RConIP, test.ip)
			}
			if (cfg.RestrictRCon == nil) != (test.restricted == nil) {
				t.Fatalf("Parse() RestrictRCon = %v, want %v", cfg.RestrictRCon, test.restricted)
			}
			if cfg.RestrictRCon != nil && *cfg.RestrictRCon != *test.restricted {
				t.Fatalf("Parse() RestrictRCon = %t, want %t", *cfg.RestrictRCon, *test.restricted)
			}
		})
	}
}

func TestParseNilReader(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse(nil) error = nil, want reader error")
	}
}

func TestParseMalformedDirectiveDoesNotHideBehindLaterValue(t *testing.T) {
	_, err := Parse(strings.NewReader("RConPassword example\nRConPort invalid\nRConPort 2306\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2: RConPort") {
		t.Fatalf("Parse() error = %v, want malformed first RConPort error", err)
	}
}

func TestParseErrorsDoNotExposePassword(t *testing.T) {
	const password = "top-secret-test-value"
	_, err := Parse(strings.NewReader("RConPassword " + password + "\nRConIP invalid\n"))
	if err == nil {
		t.Fatal("Parse() error = nil, want malformed RConIP error")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("Parse() error exposed the password: %v", err)
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.cfg")
	if err := os.WriteFile(path, []byte("RConPassword example\nRConPort 2306\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RConPassword != "example" || cfg.RConPort != 2306 {
		t.Fatalf("Load() returned incorrect RCon settings: port=%d", cfg.RConPort)
	}
}

func TestLocalAddrPort(t *testing.T) {
	tests := []struct {
		name string
		ip   netip.Addr
		want string
	}{
		{name: "missing uses IPv4 loopback", want: "127.0.0.1:2306"},
		{name: "IPv4 wildcard uses IPv4 loopback", ip: netip.MustParseAddr("0.0.0.0"), want: "127.0.0.1:2306"},
		{name: "IPv6 wildcard uses IPv6 loopback", ip: netip.IPv6Unspecified(), want: "[::1]:2306"},
		{name: "explicit IPv4 preserved", ip: netip.MustParseAddr("192.0.2.3"), want: "192.0.2.3:2306"},
		{name: "explicit IPv6 preserved", ip: netip.MustParseAddr("2001:db8::3"), want: "[2001:db8::3]:2306"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr := (Config{RConIP: test.ip, RConPort: 2306}).LocalAddrPort()
			if addr.String() != test.want {
				t.Fatalf("LocalAddrPort() = %s, want %s", addr, test.want)
			}
		})
	}
}

// boolPointer keeps table cases concise while giving RestrictRCon an explicit value.
func boolPointer(value bool) *bool {
	return &value
}
