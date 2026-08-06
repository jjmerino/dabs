package sandbox

import (
	"fmt"
	"sync"
)

// Lazy wraps a driver constructor so the driver is built — and its probe run
// (a LookPath for the vendor CLI) — on FIRST USE, not at process start. A
// command that never touches the driver (listing recipes, printing a node's
// directory, cutting a boxless place) therefore runs on a machine where the
// constructor would fail, and a command that does need the driver gets the
// constructor's own error (the install hint) from whichever method it calls.
// kind is answered statically: a driver's identity is known before it exists.
func Lazy(kind string, build func() (Driver, error)) Driver {
	return &lazy{kind: kind, build: build}
}

// The wrapper stands in front of the driver, so a caller type-asserts THIS type
// for a capability: every optional one must be forwarded, or a driver that has
// it is reported as not having it. Capable is the one list; this line is what
// makes forgetting a forward a compile error.
var _ Capable = (*lazy)(nil)

type lazy struct {
	kind  string
	build func() (Driver, error)
	once  sync.Once
	drv   Driver
	err   error
}

// get builds the driver exactly once; every call after the first sees the same
// driver or the same construction error.
func (l *lazy) get() (Driver, error) {
	l.once.Do(func() { l.drv, l.err = l.build() })
	return l.drv, l.err
}

func (l *lazy) Kind() string { return l.kind }

func (l *lazy) Build(spec BuildSpec) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	return d.Build(spec)
}

func (l *lazy) HasImage(name string) (bool, error) {
	d, err := l.get()
	if err != nil {
		return false, err
	}
	return d.HasImage(name)
}

func (l *lazy) Up(spec Spec) (string, error) {
	d, err := l.get()
	if err != nil {
		return "", err
	}
	return d.Up(spec)
}

func (l *lazy) Run(instance string, cmd []string) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	return d.Run(instance, cmd)
}

func (l *lazy) Exec(instance string, cmd []string) (string, error) {
	d, err := l.get()
	if err != nil {
		return "", err
	}
	return d.Exec(instance, cmd)
}

func (l *lazy) Down(instance string) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	return d.Down(instance)
}

func (l *lazy) Ls() ([]Info, error) {
	d, err := l.get()
	if err != nil {
		return nil, err
	}
	return d.Ls()
}

// CheckEgress builds the driver and forwards the capability. A built driver
// without the EgressEnforcer capability enforces only open, so any restricted
// mode is refused with the mechanical reason.
func (l *lazy) CheckEgress(mode string) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	if ee, ok := d.(EgressEnforcer); ok {
		return ee.CheckEgress(mode)
	}
	return fmt.Errorf("driver %s cannot enforce egress mode %q", l.kind, mode)
}

// Images builds the driver and forwards its image store. An inner driver
// without one holds nothing to list — the same skip a caller applies to a
// driver that does not implement ImageStore.
func (l *lazy) Images() ([]Image, error) {
	d, err := l.get()
	if err != nil {
		return nil, err
	}
	if s, ok := d.(ImageStore); ok {
		return s.Images()
	}
	return nil, nil
}

// CheckDetach builds the driver and forwards the capability. An inner driver
// with no answer of its own cannot hold a detached command; the refusal names
// this driver's kind and claims no cause it cannot know.
func (l *lazy) CheckDetach() error {
	d, err := l.get()
	if err != nil {
		return err
	}
	if dt, ok := d.(Detacher); ok {
		return dt.CheckDetach()
	}
	return fmt.Errorf("the %s driver cannot hold a detached command", l.kind)
}

// Detach builds the driver and forwards the start. Callers ask CheckDetach
// first, so an inner driver with no answer only reaches here out of order — it
// gets the same refusal rather than a nil error and a command nobody started.
func (l *lazy) Detach(instance string, cmd []string) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	if dt, ok := d.(Detacher); ok {
		return dt.Detach(instance, cmd)
	}
	return fmt.Errorf("the %s driver cannot hold a detached command", l.kind)
}

// RemoveImage forwards to the inner driver's image store; with none there is
// nothing to remove.
func (l *lazy) RemoveImage(name string) error {
	d, err := l.get()
	if err != nil {
		return err
	}
	if s, ok := d.(ImageStore); ok {
		return s.RemoveImage(name)
	}
	return nil
}
