package actions

import (
	"fmt"
	"os"
	"sort"

	"github.com/jjmerino/dabs/core/config"
	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// ServersList prints the fleet: the local machine (when a local driver is
// installed) plus every registered server, each as "<name>\t<strategy>
// <destination>".
func (r Real) ServersList(params.ServersList) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var rows [][]string
	if drv, ok := r.drivers["local"]; ok {
		rows = append(rows, []string{tui.Accent("local"), drv.Kind(), tui.Muted("this machine")})
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := cfg.Servers[name]
		rows = append(rows, []string{tui.Accent(name), s.Transport(), s.Host})
	}
	fmt.Fprintln(os.Stdout, tui.Rows([]string{"NAME", "VIA", "DESTINATION"}, rows))
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
	fmt.Fprintln(os.Stdout, tui.Success("server %s added %s", tui.Accent(p.Name), tui.Muted("(host %s)", host)))
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
		fmt.Fprintln(os.Stdout, tui.Muted("no server %s", p.Name))
		return nil
	}
	delete(cfg.Servers, p.Name)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, tui.Success("server %s removed", tui.Accent(p.Name)))
	return nil
}
