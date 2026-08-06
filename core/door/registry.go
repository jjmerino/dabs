package door

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// The registry is a directory on the HOST holding, per published service, a
// descriptor file and the socket the relay listens on for it. Both files are
// the relay's — a box writes neither. Plain files cross every driver's
// filesystem boundary, so `dabs services` reads the registry with no process to
// talk to, and a socket the host itself created is one the host can dial.

// Service types. The type travels with a service so a host-side index can
// render a browsable address as a link; it never changes how bytes are routed.
const (
	TypeWebUI   = "webui"
	TypeGeneral = "general"
)

// Descriptor is the JSON written beside a service's socket, at
// <dir>/<name>.json: what the service is, and which box-local loopback port the
// crossings lead to.
type Descriptor struct {
	Type string `json:"type"`
	Port int    `json:"port"`
}

// DescriptorName is the descriptor file a service of the given name gets.
func DescriptorName(name string) string { return name + ".json" }

// SocketName is the socket file the relay listens on for a service.
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

// WriteDescriptor writes a service's descriptor by rename, so a host scanning
// the directory reads either the whole file or no file — never the truncated
// middle of one.
func WriteDescriptor(dir, name, typ string, port int) error {
	b, err := json.Marshal(Descriptor{Type: typ, Port: port})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	clean := func(err error) error {
		_ = os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return clean(err)
	}
	if err := tmp.Close(); err != nil {
		return clean(err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return clean(err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, DescriptorName(name))); err != nil {
		return clean(err)
	}
	return nil
}

// RemoveDescriptor takes a service's descriptor out of the registry, which is
// what un-publishes it: what the host scans is the file.
func RemoveDescriptor(dir, name string) error {
	err := os.Remove(filepath.Join(dir, DescriptorName(name)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
