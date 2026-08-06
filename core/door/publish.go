package door

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// The box side of a published service. It dials the door and holds ONE crossing
// open for as long as the service is published; every client the host puts in
// front of that service arrives as a separate crossing, dialed on demand. The
// box is the dialing side on every driver, because the one mechanism that
// carries a socket into a box on all of them relays a host listener inward.

// Defaults for a publisher's timings, which are fields on Publisher so a test
// can drive the same code on a scale it can wait for.
const (
	// DefaultRedial is how long the publisher keeps trying a door that does not
	// answer before giving up and saying so. A dial that fails is a moment, not
	// a verdict: it must never leave the box unable to publish for the rest of
	// its life.
	DefaultRedial = 30 * time.Second
	// DefaultDialEvery is the first pause between attempts; it doubles up to
	// DefaultDialAtMost.
	DefaultDialEvery  = 100 * time.Millisecond
	DefaultDialAtMost = 2 * time.Second
	// DefaultPublisherIdle is how long the publisher waits for anything on the
	// held crossing before judging the relay gone. The relay pings well inside
	// it, so silence past this is silence, not calm.
	DefaultPublisherIdle = 20 * time.Second
)

// NotGrantedError is what a box that may not publish gets when it tries: the
// door is simply not there. It is a refusal by NAME, not a missing file — a box
// told "no" must be able to say so, and a bare ENOENT reads as a broken box.
type NotGrantedError struct{ Path string }

func (e NotGrantedError) Error() string {
	return fmt.Sprintf("this box was not granted service publishing: there is no door at %s — the recipe that boots the box must say `publish: true`", e.Path)
}

// Publisher publishes one box-local port on the box's door.
type Publisher struct {
	// Door is the box path of the door socket.
	Door string
	// Idle, Redial, DialEvery and DialAtMost are the timings above.
	Idle, Redial, DialEvery, DialAtMost time.Duration
	Log                                 io.Writer
}

// NewPublisher returns a publisher for the box's own door.
func NewPublisher(doorPath string, log io.Writer) Publisher {
	return Publisher{
		Door: doorPath, Idle: DefaultPublisherIdle, Redial: DefaultRedial,
		DialEvery: DefaultDialEvery, DialAtMost: DefaultDialAtMost, Log: log,
	}
}

// Publish holds a service open on the box door until the process dies. Running
// this IS the registration, and the crossing closing IS the deregistration: the
// relay takes the service out of the registry the moment this stops answering.
//
// A door that does not answer, or that answers BUSY, is retried: a host-side
// listener a moment late and a door at a load limit are both moments, and a
// moment must not cost the box its whole life. Only two things end this — a
// door that is NOT THERE (that box was never granted publishing, and retrying
// grants nothing) and the door's own ERR (a decision about this publication,
// which asking again cannot change).
func (p Publisher) Publish(name, typ string, port int) error {
	if err := CheckServiceName(name); err != nil {
		return err
	}
	if err := CheckServiceType(typ); err != nil {
		return err
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("%d is not a port", port)
	}
	if _, err := os.Stat(p.Door); err != nil {
		if os.IsNotExist(err) {
			return NotGrantedError{Path: p.Door}
		}
		return err
	}
	wait := p.DialEvery
	answered := time.Now()
	busy := false
	for {
		held, err := p.session(name, typ, port)
		var refused RefusedError
		if errors.As(err, &refused) {
			return err
		}
		var wasBusy BusyError
		busy = errors.As(err, &wasBusy)
		if held {
			answered = time.Now()
			wait = p.DialEvery
		}
		if time.Since(answered) > p.Redial {
			// A door that was BUSY the whole time ANSWERED, every time; saying it
			// never answered would send whoever reads this looking for a relay that
			// is dead, when what it is is full.
			if busy {
				return fmt.Errorf("the box door at %s was busy for %s: %w", p.Door, p.Redial, err)
			}
			return fmt.Errorf("the box door at %s has not answered for %s: %w", p.Door, p.Redial, err)
		}
		p.say("%s: %v — dialing the door again in %s", name, err, wait)
		time.Sleep(wait)
		if wait *= 2; wait > p.DialAtMost {
			wait = p.DialAtMost
		}
	}
}

// session dials the door, publishes the service, and holds the crossing until
// it ends. held reports whether the crossing was ever established, so a caller
// can tell "the door never answered" from "the door answered and then went".
func (p Publisher) session(name, typ string, port int) (held bool, err error) {
	conn, err := net.Dial("unix", p.Door)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if err := conn.SetDeadline(time.Now().Add(p.Idle)); err != nil {
		return false, err
	}
	if err := WriteHeader(conn, PublishHeader(name, typ, port)); err != nil {
		return false, err
	}
	if err := ReadReply(br); err != nil {
		// A RefusedError is the relay's own decision about this publication and
		// travels up as one; anything else is the transport, and the transport is
		// retried.
		return false, err
	}
	p.say("%s: published on %s (box port %d)", name, p.Door, port)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(p.Idle)); err != nil {
			return true, err
		}
		line, err := ReadLine(br)
		if err != nil {
			return true, err
		}
		switch {
		case line == MsgPing:
			if err := conn.SetWriteDeadline(time.Now().Add(p.Idle)); err != nil {
				return true, err
			}
			if err := WriteLine(conn, MsgPong); err != nil {
				return true, err
			}
		case strings.HasPrefix(line, MsgStream+" "):
			id := strings.TrimPrefix(line, MsgStream+" ")
			go p.carry(id, port)
		default:
			return true, fmt.Errorf("the door said %q, which this box does not understand", clip(line))
		}
	}
}

// carry opens one crossing for the stream the relay asked for and couples it to
// the box-local port. Each client gets its own connection to the door and its
// own connection to the service — nothing is multiplexed, so one client's
// trouble is one client's trouble.
func (p Publisher) carry(id string, port int) {
	conn, err := net.Dial("unix", p.Door)
	if err != nil {
		p.say("stream %s: %v", id, err)
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if err := conn.SetDeadline(time.Now().Add(p.Idle)); err != nil {
		return
	}
	if err := WriteHeader(conn, Header{Verb: VerbStream, Args: []string{id}}); err != nil {
		p.say("stream %s: %v", id, err)
		return
	}
	if err := ReadReply(br); err != nil {
		p.say("stream %s: %v", id, err)
		return
	}
	// Bytes now, at whatever pace the two ends work.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	up, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		p.say("stream %s: 127.0.0.1:%d: %v", id, port, err)
		return
	}
	defer up.Close()
	forwarder.Couple(bufferConn(conn, br), up)
}

// say reports one line about what the publisher is doing.
func (p Publisher) say(format string, args ...any) {
	if p.Log == nil {
		return
	}
	fmt.Fprintf(p.Log, format+"\n", args...)
}
