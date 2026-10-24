// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package beconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindRecognizedNames(t *testing.T) {
	names := []struct {
		name   string
		active bool
	}{
		{name: "BEServer.cfg"},
		{name: "BEServer_x64.cfg"},
		{name: "beserver.cfg"},
		{name: "BESERVER_X64.CFG"},
		{name: "BeServer_active_123.cfg", active: true},
		{name: "beserver_x64_active_runtime.cfg", active: true},
	}

	for _, test := range names {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			createConfigFile(t, dir, test.name, time.Unix(100, 0))

			candidates, err := Find(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != 1 || candidates[0].Active != test.active {
				t.Fatalf("Find() = %#v, want one candidate with Active=%t", candidates, test.active)
			}
		})
	}
}

func TestFindOrdering(t *testing.T) {
	dir := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	createConfigFile(t, dir, "BEServer.cfg", base.Add(24*time.Hour))
	createConfigFile(t, dir, "BEServer_x64.cfg", base.Add(48*time.Hour))
	createConfigFile(t, dir, "BEServer_active_a.cfg", base.Add(2*time.Hour))
	createConfigFile(t, dir, "BEServer_x64_active_old.cfg", base.Add(time.Hour))
	createConfigFile(t, dir, "BEServer_x64_active_new.cfg", base.Add(3*time.Hour))

	candidates, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"BEServer_x64_active_new.cfg",
		"BEServer_active_a.cfg",
		"BEServer_x64_active_old.cfg",
		"BEServer_x64.cfg",
		"BEServer.cfg",
	}
	if len(candidates) != len(want) {
		t.Fatalf("Find() returned %d candidates, want %d", len(candidates), len(want))
	}
	for i, name := range want {
		if filepath.Base(candidates[i].Path) != name {
			t.Fatalf("candidate %d = %q, want %q", i, filepath.Base(candidates[i].Path), name)
		}
	}
}

func TestFindNewestRegularWhenNoActiveConfigExists(t *testing.T) {
	dir := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	createConfigFile(t, dir, "BEServer.cfg", base)
	createConfigFile(t, dir, "BEServer_x64.cfg", base.Add(time.Hour))

	candidates, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || filepath.Base(candidates[0].Path) != "BEServer_x64.cfg" {
		t.Fatalf("Find() = %#v, want newest regular config first", candidates)
	}
}

func TestFindDeterministicTieBreak(t *testing.T) {
	dir := t.TempDir()
	modTime := time.Unix(1_700_000_000, 0)
	createConfigFile(t, dir, "BEServer_active_z.cfg", modTime)
	createConfigFile(t, dir, "BEServer_active_a.cfg", modTime)
	createConfigFile(t, dir, "BEServer.cfg", modTime)
	createConfigFile(t, dir, "BEServer_x64.cfg", modTime)

	candidates, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BEServer_active_a.cfg", "BEServer_active_z.cfg", "BEServer.cfg", "BEServer_x64.cfg"}
	for i, name := range want {
		if filepath.Base(candidates[i].Path) != name {
			t.Fatalf("candidate %d = %q, want %q", i, filepath.Base(candidates[i].Path), name)
		}
	}
}

func TestFindIgnoresUnrecognizedAndNestedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"BEServer_x64_inactive.cfg",
		"BEServer_x64_notactive.cfg",
		"BEServer_x64_myactivebackup.cfg",
		"BEServer_x64_active_.cfg",
		"other.cfg",
	} {
		createConfigFile(t, dir, name, time.Unix(100, 0))
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	createConfigFile(t, nested, "BEServer_x64.cfg", time.Unix(100, 0))

	candidates, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("Find() = %#v, want no candidates", candidates)
	}
	if _, err := Resolve(dir); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrConfigNotFound", err)
	}
}

func TestFindReturnsCandidateMetadataErrors(t *testing.T) {
	dir := t.TempDir()
	createConfigFile(t, dir, "BEServer.cfg", time.Unix(100, 0))
	broken := filepath.Join(dir, "BEServer_active_broken.cfg")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), broken); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	if _, err := Find(dir); err == nil {
		t.Fatal("Find() error = nil, want metadata error for recognized broken symlink")
	}
}

func TestResolveAndLoadExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-selected.cfg")
	if err := os.WriteFile(path, []byte("RConPassword example\nRConPort 2306\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != path {
		t.Fatalf("Resolve() = %q, want explicit path %q", resolved, path)
	}
	if _, err := Load(resolved); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDirectoryUsesFindPreference(t *testing.T) {
	dir := t.TempDir()
	createConfigFile(t, dir, "BEServer_x64.cfg", time.Now())
	createConfigFile(t, dir, "BEServer_active_runtime.cfg", time.Now().Add(-time.Hour))

	path, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "BEServer_active_runtime.cfg" {
		t.Fatalf("Resolve() = %q, want active config", path)
	}
}

func TestResolveSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.cfg")
	link := filepath.Join(dir, "selected.cfg")
	if err := os.WriteFile(target, []byte("RConPassword example\nRConPort 2306\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	resolved, err := Resolve(link)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != link {
		t.Fatalf("Resolve() = %q, want symlink path %q", resolved, link)
	}
	if _, err := Load(resolved); err != nil {
		t.Fatal(err)
	}
}

// createConfigFile creates a candidate with a controlled modification time.
func createConfigFile(t *testing.T, dir, name string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
