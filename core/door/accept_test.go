package door

// Contract tests for the one judgment the accept loop makes: whether an accept
// failure is a moment to wait out or the end of the door.

import (
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// timeoutError is an accept failure that reports itself as a deadline, the
// shape net.Error describes.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// CONTRACT: the relay carries on after a failure that describes a MOMENT, and
// stops only when the door itself is gone. Nothing restarts a relay, so getting
// this wrong either spins on a dead listener or takes the box's publishing away
// for the rest of its life over one bad instant.
func TestTheAcceptLoopSurvivesAMomentAndNotADeadDoor(t *testing.T) {
	for _, tc := range []struct {
		what string
		err  error
		keep bool
	}{
		{"a peer gone between connect and accept", syscall.ECONNABORTED, true},
		{"this process out of descriptors", syscall.EMFILE, true},
		{"the host out of descriptors", syscall.ENFILE, true},
		{"a signal", syscall.EINTR, true},
		{"a deadline", timeoutError{}, true},
		{"a wrapped moment", fmt.Errorf("accept unix /run/door.sock: %w", syscall.EMFILE), true},
		{"an accept error carrying one", &net.OpError{Op: "accept", Err: syscall.ECONNABORTED}, true},
		{"the listener closed", net.ErrClosed, false},
		{"a closed listener, wrapped", &net.OpError{Op: "accept", Err: net.ErrClosed}, false},
		{"the socket file gone", os.ErrNotExist, false},
		{"nothing at all", nil, false},
	} {
		if got := keepsAccepting(tc.err); got != tc.keep {
			t.Errorf("%s (%v): keepsAccepting = %v, want %v", tc.what, tc.err, got, tc.keep)
		}
	}
}

// CONTRACT: a run of accept failures is waited out with a GROWING pause and
// reported ONCE. The failures worth surviving (no descriptors) persist, so a
// flat retry would spin against the condition and write a line about it on
// every turn — filling the log of a host that is already out of something.
func TestARunOfAcceptFailuresBacksOffAndIsSaidOnce(t *testing.T) {
	dir := t.TempDir()
	log := &countingWriter{}
	r := NewRelay(dir, log)
	r.OpeningCap = MaxOpeningCrossings
	failing := &failingListener{err: syscall.EMFILE, until: time.Now().Add(300 * time.Millisecond)}
	r.ln = failing
	done := make(chan error, 1)
	go func() { done <- r.Serve() }()

	time.Sleep(400 * time.Millisecond)
	_ = failing.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve never returned after the listener closed")
	}
	if n := failing.attempts(); n == 0 || n > 30 {
		t.Errorf("accept was attempted %d times in 300ms, want a handful — it is backing off, not spinning", n)
	}
	if n := log.lines(); n != 1 {
		t.Errorf("the episode was reported %d times, want once", n)
	}
}

// countingWriter counts the lines written to it.
type countingWriter struct {
	mu sync.Mutex
	n  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	return len(p), nil
}

func (w *countingWriter) lines() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// failingListener fails every accept with err until its deadline, then blocks
// until it is closed — a listener that is out of descriptors and then is not.
type failingListener struct {
	err   error
	until time.Time

	mu     sync.Mutex
	n      int
	closed chan struct{}
	once   sync.Once
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
	if time.Now().Before(l.until) {
		return nil, l.err
	}
	<-l.gate()
	return nil, net.ErrClosed
}

func (l *failingListener) gate() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed == nil {
		l.closed = make(chan struct{})
	}
	return l.closed
}

func (l *failingListener) Close() error {
	gate := l.gate()
	l.once.Do(func() { close(gate) })
	return nil
}

func (l *failingListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

func (l *failingListener) attempts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}
