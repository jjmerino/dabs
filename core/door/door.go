// Package door is the box door: one dabs-owned unix socket per box, over which
// everything that crosses the box boundary travels. The HOST side listens (the
// relay), the box side dials, and every crossing is a connection of its own
// whose first line says what it is. Connections, not streams multiplexed onto
// one: a connection is what per-caller identity attaches to (SO_PEERCRED reads
// a connection), and what a pure byte relay can hand over without reading a
// byte past that first line.
//
// This file is the wire. A crossing opens with one header line
//
//	DABS-DOOR/1 PUBLISH <name> <type> <port>
//	DABS-DOOR/1 STREAM <id>
//
// which the relay answers with one reply line
//
//	DABS-DOOR/1 OK
//	DABS-DOOR/1 ERR <reason>
//
// after which the crossing is whatever its verb makes it: a PUBLISH crossing is
// held open and carries line messages (PING/PONG, and the relay's STREAM
// requests); a STREAM crossing carries raw bytes and nothing reads them again.
//
// The banner carries the version, and it is on EVERY line that opens or answers
// a crossing — the two sides are separately built (dabs on the host, the
// forwarder in the box, from whatever image the recipe named), so each has to
// be able to tell an older or newer peer from a broken one.
package door

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// BoxPath is where a box that was granted a door finds it. It is dabs's own
	// box-path namespace, fixed by contract: the box side is a binary from the
	// image, which has nothing to read a configured path out of.
	BoxPath = "/run/dabs/door.sock"

	// Banner opens every header and every reply. The number is the protocol
	// version: a change that an older peer would misread takes a new one.
	Banner = "DABS-DOOR/1"

	// MaxLine caps a header, reply or control line. Every line the protocol
	// defines is a few tens of bytes; the cap is what keeps a peer from making
	// the other side buffer without end before it has said anything.
	MaxLine = 512
)

// The verbs a crossing may open with.
const (
	// VerbPublish opens the crossing that IS a published service: the box holds
	// it, and the relay asks for streams over it. Args: name, type, box port.
	VerbPublish = "PUBLISH"
	// VerbStream opens one carrying crossing, claiming the id the relay asked
	// for on a held PUBLISH crossing. Args: id.
	VerbStream = "STREAM"
)

// The control messages a held PUBLISH crossing carries, one per line. PING is
// the relay's liveness question and PONG the box's answer: a connection that is
// merely OPEN proves nothing about the peer's health, so the crossing is judged
// by an answer that had to be produced.
const (
	MsgPing = "PING"
	MsgPong = "PONG"
	// MsgStream asks the box to open a carrying crossing for the id that
	// follows: `STREAM <id>`.
	MsgStream = "STREAM"
)

// ErrClosed is what reading a line reports when the peer went away.
var ErrClosed = errors.New("door: the crossing closed")

// RefusedError is the DOOR'S OWN answer to a crossing it will not take (a name
// already published in this box, a service type it does not know): an ERR reply
// that was read off the wire. It is the one failure retrying cannot change, and
// it is deliberately distinct from every failure that CAN — a dial that
// connects and then goes quiet, a door at its limit — because treating the two
// alike is what turns one bad moment into a box that can never publish again.
type RefusedError struct{ Reason string }

func (e RefusedError) Error() string { return e.Reason }

// BusyError is the door saying "not now": it is at a limit that depends on how
// much is going on, and how much is going on changes. It is a reply of its own
// rather than an ERR precisely so the box side can tell it from a decision and
// come back — a service that exists is not un-published by a busy moment.
type BusyError struct{ Reason string }

func (e BusyError) Error() string { return e.Reason }

// Header is a crossing's opening line: what this connection is, and the
// arguments the verb takes.
type Header struct {
	Verb string
	Args []string
}

// PublishHeader is the header a box opens a publishing crossing with.
func PublishHeader(name, typ string, port int) Header {
	return Header{Verb: VerbPublish, Args: []string{name, typ, strconv.Itoa(port)}}
}

// StreamHeader is the header a box opens a carrying crossing with.
func StreamHeader(id uint64) Header {
	return Header{Verb: VerbStream, Args: []string{strconv.FormatUint(id, 10)}}
}

// Line renders the header as it goes on the wire, without the newline.
func (h Header) Line() string {
	return strings.Join(append([]string{Banner, h.Verb}, h.Args...), " ")
}

