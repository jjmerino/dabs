package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/jjmerino/dabs/core/config"
	"github.com/jjmerino/dabs/core/sandbox"
	sshdriver "github.com/jjmerino/dabs/core/sandbox/ssh"
)

// buildDrivers assembles the sandbox fleet: the platform's local driver
// plus one ssh driver per configured target (~/.dabs/config.json). A
// missing local driver is tolerated when remote targets exist — commands
// that need it will say so.
func buildDrivers() (map[string]sandbox.Driver, []string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	drivers := map[string]sandbox.Driver{}
	order := []string{}

	local, err := localDriver()
	if err == nil {
		drivers["local"] = local
		order = append(order, "local")
	} else if len(cfg.Targets) == 0 {
		return nil, nil, err
	} else {
		fmt.Fprintf(os.Stderr, "dabs: warning: local driver unavailable: %v\n", err)
	}

	targets := make([]string, 0, len(cfg.Targets))
	for name := range cfg.Targets {
		targets = append(targets, name)
	}
	sort.Strings(targets)
	for _, name := range targets {
		drv, err := sshdriver.New(cfg.Targets[name].Host)
		if err != nil {
			return nil, nil, fmt.Errorf("target %q: %w", name, err)
		}
		drivers[name] = drv
		order = append(order, name)
	}
	return drivers, order, nil
}
