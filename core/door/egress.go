package door

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"
)

// The egress crossing: a proxy box's way out rides the same door as everything
// else that crosses its boundary. The box side (the in-box forwarder) opens one
// crossing per proxied connection — a fresh dial each time, never a stream on a
// shared connection, so what identifies a caller stays attached to the
// connection it made — and the relay dials the host proxy's socket for that
// crossing alone. Past the reply the crossing is raw bytes both ways; the relay
// reads nothing.

// VerbEgress opens a crossing carrying one proxied connection out of the box.
// No arguments: where the bytes go is the relay's, decided when the box was
// booted, never the box's to name.
const VerbEgress = "EGRESS"

// EgressHeader is the header a box opens an egress crossing with.
func EgressHeader() Header {
	return Header{Verb: VerbEgress}
}

// DialEgress opens an egress crossing on a box's door and returns it ready to
// carry bytes: dialed, declared, and answered OK. The returned connection's
// reads start with anything the reply read pulled off the wire, so no byte of
// the proxy's answer is lost.
func DialEgress(doorPath string) (net.Conn, error) {
	conn, err := net.Dial("unix", doorPath)
	if err != nil {
		return nil, err
	}
	if err := WriteHeader(conn, EgressHeader()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	if err := ReadReply(br); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return bufferConn(conn, br), nil
}

// egress serves one egress crossing: it dials the host proxy's socket for this
// crossing alone and couples the two. The dial is held like a carried one —
// the proxy restarting mid-crossing answers the crossing that caught it down —
// and a crossing that outwaits DialWait is closed by itself.
func (r *Relay) egress(conn net.Conn, br *bufio.Reader, h Header) {
	if len(h.Args) != 0 {
		_ = WriteReply(conn, fmt.Errorf("door: %s takes no arguments", VerbEgress))
		_ = conn.Close()
		return
	}
	if r.Egress == "" {
		_ = WriteReply(conn, errors.New("this box has no proxy egress — nothing stands behind this door's "+VerbEgress))
		_ = conn.Close()
		return
	}
	host, err := r.dialHeld(r.Egress)
	if err != nil {
		r.say("egress: %s did not answer for %s: %v", r.Egress, r.DialWait, err)
		_ = WriteBusy(conn, fmt.Errorf("door: the egress proxy did not answer"))
		_ = conn.Close()
		return
	}
	if err := WriteReply(conn, nil); err != nil {
		_ = host.Close()
		_ = conn.Close()
		return
	}
	// The crossing carries bytes now, at whatever pace the two ends work; a
	// deadline set for the header would cut a healthy transfer.
	_ = conn.SetDeadline(time.Time{})
	defer conn.Close()
	defer host.Close()
	Couple(bufferConn(conn, br), host)
}
