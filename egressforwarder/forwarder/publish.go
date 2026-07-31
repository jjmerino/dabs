package forwarder

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
)

// BridgeEnv is the environment variable dabs sets in a box whose services the
// host can only reach over the network. Its presence (a "1" or "true") is the
// default for `forward publish --bridge`: the forwarder cannot know what kind
// of box it is in, and the side that does — dabs, which chose the driver — says
// so here.
const BridgeEnv = "DABS_SERVICE_BRIDGE"

// BridgeWanted reports whether the environment asks for the outward door.
func BridgeWanted(value string) bool { return value == "1" || value == "true" }

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
// <ServicesDir>/<name>.json. It states what the service is, which box-local
// loopback port both doors lead to, and the port of the box's OUTWARD-facing
// door — the one a host reaches when it cannot dial the socket.
type Descriptor struct {
	Type string `json:"type"`
	Port int    `json:"port"`
	// Bridge is the port the publisher listens on across ALL the box's
	// interfaces, and is absent unless that door was asked for. A box whose
	// network namespace has a host-dialable address of its own is reached there;
	// the service's own port stays loopback-private, so the publisher remains the
	// only door into it.
	Bridge int `json:"bridge,omitempty"`
}

// DescriptorName is the descriptor file a service of the given name writes.
func DescriptorName(name string) string { return name + ".json" }

// SocketName is the socket file a service of the given name listens on.
func SocketName(name string) string { return name + ".sock" }

// MaxServiceNameLen is the longest a service name may be.
const MaxServiceNameLen = 64

// serviceName is what a service may be called: lowercase letters, digits, dot,
// dash and underscore. The name is a filename AND a cell a host prints, and the
// box that chooses it is the untrusted side — an allowlist is the only rule that
// keeps a control byte or an escape sequence out of both.
var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// CheckServiceName reports whether name may be a service's name.
func CheckServiceName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("service name is empty")
	case len(name) > MaxServiceNameLen:
		return fmt.Errorf("service name is %d bytes, over the %d allowed", len(name), MaxServiceNameLen)
	case !serviceName.MatchString(name):
		return fmt.Errorf("service name %q is not [a-z0-9._-], starting with a letter or digit", name)
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
//
// bridge additionally opens a listener across every interface of the box, for a
// host that cannot dial the socket. It is off unless asked for: in a box that
// shares the host's network namespace, that listener would stand on the host's
// own interfaces, and the socket is the way in there anyway.
func Publish(dir, name, typ string, port int, bridge bool) error {
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
	// Closing the listener unlinks the socket it created, so a descriptor that
	// cannot be written leaves no socket behind describing nothing.
	defer ln.Close()
	// The second door: a socket is only dialable by a host that shares the box's
	// kernel. Where it does not, the box's own network address is what the host
	// has, and only a listener bound across every interface answers there.
	bridgePort := 0
	if bridge {
		out, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			return err
		}
		defer out.Close()
		bridgePort = out.Addr().(*net.TCPAddr).Port
		go servePublished(out, port)
	}
	if err := writeDescriptor(dir, name, typ, port, bridgePort); err != nil {
		return err
	}
	servePublished(ln, port)
	return nil
}

// writeDescriptor writes the service's descriptor by rename, so a host scanning
// the directory reads either the whole file or no file — never the truncated
// middle of one.
func writeDescriptor(dir, name, typ string, port, bridge int) error {
	b, err := json.Marshal(Descriptor{Type: typ, Port: port, Bridge: bridge})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, DescriptorName(name))); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
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
