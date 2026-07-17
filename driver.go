package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/jjmerino/dabs/core/config"
	"github.com/jjmerino/dabs/core/sandbox"
	dockerdrv "github.com/jjmerino/dabs/core/sandbox/docker"
	"github.com/jjmerino/dabs/core/sandbox/server"
	"github.com/jjmerino/dabs/core/tui"
)

// buildDrivers assembles the drivers dabs dispatches across: the platform's
// local driver plus one server driver per registered server
// (~/.dabs/config.json, see `dabs servers`). The local driver is LAZY: its
// probe (a LookPath for the vendor CLI) runs on first use, so a command that
// never touches a driver — listing recipes, printing a node's directory,
// cutting a boxless place — runs on a machine with no sandboxing tool at all,
// and one that does gets the driver's own install-hint error.
func buildDrivers() (map[string]sandbox.Driver, []string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	drivers := map[string]sandbox.Driver{}
	order := []string{}

	drivers["local"] = sandbox.Lazy(localKind, localDriver)
	order = append(order, "local")

	// docker driver: selectable via a recipe's `target: docker`. Registered
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
			// One invalid entry must not brick every command. Skip building its
			// driver but leave the entry in the loaded config, so `dabs servers`
			// still lists it and `dabs servers rm` can remove it.
			fmt.Fprintln(os.Stderr, tui.Warn("dabs: server %q unavailable: %v", name, err))
			continue
		}
		drivers[name] = drv
		order = append(order, name)
	}
	return drivers, order, nil
}
