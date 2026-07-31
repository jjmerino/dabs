package actions

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/sandbox"
	"github.com/jjmerino/dabs/core/services"
	"github.com/jjmerino/dabs/core/tui"
)

// servicesSubdir is the directory, inside a box node's tmp space, that is bound
// into the box as its services directory. Naming it under tmp is what makes a
// published service die with the box: `rm` reaps that space without asking.
const servicesSubdir = "services"

// portsFileName is the store of name → host port assignments, under ~/.dabs. A
// service keeps its port across boxes, so a URL survives a re-up.
const portsFileName = "service-ports.json"

// resolveServicesDir returns the host directory a box node publishes its
// services into.
func (r Real) resolveServicesDir(nodeID string) (string, error) {
	tmp, err := r.resolveNodeSpace(nodeID, SpaceTmp)
	if err != nil {
		return "", err
	}
	return filepath.Join(tmp, servicesSubdir), nil
}

// resolvePortsFile returns the path of the port-assignment store.
func (r Real) resolvePortsFile() (string, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dabs", portsFileName), nil
}

// Services runs `dabs services [serve]`: with no subcommand it lists what the
// boxes publish; `serve` forwards each one from a stable loopback port and
// serves an index of them until interrupted.
func (r Real) Services(p params.Services) error {
	if p.Serve {
		return r.serveServices()
	}
	return r.listServices()
}

// scanServices reads every box node's services directory and returns what the
// boxes are publishing, each service stamped with the node and instance that
// published it.
func (r Real) scanServices() ([]services.Service, error) {
	nodes, err := r.listNodes()
	if err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	addrs := boxAddrs{}
	var out []services.Service
	for _, n := range nodes {
		if n.Kind != KindBox {
			continue
		}
		dir, err := r.resolveServicesDir(n.ID)
		if err != nil {
			return nil, err
		}
		found, err := services.ScanDir(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range found {
			s.Node, s.Instance = n.ID, n.Instance
			// The mounted socket is the direct door and needs nothing from the
			// driver. Only when it does not answer is the box's own address worth
			// asking for — the question costs a vendor-CLI call per box.
			if _, reachable := s.Route(); !reachable {
				s.BoxAddr = addrs.of(r, n)
			}
			out = append(out, s)
		}
	}
	// One name means one host port. Nodes are walked in id order, so which box
	// owns a name is the same answer here and in the serving process.
	return services.MarkConflicts(out), nil
}

// boxAddrs remembers each box's host-dialable address for the length of one
// scan, so a box publishing several services is asked about once.
type boxAddrs map[string]string

// of returns the address the host can dial to reach the node's box, or "" when
// it has none, the driver cannot say, or the driver has no such notion. A box
// nobody can address is a normal answer here: the socket is the other way in.
func (a boxAddrs) of(r Real, n Node) string {
	if n.Instance == "" {
		return ""
	}
	if addr, ok := a[n.Instance]; ok {
		return addr
	}
	target := ""
	if n.RecipeSpec != nil {
		target = n.RecipeSpec.Target
	}
	addr := ""
	if drv, err := r.driverFor(target); err == nil {
		if ba, can := drv.(sandbox.BoxAddresser); can {
			if got, err := ba.BoxAddress(n.Instance); err == nil {
				addr = got
			}
		}
	}
	a[n.Instance] = addr
	return addr
}

// listServices prints one row per published service: what it is called, what
// kind it is, which box publishes it, the host address `dabs services serve`
// gives it, and whether its socket answers right now.
func (r Real) listServices() error {
	found, err := r.scanServices()
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(os.Stdout, tui.Muted("no box is publishing a service"))
		return nil
	}
	path, err := r.resolvePortsFile()
	if err != nil {
		return err
	}
	ports, err := services.LoadPorts(path)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(found))
	for _, s := range found {
		// A name another box claimed first is not reachable at any address, so it
		// gets none — two rows sharing one host port would be a lie about where
		// the bytes go.
		addr, state := tui.Muted("unassigned"), tui.Muted("down")
		switch {
		case s.Conflict:
			addr, state = tui.Muted("-"), tui.Warn("conflict")
		default:
			if port, ok := ports.Port(s.Name); ok {
				addr = fmt.Sprintf("127.0.0.1:%d", port)
			}
			if s.Up() {
				state = tui.Success("up")
			}
		}
		rows = append(rows, []string{tui.Accent(s.Name), s.Type, s.Node, s.Instance, addr, state})
	}
	fmt.Fprintln(os.Stdout, tui.Rows([]string{"NAME", "TYPE", "BOX", "INSTANCE", "HOST", "STATE"}, rows))
	fmt.Fprintln(os.Stdout, tui.Muted("serve them: dabs services serve"))
	return nil
}

// serveServices forwards every published service from its assigned loopback
// port and serves the index, until the terminal interrupts it.
func (r Real) serveServices() error {
	path, err := r.resolvePortsFile()
	if err != nil {
		return err
	}
	ports, err := services.LoadPorts(path)
	if err != nil {
		return err
	}
	srv := services.NewServer(r.scanServices, ports, os.Stdout)
	stop := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		close(stop)
	}()
	defer signal.Stop(sigs)
	return srv.Serve(stop)
}
