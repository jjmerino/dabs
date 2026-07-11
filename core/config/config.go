// Package config loads and saves dabs's own configuration
// (~/.dabs/config.json). Absent file = zero config = local driver only; a
// missing config is never an error.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Server is a registered remote machine running dabs. HOW dabs reaches it
// is the transport (Via): "ssh" today (pubkey auth), a future "dabs serve"
// daemon later. Servers are one kind of sandbox target; future driver kinds
// (modal, daytona, …) will be targets without being servers.
type Server struct {
	Via  string `json:"via,omitempty"` // transport strategy; empty ⇒ "ssh"
	Host string `json:"host"`          // transport address (e.g. "homelab", "user@10.0.0.5")
}

// Transport returns the connection strategy, defaulting to ssh.
func (s Server) Transport() string {
	if s.Via == "" {
		return "ssh"
	}
	return s.Via
}

// Config declares the sandbox fleet: the local driver always exists;
// servers add remote machines. Recipes choose where their sandboxes live
// via their `target`; ls aggregates across everything.
type Config struct {
	Servers map[string]Server `json:"servers"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return filepath.Join(home, ".dabs", "config.json"), nil
}

// Load reads ~/.dabs/config.json, returning the zero Config when absent.
func Load() (Config, error) {
	var c Config
	p, err := path()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(p)
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

// Save writes ~/.dabs/config.json, creating the directory if needed.
func Save(c Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.WriteFile(p, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
