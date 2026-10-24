//go:build integration

// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/bercon

package bercon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// errNoIntegrationTargets distinguishes an intentionally absent configuration.
var errNoIntegrationTargets = errors.New("live integration targets are not configured")

// integrationTarget describes one locally configured endpoint
// without storing its password in the target document.
type integrationTarget struct {
	Name               string             `json:"name"`                           // Safe display label for test output.
	Address            string             `json:"address"`                        // UDP endpoint to probe.
	PasswordEnv        string             `json:"password_env"`                   // Environment variable holding the secret.
	ResponseModel      string             `json:"response_model"`                 // Observed response behavior for reporting.
	Probes             []integrationProbe `json:"probes"`                         // Read-only commands to execute.
	AllowUnknownProbes bool               `json:"allow_unknown_probes,omitempty"` // Explicit opt-in for commands outside the allowlist.
}

// integrationProbe is one configured, read-only command and its observation.
type integrationProbe struct {
	Name       string `json:"name"`        // Label used in test output.
	Command    string `json:"command"`     // Configured read-only RCon command.
	Expect     string `json:"expect"`      // Observable result required by the probe.
	ObserveFor string `json:"observe_for"` // Optional event collection window.
}

// TestLiveTargets runs only when explicitly enabled and locally configured.
func TestLiveTargets(t *testing.T) {
	targets, err := loadIntegrationTargets()
	if errors.Is(err, errNoIntegrationTargets) {
		t.Skip("live targets are not configured")
	}
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range targets {
		target := target
		t.Run(target.Name, func(t *testing.T) {
			runIntegrationTarget(t, target)
		})
	}
}

// TestIntegrationConfigRequiresUnknownProbeOptIn protects the default live suite.
func TestIntegrationConfigRequiresUnknownProbeOptIn(t *testing.T) {
	target := integrationTarget{
		Name:          "safe-label",
		Address:       "127.0.0.1:2305",
		PasswordEnv:   "TEST_PASSWORD",
		ResponseModel: "command-response",
		Probes: []integrationProbe{{
			Name:    "unsafe",
			Command: "#kick 1",
			Expect:  "observe",
		}},
	}
	if err := validateIntegrationTarget(target); err == nil {
		t.Fatal("unknown command was accepted without explicit opt-in")
	}
	target.AllowUnknownProbes = true
	if err := validateIntegrationTarget(target); err != nil {
		t.Fatalf("explicit unknown-probe opt-in was rejected: %v", err)
	}
}

// loadIntegrationTargets reads JSON from exactly one environment source.
func loadIntegrationTargets() ([]integrationTarget, error) {
	inline := os.Getenv("BERCON_INTEGRATION_TARGETS")
	path := os.Getenv("BERCON_INTEGRATION_TARGETS_FILE")
	if inline != "" && path != "" {
		return nil, errors.New("configure only one BERCON_INTEGRATION_TARGETS source")
	}

	if inline == "" && path == "" {
		return nil, errNoIntegrationTargets
	}

	data := []byte(inline)
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read integration target file: %w", err)
		}
	}

	var targets []integrationTarget
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("decode integration targets: %w", err)
	}
	if len(targets) == 0 {
		return nil, errors.New("integration target list is empty")
	}

	for i := range targets {
		if err := validateIntegrationTarget(targets[i]); err != nil {
			return nil, fmt.Errorf("integration target %d: %w", i, err)
		}
	}

	return targets, nil
}

// validateIntegrationTarget requires explicit approval outside the read-only allowlist.
func validateIntegrationTarget(target integrationTarget) error {
	if strings.TrimSpace(target.Name) == "" ||
		strings.TrimSpace(target.Address) == "" ||
		target.PasswordEnv == "" {
		return errors.New("name, address, and password_env are required")
	}

	if target.ResponseModel != "observe" &&
		target.ResponseModel != "command-response" &&
		target.ResponseModel != "event-stream" {
		return fmt.Errorf("unsupported response_model %q", target.ResponseModel)
	}

	if len(target.Probes) == 0 {
		return errors.New("at least one probe is required")
	}

	for i, probe := range target.Probes {
		if strings.TrimSpace(probe.Name) == "" ||
			strings.TrimSpace(probe.Command) == "" {
			return fmt.Errorf("probe %d requires name and command", i)
		}

		if probe.Expect != "observe" &&
			probe.Expect != "response" &&
			probe.Expect != "messages" {
			return fmt.Errorf("probe %q has unsupported expectation %q", probe.Name, probe.Expect)
		}

		if probe.ObserveFor != "" {
			observeFor, err := time.ParseDuration(probe.ObserveFor)
			if err != nil || observeFor < 0 {
				return fmt.Errorf("probe %q has invalid observe_for", probe.Name)
			}
		}

		if !isReadOnlyProbe(probe.Command) && !target.AllowUnknownProbes {
			return fmt.Errorf("probe %q is not read-only allowlisted; set allow_unknown_probes explicitly", probe.Name)
		}
	}

	return nil
}

// isReadOnlyProbe recognizes exact commands with known read-only behavior.
func isReadOnlyProbe(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "version", "players", "admins", "bans", "missions", "#players":
		return true

	default:
		return false
	}
}

// runIntegrationTarget executes probes and records response metadata only.
func runIntegrationTarget(t *testing.T, target integrationTarget) {
	t.Helper()
	password := os.Getenv(target.PasswordEnv)
	if password == "" {
		t.Fatalf("password environment variable %q is empty", target.PasswordEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := Dial(ctx, target.Address, password, nil)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Login may deliver independent events before the application starts probing.
	initialMessages := drainMessages(client, 100*time.Millisecond)
	t.Logf("model=%s initial_messages=%d", target.ResponseModel, initialMessages)

	for _, probe := range target.Probes {
		probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
		response, commandErr := client.Command(probeCtx, probe.Command)
		probeCancel()
		if commandErr != nil {
			t.Errorf("probe %q failed: %v", probe.Name, commandErr)
			continue
		}

		observeFor := time.Second
		if probe.ObserveFor != "" {
			observeFor, _ = time.ParseDuration(probe.ObserveFor)
		}
		messages := drainMessages(client, observeFor)
		t.Logf("probe=%s response_bytes=%d messages=%d", probe.Name, len(response), messages)

		switch probe.Expect {
		case "response":
			if len(response) == 0 {
				t.Errorf("probe %q expected a non-empty command response", probe.Name)
			}

		case "messages":
			if messages == 0 {
				t.Errorf("probe %q expected at least one server message", probe.Name)
			}
		}
	}
}

// drainMessages counts queued asynchronous events without logging their payloads.
func drainMessages(client *Client, observeFor time.Duration) int {
	timer := time.NewTimer(observeFor)
	defer timer.Stop()

	count := 0
	messages := client.Messages()
	for {
		select {
		case _, ok := <-messages:
			if !ok {
				return count
			}
			count++

		case <-timer.C:
			return count
		}
	}
}
