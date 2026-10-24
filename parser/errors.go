// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package parser

import "errors"

// ErrUnrecognized reports that a payload does not match the parser's response schema.
var ErrUnrecognized = errors.New("response format is not recognized")

// ErrMalformedRows marks a partial parse that contains malformed candidate rows.
var ErrMalformedRows = errors.New("one or more rows are malformed")
