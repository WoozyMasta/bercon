// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/woozymasta/bercon/beconfig"
)

func ExampleResolve() {
	dir, err := os.MkdirTemp("", "beconfig-example")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	configPath := filepath.Join(dir, "BEServer_x64.cfg")
	if err := os.WriteFile(configPath, []byte("RConPassword example\nRConPort 2306\n"), 0o600); err != nil {
		panic(err)
	}

	path, err := beconfig.Resolve(dir)
	if err != nil {
		panic(err)
	}
	cfg, err := beconfig.Load(path)
	if err != nil {
		panic(err)
	}

	fmt.Println(cfg.LocalAddrPort())
	// Output: 127.0.0.1:2306
}
