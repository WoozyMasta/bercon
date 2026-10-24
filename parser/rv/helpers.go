// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package rv

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"

	"github.com/woozymasta/bercon/parser"
)

// parseAddrPort accepts an IP address with or without a port.
func parseAddrPort(s string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap, nil
	}

	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.AddrPort{}, err
	}

	return netip.AddrPortFrom(a, 0), nil
}

// isGUID validates the observed 32-character hexadecimal BattlEye form.
func isGUID(s string) bool {
	if len(s) != 32 {
		return false
	}

	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}

	return true
}

// hasLine reports whether data contains a complete, case-insensitive marker line.
func hasLine(data []byte, marker string) bool {
	markerBytes := []byte(marker)
	for len(data) > 0 {
		line := data
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			line = data[:newline]
			data = data[newline+1:]
		} else {
			data = nil
		}

		if bytes.EqualFold(bytes.TrimSpace(line), markerBytes) {
			return true
		}
	}

	return false
}

// joinIssues returns nil for clean input or an errors.Is-compatible aggregate.
func joinIssues(issues []string) error {
	if len(issues) == 0 {
		return nil
	}

	return errors.Join(append([]error{parser.ErrMalformedRows}, toErrors(issues)...)...)
}

// toErrors converts parser diagnostics into independent error values.
func toErrors(s []string) []error {
	out := make([]error, 0, len(s))
	for _, v := range s {
		out = append(out, errors.New(v))
	}

	return out
}
