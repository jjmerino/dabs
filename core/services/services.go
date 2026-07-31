// Package services is the host half of box-published services: reading what a
// box published into its mounted services directory, holding a stable local
// port per service name, and serving each one on 127.0.0.1 so a browser or a
// client on the host can reach a program that only ever bound box-local
// loopback.
//
// A box registers by running the forwarder's `publish` mode, which writes a
// socket and a descriptor into the directory dabs bound into it. The directory
// is the whole registry: no control protocol, and a service is gone the moment
// its socket stops accepting.
package services

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// Service is one published service as the host sees it: the name it was
// published under, its type, the box-local port the socket leads to, the host
// path of that socket, and which box published it.
type Service struct {
	Name    string
	Type    string
	BoxPort int
	Socket  string // host path of the unix socket the box listens on
	// BridgePort is the port the publisher listens on across every interface of
	// the box, from the descriptor. Zero when the publisher opened no such door.
	BridgePort int
	// BoxAddr is an address the HOST can dial to reach the box's own network
	// namespace, supplied by the caller (which knows the driver). Empty when the
	// box has none, or the driver cannot say.
	BoxAddr  string
	Node     string // id of the node whose services directory holds it
	Instance string // the driver's name for that node's box
	// Conflict marks a service whose name another box claimed first. One name
	// means one host port, so only the first claimant is reachable; the rest are
	// reported (see MarkConflicts) rather than left silently unreachable behind a
	// row that claims they share an address.
	Conflict bool
}

// MarkConflicts flags every service whose name a box earlier in the list already
// claimed. The order decides, and it is the caller's stable one (nodes by id),
// so the listing and the server agree on which box owns a name without talking
// to each other.
func MarkConflicts(found []Service) []Service {
	claimed := map[string]bool{}
	out := make([]Service, 0, len(found))
	for _, s := range found {
		s.Conflict = claimed[s.Name]
		claimed[s.Name] = true
		out = append(out, s)
	}
	return out
}

// MaxCellLen caps how much of a box-written string the host prints.
const MaxCellLen = 64

// Printable is a box-written string reduced to what is safe to print: the box
// is the untrusted side, and a value carrying newlines or escape sequences would
// otherwise forge rows in the host's listing. Anything unprintable becomes a
// question mark, and the result is capped.
func Printable(s string) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		if i >= MaxCellLen {
			break
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// nameIsUsable reports whether a filename may be taken as a service name. The
// name is the key everything else hangs off — the port assignment, and which
// box owns it — so a name that would have to be shortened or rewritten to be
// printed is REFUSED rather than folded: a hostile box writing a long filename
// directly would otherwise land on a neighbour's printed name and take its
// ownership.
func nameIsUsable(name string) bool {
	return name != "" && len(name) <= forwarder.MaxServiceNameLen && Printable(name) == name
}

// ScanDir returns the services published into one box's services directory. A
// descriptor without its socket is a service whose publisher died; it is not
// reported, because nothing can be dialed. A name the host cannot print as
// written is not reported either — see nameIsUsable. An absent directory holds no
// services and is not an error — most boxes publish nothing.
func ScanDir(dir string) ([]Service, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Service
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name == e.Name() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var d forwarder.Descriptor
		if err := json.Unmarshal(b, &d); err != nil {
			continue
		}
		sock := filepath.Join(dir, forwarder.SocketName(name))
		if _, err := os.Stat(sock); err != nil {
			continue
		}
		if !nameIsUsable(name) {
			continue
		}
		// The descriptor's own fields are box-written too, and unlike the name they
		// key nothing — printable is enough.
		out = append(out, Service{Name: name, Type: Printable(d.Type), BoxPort: d.Port, BridgePort: d.Bridge, Socket: sock})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Route is where the host dials to reach one service.
type Route struct {
	Network string // "unix" or "tcp"
	Addr    string // socket path, or host:port
}

// dialTimeout is how long a reachability probe waits. Both candidates are on
// this machine or one hop into a local VM, so slow means unreachable.
const dialTimeout = 500 * time.Millisecond

// Route reports where to reach the service, and whether anything answers. The
// mounted socket is tried first: it is the direct door, and it is the only one
// where the host shares the box's kernel. Where it does not answer, the box's
// own address plus the publisher's outward port is the other way in — a box
// whose filesystem crosses a VM boundary hands the host a socket file it cannot
// dial, but its network is still reachable.
//
// A socket file left behind by a dead publisher refuses, which is the
// difference between a listed service and a reachable one.
func (s Service) Route() (Route, bool) {
	if dials("unix", s.Socket) {
		return Route{Network: "unix", Addr: s.Socket}, true
	}
	if s.BoxAddr != "" && s.BridgePort != 0 {
		addr := net.JoinHostPort(s.BoxAddr, strconv.Itoa(s.BridgePort))
		if dials("tcp", addr) {
			return Route{Network: "tcp", Addr: addr}, true
		}
	}
	return Route{}, false
}

// Up reports whether the service answers on any route.
func (s Service) Up() bool {
	_, ok := s.Route()
	return ok
}

// dials reports whether a connection can be opened.
func dials(network, addr string) bool {
	conn, err := net.DialTimeout(network, addr, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
