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
	"strings"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// Service is one published service as the host sees it: the name it was
// published under, its type, the box-local port the socket leads to, the host
// path of that socket, and which box published it.
type Service struct {
	Name     string
	Type     string
	BoxPort  int
	Socket   string // host path of the unix socket the box listens on
	Node     string // id of the node whose services directory holds it
	Instance string // the driver's name for that node's box
}

// ScanDir returns the services published into one box's services directory. A
// descriptor without its socket is a service whose publisher died; it is not
// reported, because nothing can be dialed. An absent directory holds no
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
		out = append(out, Service{Name: name, Type: d.Type, BoxPort: d.Port, Socket: sock})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Up reports whether the service answers: its socket accepts a connection. A
// socket file left behind by a dead publisher refuses, which is the difference
// between a listed service and a reachable one.
func (s Service) Up() bool {
	conn, err := net.DialTimeout("unix", s.Socket, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
