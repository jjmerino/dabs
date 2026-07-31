package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
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
}

// forward is one live loopback listener standing in front of a service's
// socket.
type forward struct {
	served Served
	ln     net.Listener
}

// NewServer returns a server that forwards what source reports, taking each
// service's host port from ports.
func NewServer(source Source, ports *Ports, log io.Writer) *Server {
	return &Server{source: source, ports: ports, log: log, serving: map[string]*forward{}}
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
// service gets a listener on its assigned port, one whose socket is gone loses
// its listener. It is the whole reconciliation step, exposed so a test can run
// exactly one.
func (s *Server) Sync() error {
	found, err := s.source()
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, svc := range found {
		live[svc.Name] = true
		s.mu.Lock()
		existing, ok := s.serving[svc.Name]
		s.mu.Unlock()
		if ok {
			// The name is already served; keep the port and refresh what the index
			// says about it (a re-upped box publishes the same name from a new box).
			s.mu.Lock()
			existing.served.Service = svc
			s.mu.Unlock()
			continue
		}
		port, err := s.ports.Assign(svc.Name, FreeOnLoopback)
		if err != nil {
			fmt.Fprintf(s.log, "%s: %v\n", svc.Name, err)
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			fmt.Fprintf(s.log, "%s: 127.0.0.1:%d: %v\n", svc.Name, port, err)
			continue
		}
		f := &forward{served: Served{Service: svc, Port: port}, ln: ln}
		s.mu.Lock()
		s.serving[svc.Name] = f
		s.mu.Unlock()
		go acceptInto(ln, svc.Socket)
		fmt.Fprintf(s.log, "%s → http://127.0.0.1:%d (%s)\n", svc.Name, port, svc.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, f := range s.serving {
		if live[name] {
			continue
		}
		_ = f.ln.Close()
		delete(s.serving, name)
		fmt.Fprintf(s.log, "%s: gone\n", name)
	}
	return nil
}

// Serving lists what the server currently forwards, by name.
func (s *Server) Serving() []Served {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Served, 0, len(s.serving))
	for _, f := range s.serving {
		out = append(out, f.served)
	}
	sortServed(out)
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

// acceptInto forwards every connection on ln into a fresh connection to the
// service's unix socket. It is raw TCP both ways: nothing here reads the bytes,
// so anything that survives a socket survives this — HTTP, websockets, or a
// protocol dabs has never heard of.
func acceptInto(ln net.Listener, sock string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			up, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer up.Close()
			forwarder.Couple(conn, up)
		}(conn)
	}
}
