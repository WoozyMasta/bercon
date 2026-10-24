// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Candidate is one recognized config file found directly in a directory.
type Candidate struct {
	// ModTime is the file modification time used to order candidates.
	ModTime time.Time `json:"mod_time" yaml:"mod_time"`

	// Path is the candidate file path.
	Path string `json:"path" yaml:"path"`

	// Active reports whether the filename has the exact _active_<suffix> form.
	Active bool `json:"active" yaml:"active"`
}

// Find returns recognized BattlEye config files directly inside dir,
// ordered with active runtime copies first and newest modification time first within each group.
// Modification time is only a discovery heuristic.
func Find(dir string) ([]Candidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read BattlEye config directory %q: %w", dir, err)
	}

	candidates := make([]Candidate, 0)
	for _, entry := range entries {
		active, matched := matchConfigName(entry.Name())
		if !matched || entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			// A matching name is an intended candidate.
			// Do not silently fall back to another file when its metadata cannot be inspected.
			return nil, fmt.Errorf("stat BattlEye config candidate %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		candidates = append(candidates, Candidate{
			Path:    path,
			ModTime: info.ModTime(),
			Active:  active,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Active != candidates[j].Active {
			return candidates[i].Active
		}
		if !candidates[i].ModTime.Equal(candidates[j].ModTime) {
			return candidates[i].ModTime.After(candidates[j].ModTime)
		}

		return candidates[i].Path < candidates[j].Path
	})

	return candidates, nil
}

// Resolve accepts an explicit regular file or selects the first candidate when path is a directory.
// Explicit files do not need a recognized filename.
func Resolve(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat BattlEye config path %q: %w", path, err)
	}
	if info.IsDir() {
		candidates, err := Find(path)
		if err != nil {
			return "", err
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("%w in %q", ErrConfigNotFound, path)
		}

		return candidates[0].Path, nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("BattlEye config path %q is not a regular file or directory", path)
	}

	return path, nil
}

// matchConfigName recognizes canonical config names and active runtime copies.
func matchConfigName(name string) (active, matched bool) {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".cfg") {
		return false, false
	}

	base := strings.TrimSuffix(lower, ".cfg")
	switch base {
	case "beserver", "beserver_x64":
		return false, true
	}

	for _, prefix := range []string{"beserver_active_", "beserver_x64_active_"} {
		if strings.HasPrefix(base, prefix) && len(base) > len(prefix) {
			return true, true
		}
	}

	return false, false
}
