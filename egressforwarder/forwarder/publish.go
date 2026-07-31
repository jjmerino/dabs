package forwarder

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ServicesDir is where a box publishes its services, in dabs's own box-path
// namespace. The host binds a directory here, so the sockets and descriptors a
// box writes are plain host files: the directory IS the registry, and there is
// no control protocol to speak.
const ServicesDir = "/run/dabs/services"

// Service types. The type travels with a service so a host-side index can
// render a browsable address as a link; it never changes how bytes are routed.
const (
	TypeWebUI   = "webui"
	TypeGeneral = "general"
)

// Descriptor is the JSON a published service writes beside its socket, at
// <ServicesDir>/<name>.json. It states what the service is and which box-local
// loopback port the socket leads to.
type Descriptor struct {
	Type string `json:"type"`
	Port int    `json:"port"`
}

// DescriptorName is the descriptor file a service of the given name writes.
func DescriptorName(name string) string { return name + ".json" }

// SocketName is the socket file a service of the given name listens on.
func SocketName(name string) string { return name + ".sock" }

// CheckServiceName reports whether name can be a service's filename: a single
// path element, non-empty, no separators and no directory shorthand. This is
// the whole rule — the forwarder is plumbing and takes the name it is given.
func CheckServiceName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("service name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("service name %q is a directory shorthand", name)
	case strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, os.PathSeparator):
		return fmt.Errorf("service name %q contains a path separator", name)
	}
	return nil
}

// CheckServiceType reports whether typ is a known service type.
func CheckServiceType(typ string) error {
	if typ != TypeWebUI && typ != TypeGeneral {
		return fmt.Errorf("service type %q is not %s or %s", typ, TypeWebUI, TypeGeneral)
	}
	return nil
}

// Publish registers a service and serves it until the process dies. It writes
// the descriptor into dir, listens on the unix socket beside it, and pipes each
// accepted connection to 127.0.0.1:port inside the box. Running this IS the
// registration; the socket dying with the process is the deregistration.
func Publish(dir, name, typ string, port int) error {
	if err := CheckServiceName(name); err != nil {
		return err
	}
	if err := CheckServiceType(typ); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sock := filepath.Join(dir, SocketName(name))
	// A stale socket file from a box that died holding one refuses the bind; the
	// process that owned it is gone with its box, so the file is debris.
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer ln.Close()
	b, err := json.Marshal(Descriptor{Type: typ, Port: port})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, DescriptorName(name)), append(b, '\n'), 0o644); err != nil {
		return err
	}
	servePublished(ln, port)
	return nil
}

// servePublished accepts connections until the listener fails, giving each one
// its own connection to the box-local port.
func servePublished(ln net.Listener, port int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go dialAndCouple(conn, fmt.Sprintf("127.0.0.1:%d", port))
	}
}

// dialAndCouple opens one connection to addr and couples it to conn.
func dialAndCouple(conn net.Conn, addr string) {
	defer conn.Close()
	up, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer up.Close()
	Couple(conn, up)
}

// halfCloser is a connection that can close one direction while the other keeps
// flowing — what a tunnel needs to pass an EOF through without tearing the
// whole connection down.
type halfCloser interface {
	io.ReadWriter
	CloseWrite() error
}

// Couple pumps bytes both ways between two connections and returns once BOTH
// directions have finished. EOF on one side half-closes the other, so a client
// that shuts down its write side after the request still receives the whole
// response.
func Couple(a, b net.Conn) {
	ha, aok := a.(halfCloser)
	hb, bok := b.(halfCloser)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		if bok {
			_ = hb.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		if aok {
			_ = ha.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
