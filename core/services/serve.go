package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jjmerino/dabs/core/door"
)

// Source reports the services published right now. Serve calls it repeatedly,
// so a box that boots (or dies) while the host is serving is picked up without
// anything being restarted.
type Source func() ([]Service, error)

// ScanEvery is how often Serve re-reads the source.
const ScanEvery = 2 * time.Second

// Served is one service the host is currently forwarding: the service itself,
// and the loopback port it answers on.
type Served struct {
	Service
	Port int
	// Down marks a service that is still published but did not answer on the
	// last scan. Its port keeps standing — the address was handed out — and the
	// index says so.
	Down bool
}

// URL is the address a client reaches the service at.
func (s Served) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.Port) }

// Addr is the host address the service answers on.
func (s Served) Addr() string { return fmt.Sprintf("127.0.0.1:%d", s.Port) }

// Server forwards every published service from a stable loopback port and
// serves an index of them. It is the whole host side of `dabs services serve`;
// the CLI only builds one and runs it.
type Server struct {
	source Source
	ports  *Ports
	log    io.Writer

	mu      sync.Mutex
	serving map[string]*forward // by service name
	// told records the socket each conflicting name was last reported for, so a
	// collision is named once instead of once per scan.
	told map[string]string
	// conflicted is the services a name's owner shut out, as of the last scan —
	// the index shows them, because a service that is published and unreachable
	// is exactly what someone comes to the index to find out about.
	conflicted []Service
}

// forward is one live loopback listener standing in front of a service's
// socket. The socket it dials is read PER CONNECTION and may be replaced while
// the listener stays up: a re-upped box publishes the same name from a new node
// directory, and the port a human wrote down has to follow it there.
type forward struct {
	ln net.Listener

	mu     sync.Mutex
	served Served
	socket string
}

// markDown records that the service is still published but did not answer this
// scan. The listener stays: a box under momentary load is not a box that went
// away, and closing the port would take the address a human is holding.
func (f *forward) markDown(svc Service) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served.Service = svc
	f.served.Down = true
}

// target is the socket the forward currently dials.
func (f *forward) target() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.socket
}

// retarget points the forward at svc over socket, keeping its port and listener.
func (f *forward) retarget(svc Service, socket string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served.Service = svc
	f.served.Down = false
	f.socket = socket
}

// snapshot is what the index renders for this forward.
func (f *forward) snapshot() Served {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served
}

// NewServer returns a server that forwards what source reports, taking each
// service's host port from ports.
func NewServer(source Source, ports *Ports, log io.Writer) *Server {
	return &Server{source: source, ports: ports, log: log, serving: map[string]*forward{}, told: map[string]string{}}
}

// Serve forwards services and serves the index at 127.0.0.1:IndexPort until
// stop is closed. It returns when the index listener fails or stop fires.
func (s *Server) Serve(stop <-chan struct{}) error {
	index, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", IndexPort))
	if err != nil {
		return fmt.Errorf("index on 127.0.0.1:%d: %w", IndexPort, err)
	}
	defer index.Close()
	idx := &http.Server{Handler: s}
	go func() { _ = idx.Serve(index) }()
	fmt.Fprintf(s.log, "index: http://127.0.0.1:%d\n", IndexPort)
	for {
		if err := s.Sync(); err != nil {
			fmt.Fprintf(s.log, "scan: %v\n", err)
		}
		select {
		case <-stop:
			_ = idx.Close()
			s.closeAll()
			return nil
		case <-time.After(ScanEvery):
		}
	}
}

