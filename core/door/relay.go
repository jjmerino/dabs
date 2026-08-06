package door

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// The relay is the HOST side of one box's door: it owns the listening socket
// the box dials, and it is a pure byte relay — it reads each crossing's opening
// line, and nothing after it. One relay serves one box, for as long as that
// box's node lives, so the path a box dials is answered from the moment the box
// exists: a box never dials into a gap.

// Default timings of a relay. They are fields rather than constants so a test
// can drive the same code on a scale it can wait for.
const (
	// DefaultPingEvery is how often the relay asks a held crossing to prove it
	// is alive.
	DefaultPingEvery = 5 * time.Second
	// DefaultIdle is how long the relay waits for anything at all on a held
	// crossing before judging it dead. An open connection is not evidence: the
	// box may be talking to a socket whose acceptor is gone, and a read that
	// never answers looks exactly like a healthy quiet service.
	DefaultIdle = 20 * time.Second
	// DefaultHeaderWait is how long a fresh crossing has to say what it is.
	DefaultHeaderWait = 10 * time.Second
	// DefaultStreamWait is how long a parked client connection waits for the box
	// to open the crossing that carries it.
	DefaultStreamWait = 15 * time.Second
)

// Relay serves one box's door: it accepts crossings on the door socket, holds
// each published service's crossing open, and stands a host listener in front
// of every service so anything on the host can dial it as a plain unix socket.
type Relay struct {
	// PingEvery, Idle, HeaderWait and StreamWait are the timings above.
	PingEvery, Idle, HeaderWait, StreamWait time.Duration

	dir string // the registry directory, on the host
	log io.Writer

	mu        sync.Mutex
	ln        net.Listener
	closed    bool
	published map[string]*publication
	pending   map[uint64]net.Conn
	next      uint64
}

// NewRelay returns a relay that publishes into dir and reports what it does to
// log.
func NewRelay(dir string, log io.Writer) *Relay {
	return &Relay{
		PingEvery: DefaultPingEvery, Idle: DefaultIdle,
		HeaderWait: DefaultHeaderWait, StreamWait: DefaultStreamWait,
		dir: dir, log: log,
		published: map[string]*publication{}, pending: map[uint64]net.Conn{},
	}
}

// Run opens the door at doorPath and serves it until the relay is closed or the
// listener fails. It is the whole relay process.
func Run(doorPath, dir string, log io.Writer) error {
	r := NewRelay(dir, log)
	if err := r.Open(doorPath); err != nil {
		return err
	}
	return r.Serve()
}

// Open binds the door socket. It returns once the path is listening, so a
// caller that boots a box next knows the box's door answers from its first
// instant — the box side never has to dial a path nothing holds.
func (r *Relay) Open(doorPath string) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(doorPath), 0o700); err != nil {
		return err
	}
	// A socket file left by a relay whose process is gone refuses the bind. Its
	// owner died with the node's previous relay, so the file is debris.
	_ = os.Remove(doorPath)
	ln, err := net.Listen("unix", doorPath)
	if err != nil {
		return fmt.Errorf("door: open %s: %w", doorPath, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ln = ln
	return nil
}

// Serve accepts crossings until the relay is closed or the listener fails.
func (r *Relay) Serve() error {
	r.mu.Lock()
	ln := r.ln
	r.mu.Unlock()
	if ln == nil {
		return errors.New("door: the relay was never opened")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		go r.cross(conn)
	}
}

// Close stops the relay and every service it publishes.
func (r *Relay) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	ln := r.ln
	pubs := make([]*publication, 0, len(r.published))
	for _, p := range r.published {
		pubs = append(pubs, p)
	}
	parked := make([]net.Conn, 0, len(r.pending))
	for id, c := range r.pending {
		parked = append(parked, c)
		delete(r.pending, id)
	}
	r.mu.Unlock()
	for _, p := range pubs {
		p.close()
	}
	for _, c := range parked {
		_ = c.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// cross reads one crossing's opening line and does what the line says.
func (r *Relay) cross(conn net.Conn) {
	br := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(r.HeaderWait))
	h, err := ReadHeader(br)
	if err != nil {
		r.say("crossing refused: %v", err)
		_ = conn.Close()
		return
	}
	switch h.Verb {
	case VerbPublish:
		r.publish(conn, br, h)
	case VerbStream:
		r.stream(conn, br, h)
	default:
		_ = WriteReply(conn, fmt.Errorf("door: %q is not a verb this door knows", h.Verb))
		_ = conn.Close()
	}
}

// publish takes a box's publishing crossing: it stands a host listener in front
// of the service, writes its descriptor, and holds the crossing open until
// either side stops answering.
func (r *Relay) publish(conn net.Conn, br *bufio.Reader, h Header) {
	name, typ, port, err := h.Publication()
	if err != nil {
		_ = WriteReply(conn, err)
		_ = conn.Close()
		return
	}
	p := &publication{relay: r, name: name, typ: typ, port: port, conn: conn, br: br, done: make(chan struct{})}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = WriteReply(conn, errors.New("door: the relay is closing"))
		_ = conn.Close()
		return
	}
	if _, taken := r.published[name]; taken {
		r.mu.Unlock()
		// One name is one listener in one box. The host resolves a name claimed by
		// TWO boxes as a conflict; inside one box the second claimant is simply
		// wrong, and is told so instead of being left publishing into nothing.
		_ = WriteReply(conn, fmt.Errorf("door: %q is already published in this box", name))
		_ = conn.Close()
		return
	}
	r.published[name] = p
	r.mu.Unlock()
	if err := p.open(); err != nil {
		r.drop(name, p)
		_ = WriteReply(conn, err)
		_ = conn.Close()
		return
	}
	if err := WriteReply(conn, nil); err != nil {
		p.close()
		return
	}
	r.say("%s: published (%s, box port %d)", name, typ, port)
	p.hold()
}

