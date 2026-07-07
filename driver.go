package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/jjmerino/dabs/core/config"
	"github.com/jjmerino/dabs/core/sandbox"
	dockerdrv "github.com/jjmerino/dabs/core/sandbox/docker"
	"github.com/jjmerino/dabs/core/sandbox/server"
)

// buildDrivers assembles the sandbox fleet: the platform's local driver
// plus one server driver per registered server (~/.dabs/config.json, see
// `dabs servers`). A missing local driver is tolerated when servers exist —
// commands that need it will say so.
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
	} else if len(cfg.Servers) == 0 {
		return nil, nil, err
	} else {
		fmt.Fprintf(os.Stderr, "dabs: warning: local driver unavailable: %v\n", err)
	}

	// docker driver: selectable via dabs.json "driver":"docker". Registered
	// whenever docker is present, regardless of platform.
	if dkr, err := dockerdrv.New(); err == nil {
		drivers["docker"] = dkr
		order = append(order, "docker")
		// INTERNAL privileged variant for running a nested sandbox in the box.
		// Map-only (NOT in order): it creates the same containers "docker"
		// already lists/resolves, so listing it too would double-count. It only
		// differs at `up` time (adds --privileged -v /tmp).
		if nd, err := dockerdrv.NewNested(); err == nil {
			drivers["INTERNAL-docker-privileged-for-nested-sandboxing"] = nd
		}
	}

	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		drv, err := server.New(cfg.Servers[name].Transport(), cfg.Servers[name].Host)
		if err != nil {
			return nil, nil, fmt.Errorf("server %q: %w", name, err)
		}
		drivers[name] = drv
		order = append(order, name)
	}
	return drivers, order, nil
}
