// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"RConPassword example\nRConPort 2306\n",
		"; comment\n# comment\n// comment\nRConPassword sample\nRConPort 1\n",
		"RConPassword first\nRConPassword last\nRConPort 65535\n",
		"RConPassword sample\r\nRConPort 2306\r\n",
		"\uFEFFRConPassword sample\nRConPort 2306\n",
		"UnknownDirective value\nRConPassword sample\nRConPort 2306\n",
		"RConPassword sample\nRConPort invalid\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Bound the generated credential so each fuzz input stays small and quick.
		if len(data) > 4096 {
			data = data[:4096]
		}
		_, _ = Parse(bytes.NewReader(data))

		password := "fuzz-secret-" + hex.EncodeToString(data)
		input := "RConPassword " + password + "\nRConPort invalid\n" + string(data)
		_, err := Parse(strings.NewReader(input))
		if err != nil && strings.Contains(err.Error(), password) {
			t.Fatal("Parse() error contains the configured password")
		}
	})
}