// stream hands a parked client connection to the crossing the box opened for
// it, and gets out of the way: from here the two are one byte pipe, and nothing
// reads what flows through.
func (r *Relay) stream(conn net.Conn, br *bufio.Reader, h Header) {
	id, err := h.StreamID()
	if err != nil {
		_ = WriteReply(conn, err)
		_ = conn.Close()
		return
	}
	client := r.claim(id)
	if client == nil {
		_ = WriteReply(conn, fmt.Errorf("door: nothing is waiting for stream %d", id))
		_ = conn.Close()
		return
	}
	if err := WriteReply(conn, nil); err != nil {
		_ = conn.Close()
		_ = client.Close()
		return
	}
	// The crossing carries bytes now, at whatever pace the two ends work; a
	// deadline set for the header would cut a healthy transfer.
	_ = conn.SetDeadline(time.Time{})
	defer conn.Close()
	defer client.Close()
	forwarder.Couple(client, buffered(conn, br))
}

// park holds a client connection until the box opens a crossing for it, and
// returns the id the box is asked to claim it with.
func (r *Relay) park(c net.Conn) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := r.next
	r.pending[id] = c
	return id
}

// claim takes a parked connection, or nil when nothing is parked under that id.
func (r *Relay) claim(id uint64) net.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.pending[id]
	if !ok {
		return nil
	}
	delete(r.pending, id)
	return c
}

// drop forgets a publication, if it is still the one registered under its name.
func (r *Relay) drop(name string, p *publication) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.published[name] == p {
		delete(r.published, name)
	}
}

// say reports one line of what the relay did.
func (r *Relay) say(format string, args ...interface{}) {
	if r.log == nil {
		return
	}
	fmt.Fprintf(r.log, format+"\n", args...)
}

// publication is one service the box published: the crossing the box holds, the
// host listener standing in front of it, and the descriptor that makes it
// visible to `dabs services`. All three appear together and go together — the
// registry never describes a service nothing answers for.
type publication struct {
	relay *Relay
	name  string
	typ   string
	port  int

	conn net.Conn
	br   *bufio.Reader
	ln   net.Listener

	wmu  sync.Mutex // one writer at a time on the held crossing
	once sync.Once
	done chan struct{}
}

// open stands the host listener up and writes the descriptor beside it.
func (p *publication) open() error {
	sock := filepath.Join(p.relay.dir, SocketName(p.name))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("door: listen for %q: %w", p.name, err)
	}
	if err := WriteDescriptor(p.relay.dir, p.name, p.typ, p.port); err != nil {
		_ = ln.Close()
		return fmt.Errorf("door: describe %q: %w", p.name, err)
	}
	p.ln = ln
	go p.accept()
	return nil
}

// hold keeps the crossing alive: it asks for a heartbeat on a schedule and
// reads the answers, and closes the publication the moment either stops.
func (p *publication) hold() {
	defer p.close()
	go p.ping()
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(p.relay.Idle))
		line, err := ReadLine(p.br)
		if err != nil {
			p.relay.say("%s: crossing lost: %v", p.name, err)
			return
		}
		if line != MsgPong {
			p.relay.say("%s: crossing said %q, which is not %s", p.name, clip(line), MsgPong)
			return
		}
	}
}

// ping asks the box to answer, on a schedule, until the publication ends.
func (p *publication) ping() {
	tick := time.NewTicker(p.relay.PingEvery)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-tick.C:
			if err := p.send(MsgPing); err != nil {
				p.close()
				return
			}
		}
	}
}

// accept forwards every host-side connection into a crossing of its own.
func (p *publication) accept() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.carry(conn)
	}
}

// carry asks the box for a crossing to put one client connection on, and parks
// the client until the box opens it. A box that never does leaves a parked
// connection, so the wait is bounded: the client is closed rather than held for
// ever by a publisher that stopped answering.
func (p *publication) carry(client net.Conn) {
	id := p.relay.park(client)
	if err := p.send(fmt.Sprintf("%s %d", MsgStream, id)); err != nil {
		if c := p.relay.claim(id); c != nil {
			_ = c.Close()
		}
		p.close()
		return
	}
	time.AfterFunc(p.relay.StreamWait, func() {
		if c := p.relay.claim(id); c != nil {
			p.relay.say("%s: the box did not open stream %d", p.name, id)
			_ = c.Close()
		}
	})
}

// send writes one control line to the box, one writer at a time.
func (p *publication) send(line string) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := p.conn.SetWriteDeadline(time.Now().Add(p.relay.Idle)); err != nil {
		return err
	}
	return WriteLine(p.conn, line)
}

// close takes the whole publication down: the listener (which unlinks its
// socket), the descriptor, and the crossing itself. What the host scans and
// what the host can dial disappear together.
func (p *publication) close() {
	p.once.Do(func() {
		close(p.done)
		if p.ln != nil {
			_ = p.ln.Close()
		}
		_ = RemoveDescriptor(p.relay.dir, p.name)
		_ = p.conn.Close()
		p.relay.drop(p.name, p)
		p.relay.say("%s: gone", p.name)
	})
}

// buffered returns a connection whose reads start with whatever the header read
// already pulled off the wire, so a byte read early is not a byte lost.
func buffered(conn net.Conn, br *bufio.Reader) net.Conn {
	return bufferedConn{Conn: conn, br: br}
}

type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// CloseWrite passes a half-close through to the underlying connection, so an
// EOF in one direction does not tear down the other.
func (c bufferedConn) CloseWrite() error {
	if hc, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}