// Publication reads a PUBLISH header's arguments.
func (h Header) Publication() (name, typ string, port int, err error) {
	if len(h.Args) != 3 {
		return "", "", 0, fmt.Errorf("door: %s takes a name, a type and a port", VerbPublish)
	}
	port, err = strconv.Atoi(h.Args[2])
	if err != nil || port <= 0 || port > 65535 {
		return "", "", 0, fmt.Errorf("door: %q is not a port", h.Args[2])
	}
	if err := CheckServiceName(h.Args[0]); err != nil {
		return "", "", 0, err
	}
	if err := CheckServiceType(h.Args[1]); err != nil {
		return "", "", 0, err
	}
	return h.Args[0], h.Args[1], port, nil
}

// StreamID reads a STREAM header's argument.
func (h Header) StreamID() (uint64, error) {
	if len(h.Args) != 1 {
		return 0, fmt.Errorf("door: %s takes one id", VerbStream)
	}
	id, err := strconv.ParseUint(h.Args[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("door: %q is not a stream id", h.Args[0])
	}
	return id, nil
}

// WriteHeader sends a crossing's opening line.
func WriteHeader(w io.Writer, h Header) error {
	return WriteLine(w, h.Line())
}

// ReadHeader reads a crossing's opening line. A line that does not carry this
// protocol's banner is refused by name rather than parsed as far as it goes:
// whatever dialed, it is not speaking to this door.
func ReadHeader(r *bufio.Reader) (Header, error) {
	line, err := ReadLine(r)
	if err != nil {
		return Header{}, err
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != Banner {
		return Header{}, fmt.Errorf("door: %q does not open with %s", clip(line), Banner)
	}
	return Header{Verb: fields[1], Args: fields[2:]}, nil
}

// WriteReply answers a header: OK when reason is nil, else ERR and why. The
// reason travels on one line, so its newlines are flattened — a message that
// forged a second line would be read as the next thing the peer said.
func WriteReply(w io.Writer, reason error) error {
	if reason == nil {
		return WriteLine(w, Banner+" OK")
	}
	return WriteLine(w, Banner+" ERR "+flattenLine(reason.Error()))
}

// WriteBusy answers a header with "not now, and it is worth asking again". It
// is what a load limit says, so a crossing that hit one is never mistaken for
// one the door decided against.
func WriteBusy(w io.Writer, reason error) error {
	return WriteLine(w, Banner+" BUSY "+flattenLine(reason.Error()))
}

// ReadReply reads the answer to a header: nil when the crossing was accepted, a
// RefusedError when the door decided against it, a BusyError when the door is
// at a limit and the same request may work later, or a plain error when the
// reply could not be read or was not this protocol's.
func ReadReply(r *bufio.Reader) error {
	line, err := ReadLine(r)
	if err != nil {
		return err
	}
	rest, ok := strings.CutPrefix(line, Banner+" ")
	if !ok {
		return fmt.Errorf("door: %q does not open with %s", clip(line), Banner)
	}
	switch {
	case rest == "OK":
		return nil
	case strings.HasPrefix(rest, "ERR "):
		return RefusedError{Reason: strings.TrimPrefix(rest, "ERR ")}
	case strings.HasPrefix(rest, "BUSY "):
		return BusyError{Reason: strings.TrimPrefix(rest, "BUSY ")}
	}
	return fmt.Errorf("door: %q is not OK, ERR or BUSY", clip(rest))
}

// WriteLine sends one protocol line.
func WriteLine(w io.Writer, s string) error {
	_, err := io.WriteString(w, s+"\n")
	return err
}

// ReadLine reads one protocol line, without its newline, refusing one longer
// than MaxLine. It reads byte by byte on purpose: the reader is shared with the
// raw bytes that follow a header, and buffering past the newline would swallow
// the first bytes of the payload.
func ReadLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", ErrClosed
			}
			return "", err
		}
		if c == '\n' {
			return strings.TrimSuffix(b.String(), "\r"), nil
		}
		if b.Len() >= MaxLine {
			return "", fmt.Errorf("door: a line ran past %d bytes", MaxLine)
		}
		b.WriteByte(c)
	}
}

// flattenLine flattens a message to a single wire line.
func flattenLine(s string) string {
	return clip(strings.NewReplacer("\n", " ", "\r", " ").Replace(s))
}

// clip shortens a peer-written string to what fits on one line, so a refusal
// quoting it stays a line. It cuts on a RUNE boundary: what a peer wrote is
// arbitrary bytes, and cutting mid-rune would put a broken one in the message.
func clip(s string) string {
	if len(s) <= clipAt {
		return s
	}
	cut := 0
	for i := range s {
		if i > clipAt {
			break
		}
		cut = i
	}
	return s[:cut] + "…"
}

// clipAt is how much of a peer-written string a message quotes.
const clipAt = 120
