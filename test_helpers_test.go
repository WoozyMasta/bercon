// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import "testing"

// waitForTestServer waits for a test server and reports any unexpected serve error.
func waitForTestServer(t testing.TB, done <-chan error) {
	t.Helper()

	if err := <-done; err != nil {
		t.Errorf("test server Serve returned an error: %v", err)
	}
}
