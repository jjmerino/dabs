package door

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// A carried socket is a recipe `socket:` as the box receives it. The box side
// of one is an arbitrary program dialing a plain path, so a carried crossing
// opens with no header and the relay reads nothing at all — it is the purest
// crossing the relay serves. What the relay adds is its lifetime: the listener
// the box's mount is established against is the relay's own, which lives
// exactly as long as the box's node, and the host program's socket is dialed
// fresh for every crossing. A host listener that restarts is reached again on
// the next dial, and a dial that lands while it is down fails alone — the
// mount itself cannot be finished by one refusal, because nothing about it is
// established once.

// Carry is one host socket the relay carries into a box: Listen is the
// dabs-owned socket the box's mount lands on, Dial is the host program's own
// socket, dialed per crossing.
type Carry struct {
	Listen string
	Dial   string
}

// DefaultDialWait is how long one carried crossing holds while the host path
// does not answer before failing that crossing. It rides out a host program's
// restart; a crossing that outwaits it is closed, and the next dial starts
// fresh.
const DefaultDialWait = 10 * time.Second

// How soon a carried crossing retries the host path, and the ceiling the pause
// doubles up to.
const (
	carryDialFirst  = 25 * time.Millisecond
	carryDialAtMost = 500 * time.Millisecond
)

// openCarries binds a listener for every carried socket. It runs while the
// door's claim is held, so a stale socket file at a listen path is debris of a
// relay that is gone, exactly as it is for the door itself.
func (r *Relay) openCarries() ([]net.Listener, error) {
	lns := make([]net.Listener, 0, len(r.Carries))
	for _, c := range r.Carries {
		_ = os.Remove(c.Listen)
		ln, err := net.Listen("unix", c.Listen)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return nil, fmt.Errorf("door: carry %s: %w", c.Listen, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

// serveCarry accepts carried crossings until the listener closes. The accept
// loop survives the same momentary failures the door's does, and for the same
// reason: nothing restarts a relay, so ending the loop on a moment would take
// the box's mount with it.
func (r *Relay) serveCarry(c Carry, ln net.Listener) {
	wait := time.Duration(0)
	for {
		conn, err := ln.Accept()
		if err != nil {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed || !keepsAccepting(err) {
				return
			}
			if wait == 0 {
				wait = acceptRetryFirst
				r.say("carry %s: accept: %v — still answering", c.Listen, err)
			} else {
				wait *= 2
				if wait > acceptRetryAtMost {
					wait = acceptRetryAtMost
				}
			}
			time.Sleep(wait)
			continue
		}
		wait = 0
		go r.carry(c, conn)
	}
}

// carry serves one carried crossing: it dials the host program's socket for
// this crossing alone and couples the two. A host path that does not answer is
// held for, up to DialWait — a listener mid-restart answers the same crossing
// that caught it down — and a crossing that outwaits it is closed by itself:
// the failure belongs to the dial that made it, never to the mount.
func (r *Relay) carry(c Carry, conn net.Conn) {
	defer conn.Close()
	host, err := r.dialHeld(c.Dial)
	if err != nil {
		r.say("carry %s: %s did not answer for %s: %v", c.Listen, c.Dial, r.DialWait, err)
		return
	}
	defer host.Close()
	forwarder.Couple(conn, host)
}

// dialHeld dials a host socket until it answers or DialWait runs out.
func (r *Relay) dialHeld(path string) (net.Conn, error) {
	deadline := time.Now().Add(r.DialWait)
	wait := carryDialFirst
	for {
		conn, err := net.DialTimeout("unix", path, r.DialWait)
		if err == nil {
			return conn, nil
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, err
		}
		time.Sleep(wait)
		if wait *= 2; wait > carryDialAtMost {
			wait = carryDialAtMost
		}
	}
}