// Sync brings the live forwards in line with what the source reports: a new
// service gets a listener on its assigned port, one that moved to a new socket
// is retargeted at it, one that is gone loses its listener, and a name a second
// box also claims is reported rather than silently swallowed. It is the whole
// reconciliation step, exposed so a test can run exactly one.
func (s *Server) Sync() error {
	found, err := s.source()
	if err != nil {
		return err
	}
	found = MarkConflicts(found)
	live := map[string]bool{}
	var conflicted []Service
	for _, svc := range found {
		if svc.Conflict {
			conflicted = append(conflicted, svc)
			s.tell(svc)
			continue
		}
		// Still published — the descriptor and socket are there — so the name keeps
		// its listener whatever the probe says. Whether it ANSWERS is a separate
		// question, asked again every scan.
		live[svc.Name] = true
		socket, reachable := svc.Route()
		s.mu.Lock()
		existing, held := s.serving[svc.Name]
		s.mu.Unlock()
		if held {
			if !reachable {
				existing.markDown(svc)
				continue
			}
			// Where the name answers may have moved — a re-up publishes it from a
			// new node directory — and the port must follow it, or it forwards into
			// a dead door for ever.
			if existing.target() != socket {
				fmt.Fprintf(s.log, "%s → %s (moved)\n", svc.Name, socket)
			}
			existing.retarget(svc, socket)
			continue
		}
		// Nothing is listening for this name yet and nothing answers behind it:
		// there is no address worth handing out. Retried on the next scan.
		if !reachable {
			continue
		}
		f, err := s.open(svc, socket)
		if err != nil {
			fmt.Fprintf(s.log, "%s: %v\n", svc.Name, err)
			continue
		}
		s.mu.Lock()
		s.serving[svc.Name] = f
		s.mu.Unlock()
		go acceptInto(f)
		fmt.Fprintf(s.log, "%s → http://127.0.0.1:%d (%s)\n", svc.Name, f.snapshot().Port, svc.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicted = conflicted
	for name, f := range s.serving {
		if live[name] {
			continue
		}
		_ = f.ln.Close()
		delete(s.serving, name)
		delete(s.told, name)
		fmt.Fprintf(s.log, "%s: gone\n", name)
	}
	return nil
}

// open binds the service's assigned port. An assignment persisted earlier can
// have been taken by something else in the meantime, and retrying it every scan
// would never come back — so a refused port is given up and a fresh one probed
// and persisted in its place.
func (s *Server) open(svc Service, socket string) (*forward, error) {
	port, err := s.ports.Assign(svc.Name, FreeOnLoopback)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Fprintf(s.log, "%s: 127.0.0.1:%d is taken, moving it\n", svc.Name, port)
		port, err = s.ports.Reassign(svc.Name, FreeOnLoopback)
		if err != nil {
			return nil, err
		}
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, err
		}
	}
	return &forward{ln: ln, served: Served{Service: svc, Port: port}, socket: socket}, nil
}

// tell reports a name a second box also claims — once per claimant, not once
// per scan.
func (s *Server) tell(svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.told[svc.Name] == svc.Socket {
		return
	}
	s.told[svc.Name] = svc.Socket
	fmt.Fprintf(s.log, "%s: also published by %s — not served, rename one of them\n", svc.Name, svc.Node)
}

// Serving lists what the server currently forwards, by name.
func (s *Server) Serving() []Served {
	s.mu.Lock()
	forwards := make([]*forward, 0, len(s.serving))
	for _, f := range s.serving {
		forwards = append(forwards, f)
	}
	s.mu.Unlock()
	out := make([]Served, 0, len(forwards))
	for _, f := range forwards {
		out = append(out, f.snapshot())
	}
	sortServed(out)
	return out
}

// Conflicts lists the services shut out by a name's owner, as of the last scan.
func (s *Server) Conflicts() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Service{}, s.conflicted...)
	// Name then node: two boxes shut out of one name must not swap places
	// between refreshes of the index.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// closeAll drops every live listener.
func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, f := range s.serving {
		_ = f.ln.Close()
		delete(s.serving, name)
	}
}

// acceptInto forwards every connection on the forward's listener into a fresh
// connection to wherever it currently targets. It is raw bytes both ways:
// nothing here reads them, so anything that survives a socket survives this —
// HTTP, websockets, or a protocol dabs has never heard of.
func acceptInto(f *forward) {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			up, err := net.Dial("unix", f.target())
			if err != nil {
				return
			}
			defer up.Close()
			door.Couple(conn, up)
		}(conn)
	}
}
