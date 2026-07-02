// Package config loads dabs's own configuration (~/.dabs/config.json).
// Absent file = zero config = local driver only; a missing config is never
// an error.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Target is a remote machine with dabs installed, reachable over ssh with
// pubkey auth.
type Target struct {
	Host string `json:"host"` // ssh destination (e.g. "homelab", "user@10.0.0.5")
}

// Config declares the sandbox fleet: the local driver always exists;
// targets add remote machines. Manifests choose where their sandboxes live
// via dabs.json "target"; ls aggregates across everything.
type Config struct {
	Targets map[string]Target `json:"targets"`
}

// Load reads ~/.dabs/config.json, returning the zero Config when absent.
func Load() (Config, error) {
	var c Config
	home, err := os.UserHomeDir()
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".dabs", "config.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}
