// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import "errors"

var (
	// ErrConfigNotFound means a directory contains no recognized BattlEye config files.
	ErrConfigNotFound = errors.New("BattlEye config not found")
	// ErrRConPasswordMissing means the parsed config has no non-empty RConPassword.
	ErrRConPasswordMissing = errors.New("RConPassword is missing")
	// ErrRConPortMissing means the parsed config has no RConPort directive.
	ErrRConPortMissing = errors.New("RConPort is missing")
)
