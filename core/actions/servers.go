package actions

import (
	"fmt"
	"os"
	"sort"

	"github.com/jjmerino/dabs/core/config"
	"github.com/jjmerino/dabs/core/params"
)

// ServersList prints the registered servers.
func (r Real) ServersList(params.ServersList) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Servers) == 0 {
		fmt.Fprintln(os.Stdout, "(no servers registered — dabs servers add <name> [host])")
		return nil
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stdout, "%s\t%s\n", name, cfg.Servers[name].Host)
	}
	return nil
}

// ServersAdd registers a server. Registration is config-only: reachability
// and the remote dabs install are checked on first use.
func (r Real) ServersAdd(p params.ServersAdd) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]config.Server{}
	}
	host := p.Host
	if host == "" {
		host = p.Name
	}
	cfg.Servers[p.Name] = config.Server{Host: host}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "server %s added (host %s)\n", p.Name, host)
	return nil
}

// ServersRemove unregisters a server. Its sandboxes are untouched on the
// remote; they just stop appearing in this machine's fleet.
func (r Real) ServersRemove(p params.ServersRemove) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Servers[p.Name]; !ok {
		fmt.Fprintf(os.Stdout, "no server %s\n", p.Name)
		return nil
	}
	delete(cfg.Servers, p.Name)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "server %s removed\n", p.Name)
	return nil
}
