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
	"syscall"
	"time"
)

// The relay is the HOST side of one box's door: it owns the listening socket
// the box dials, and it is a pure byte relay — it reads each crossing's opening
// line, and nothing after it. One relay serves one box, for as long as that
// box's node lives, so the path a box dials is answered from the moment the box
// exists: a box never dials into a gap.

// Defaults for a relay's timings, which are fields on Relay so a test can
// drive the same code on a scale it can wait for.
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

// What one box may make the host allocate. The box is the untrusted side of
// this socket: a crossing that has not yet said what it is costs the host a
// goroutine and a read buffer for nothing in particular, and a publication
// costs it a listener and two files — so both are capped. A limit here is a
// LOAD limit, so hitting one is answered BUSY, never ERR: the amount going on
// changes, and a box must be able to come back rather than lose publishing for
// the rest of its life over a busy moment.
const (
	// MaxPublications is the most services one box may have published at once.
	MaxPublications = 32
	// MaxOpeningCrossings is the most crossings that may be reading their
	// opening line at one time. A crossing that has said what it is no longer
	// counts here — from that point it is a publication (capped above) or a
	// stream carrying bytes for one of them, and it may live as long as it is
	// used. Streams are how an ordinary web UI works: one connection per client,
	// held open, and they must never crowd out the next publish.
	MaxOpeningCrossings = 64
)

// How long the relay waits after a momentary accept failure before accepting
// again: a short first pause, doubling to a ceiling. The failures worth waiting
// out include running out of descriptors, which does not clear in a
// millisecond — a flat retry would spin against it, and say so on every turn.
const (
	acceptRetryFirst  = 5 * time.Millisecond
	acceptRetryAtMost = time.Second
)

// Relay serves one box's door: it accepts crossings on the door socket, holds
// each published service's crossing open, and stands a host listener in front
// of every service so anything on the host can dial it as a plain unix socket.
type Relay struct {
	// PingEvery, Idle, HeaderWait, StreamWait and DialWait are the timings
	// above, and OpeningCap is the limit above. They are read by the goroutines
	// Serve starts, so they are set before Open and Serve are called, not after.
	PingEvery, Idle, HeaderWait, StreamWait, DialWait time.Duration
	// OpeningCap is how many crossings may be reading their opening line at one
	// time; MaxOpeningCrossings by default.
	OpeningCap int
	// Carries are the host sockets this relay carries into the box, one
	// listener each (see carry.go). Set before Open.
	Carries []Carry
	// Egress is the host proxy's socket an EGRESS crossing is coupled to, ""
	// for a box with no proxy egress (see egress.go). Set before Open.
	Egress string

	dir string // the registry directory, on the host; "" for a box that may not publish
	log io.Writer

	// opening bounds the crossings that have not yet said what they are.
	opening chan struct{}

	// beforeOpen and afterOpen, when set, run either side of a publication's
	// listener being stood up. Nothing in production sets them: they exist so a
	// test can hold a publication inside that window, land a Close in it, and
	// then look at what the relay left — with no waiting on timing to decide
	// whether it has finished (see relay_export_test.go).
	beforeOpen func()
	afterOpen  func(error)

	mu        sync.Mutex
	ln        net.Listener
	carryLns  []net.Listener
	lock      *os.File // the exclusive claim on this door, held for the relay's life
	closed    bool
	published map[string]*publication
	next      uint64
}

// NewRelay returns a relay that publishes into dir and reports what it does to
// log. An empty dir is a box that may not publish: the door still answers, and
// a PUBLISH crossing is refused by name.
func NewRelay(dir string, log io.Writer) *Relay {
	return &Relay{
		PingEvery: DefaultPingEvery, Idle: DefaultIdle,
		HeaderWait: DefaultHeaderWait, StreamWait: DefaultStreamWait,
		DialWait:   DefaultDialWait,
		OpeningCap: MaxOpeningCrossings,
		dir:        dir, log: log,
		published: map[string]*publication{},
	}
}

// Run opens the door at doorPath, stands a listener for every carried socket,
// and serves until the relay is closed or the listener fails. It is the whole
// relay process.
func Run(doorPath, dir, egress string, carries []Carry, log io.Writer) error {
	r := NewRelay(dir, log)
	r.Carries = carries
	r.Egress = egress
	if err := r.Open(doorPath); err != nil {
		return err
	}
	return r.Serve()
}

// Open binds the door socket. It returns once the path is listening, so a
// caller that boots a box next knows the box's door answers from its first
// instant — the box side never has to dial a path nothing holds.
//
// One door is one relay, and the claim is an exclusive lock on a file beside
// the socket, taken BEFORE the socket is replaced: binding a unix socket means
// unlinking whatever stands there, so a second relay aimed at a live box's door
// would take that box's crossings with nothing failing — the first relay keeps
// its listening descriptor and its registry, now describing services nothing
// reaches. The lock turns that into a refusal by name.
func (r *Relay) Open(doorPath string) error {
	// The registry is the host's alone: the sockets in it are the only route
	// into a published service, so the directory is not other users' business.
	// A relay with no registry serves a box that may not publish.
	if r.dir != "" {
		if err := os.MkdirAll(r.dir, 0o700); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(doorPath), 0o700); err != nil {
		return err
	}
	lock, err := claimDoor(doorPath)
	if err != nil {
		return err
	}
	// The opening semaphore is built here, from the cap as it stands, so it is
	// the one Serve's goroutines will use. A cap of zero would be a door that
	// turns EVERY crossing away, which is nobody's intent — a relay built by
	// hand rather than by NewRelay gets the default.
	if r.OpeningCap <= 0 {
		r.OpeningCap = MaxOpeningCrossings
	}
	if r.DialWait <= 0 {
		r.DialWait = DefaultDialWait
	}
	r.opening = make(chan struct{}, r.OpeningCap)
	// The carried sockets bind before the door: the door's existence is what a
	// boot waits on before bringing the box up, so when it appears, every
	// listener a mount is established against is already standing.
	carryLns, err := r.openCarries()
	if err != nil {
		_ = lock.Close()
		return err
	}
	// A socket file left by a relay whose process is gone refuses the bind. Its
	// owner is gone — the claim above just proved it — so the file is debris.
	_ = os.Remove(doorPath)
	ln, err := net.Listen("unix", doorPath)
	if err != nil {
		for _, l := range carryLns {
			_ = l.Close()
		}
		_ = lock.Close()
		return fmt.Errorf("door: open %s: %w", doorPath, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ln, r.carryLns, r.lock = ln, carryLns, lock
	return nil
}

// claimDoor takes the exclusive claim on a door, as a lock file beside it. The
// kernel releases the lock when the holder's process dies, so a relay that was
// killed leaves the door claimable rather than locked for ever.
func claimDoor(doorPath string) (*os.File, error) {
	lockPath := doorPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("door: claim %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("door: a relay is already answering %s", doorPath)
	}
	return f, nil
}

// Serve accepts crossings until the relay is closed or the listener fails.
func (r *Relay) Serve() error {
	r.mu.Lock()
	ln := r.ln
	carryLns := r.carryLns
	r.mu.Unlock()
	if ln == nil {
		return errors.New("door: the relay was never opened")
	}
	for i, c := range r.Carries {
		go r.serveCarry(c, carryLns[i])
	}
	// wait is where the current run of accept failures has backed off to; zero
	// means accepting is healthy, and is what makes the report below one per
	// episode rather than one per attempt.
	wait := time.Duration(0)
	for {
		conn, err := ln.Accept()
		if err != nil {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return nil
			}
			// A momentary accept failure (the host out of descriptors, a peer gone
			// between connect and accept) must not end the door: this relay is the
			// only thing answering it and nothing restarts one, so returning here
			// would take the box's publishing for the rest of its life. Back off and
			// keep accepting; only a listener that is really gone ends Serve.
			if !keepsAccepting(err) {
				return err
			}
			if wait == 0 {
				wait = acceptRetryFirst
				r.say("accept: %v — still answering", err)
			} else {
				wait *= 2
				if wait > acceptRetryAtMost {
					wait = acceptRetryAtMost
				}
			}
			time.Sleep(wait)
			continue
		}
		wait = 0 // accepting works again; the next failure starts a new episode
		go r.cross(conn)
	}
}

// keepsAccepting reports whether the relay carries on after an accept failure.
// The failures it survives are the ones that describe a MOMENT — the host out
// of descriptors, a peer that hung up between connect and accept, a signal, a
// deadline — because nothing restarts a relay, so ending the loop on one would
// take the box's publishing with it. A listener that is really closed, or any
// error that says the door itself is gone, ends the loop.
func keepsAccepting(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, net.ErrClosed):
		return false
	case errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.EMFILE),
		errors.Is(err, syscall.ENFILE),
		errors.Is(err, syscall.EINTR):
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Close stops the relay and every service it publishes.
func (r *Relay) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	ln, lock := r.ln, r.lock
	carryLns := r.carryLns
	pubs := make([]*publication, 0, len(r.published))
	for _, p := range r.published {
		pubs = append(pubs, p)
	}
	r.mu.Unlock()
	for _, p := range pubs {
		p.close()
	}
	for _, l := range carryLns {
		_ = l.Close()
	}
	if lock != nil {
		_ = lock.Close() // releases the door claim
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// cross reads one crossing's opening line and does what the line says. The
// opening slot is given back as soon as the line is read, whether it read or
// not: what it bounds is the crossings the host is holding open for nothing —
// a publication or a stream is bounded by what it is, and lives as long as it
// is used.
func (r *Relay) cross(conn net.Conn) {
	select {
	case r.opening <- struct{}{}:
	default:
		// More crossings are reading their opening line at once than the host will
		// hold for one box. BUSY, and named, so the box learns what it hit and
		// knows to come back.
		_ = WriteBusy(conn, fmt.Errorf("door: %d crossings are already opening", r.OpeningCap))
		_ = conn.Close()
		r.say("a crossing was turned away: %d already opening", r.OpeningCap)
		return
	}
	br := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(r.HeaderWait))
	h, err := ReadHeader(br)
	<-r.opening
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
	case VerbEgress:
		r.egress(conn, br, h)
	default:
		_ = WriteReply(conn, fmt.Errorf("door: %q is not a verb this door knows", h.Verb))
		_ = conn.Close()
	}
}

// publish takes a box's publishing crossing: it stands a host listener in front
// of the service, writes its descriptor, and holds the crossing open until
// either side stops answering.
func (r *Relay) publish(conn net.Conn, br *bufio.Reader, h Header) {
	// A door without a registry belongs to a box that may not publish — it
	// exists for the box's other crossings. The refusal is a decision and is
	// BY NAME, so a denied publish reads as a denial, not a broken door.
	if r.dir == "" {
		_ = WriteReply(conn, errors.New("this box was not granted service publishing — the recipe that boots the box must say `publish: true`"))
		_ = conn.Close()
		return
	}
	name, typ, port, err := h.Publication()
	if err != nil {
		_ = WriteReply(conn, err)
		_ = conn.Close()
		return
	}
	p := &publication{
		relay: r, name: name, typ: typ, port: port,
		conn: conn, br: br, done: make(chan struct{}),
		pending: map[uint64]net.Conn{},
	}
	if err := r.hold(p); err != nil {
		// A limit says BUSY and a decision says ERR, because the box side must
		// treat them differently: it comes back from one and gives up on the other.
		var busy BusyError
		if errors.As(err, &busy) {
			_ = WriteBusy(conn, err)
		} else {
			_ = WriteReply(conn, err)
		}
		_ = conn.Close()
		return
	}
	if r.beforeOpen != nil {
		r.beforeOpen()
	}
	err = p.open()
	if r.afterOpen != nil {
		r.afterOpen(err)
	}
	if err != nil {
		r.drop(name, p)
		var busy BusyError
		if errors.As(err, &busy) {
			_ = WriteBusy(conn, err)
		} else {
			_ = WriteReply(conn, err)
		}
		_ = conn.Close()
		return
	}
	if err := WriteReply(conn, nil); err != nil {
		p.close()
		return
	}
	r.say("%s: published (%s, box port %d)", name, typ, port)
	p.serve()
}

// hold registers a publication under its name, or says why it cannot. Two of
// the three refusals are BUSY — a relay that is closing and a box at its
// publication limit are both states of the moment, and a publisher that comes
// back may well get in. The third is a decision: one name is one listener in
// one box, so the second claimant of a live name is simply wrong, and is told
// so rather than left retrying a name it will never get. (The host resolves a
// name claimed by TWO boxes as a conflict; this is one box's own doing.)
func (r *Relay) hold(p *publication) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
		return BusyError{Reason: "door: the relay is closing"}
	case len(r.published) >= MaxPublications:
		return BusyError{Reason: fmt.Sprintf("door: this box already publishes %d services, the most one door carries at once", MaxPublications)}
	}
	if _, taken := r.published[p.name]; taken {
		return fmt.Errorf("door: %q is already published in this box", p.name)
	}
	r.published[p.name] = p
	return nil
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
	Couple(client, bufferConn(conn, br))
}

// mintStreamID returns an id no live crossing is using. Ids are minted here, so
// they are unique across the whole door even though each parked connection is
// held by the publication that parked it.
func (r *Relay) mintStreamID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	return r.next
}

// claim takes the connection parked under an id, or nil when nothing is parked
// under it. A parked connection is held by the publication that parked it, so
// the search is over the publications — an id can never name a connection that
// belongs to another one.
func (r *Relay) claim(id uint64) net.Conn {
	r.mu.Lock()
	pubs := make([]*publication, 0, len(r.published))
	for _, p := range r.published {
		pubs = append(pubs, p)
	}
	r.mu.Unlock()
	for _, p := range pubs {
		if c := p.claim(id); c != nil {
			return c
		}
	}
	return nil
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
func (r *Relay) say(format string, args ...any) {
	if r.log == nil {
		return
	}
	fmt.Fprintf(r.log, format+"\n", args...)
}

// publication is one service the box published: the crossing the box holds, the
// host listener standing in front of it, the client connections waiting for a
// crossing to carry them, and the descriptor that makes it visible to
// `dabs services`. They appear together and go together — the registry never
// describes a service nothing answers for.
type publication struct {
	relay *Relay
	name  string
	typ   string
	port  int

	conn net.Conn
	br   *bufio.Reader

	wmu  sync.Mutex // one writer at a time on the held crossing
	once sync.Once
	done chan struct{}

	mu      sync.Mutex
	ln      net.Listener
	shut    bool
	pending map[uint64]net.Conn
}

// open stands the host listener up and writes the descriptor beside it. A
// publication closed while this was running (the relay shutting down between
// the two) is undone here, rather than left holding a listener and a descriptor
// nothing will ever take away.
func (p *publication) open() error {
	sock := filepath.Join(p.relay.dir, NameSocket(p.name))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("door: listen for %q: %w", p.name, err)
	}
	if err := WriteDescriptor(p.relay.dir, p.name, p.typ, p.port); err != nil {
		_ = ln.Close()
		return fmt.Errorf("door: describe %q: %w", p.name, err)
	}
	p.mu.Lock()
	if p.shut {
		p.mu.Unlock()
		_ = ln.Close()
		_ = RemoveDescriptor(p.relay.dir, p.name)
		return BusyError{Reason: "door: the relay is closing"}
	}
	p.ln = ln
	p.mu.Unlock()
	go p.accept(ln)
	return nil
}

// serve keeps the crossing alive: it asks for a heartbeat on a schedule and
// reads the answers, and closes the publication the moment either stops.
func (p *publication) serve() {
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
func (p *publication) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
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
	id := p.relay.mintStreamID()
	if !p.park(id, client) {
		_ = client.Close()
		return
	}
	if err := p.send(fmt.Sprintf("%s %d", MsgStream, id)); err != nil {
		if c := p.claim(id); c != nil {
			_ = c.Close()
		}
		p.close()
		return
	}
	time.AfterFunc(p.relay.StreamWait, func() {
		if c := p.claim(id); c != nil {
			p.relay.say("%s: the box did not open stream %d", p.name, id)
			_ = c.Close()
		}
	})
}

// park holds a client connection until the box opens a crossing for it, and
// reports whether it was taken — a publication already closed takes none.
func (p *publication) park(id uint64, client net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shut {
		return false
	}
	p.pending[id] = client
	return true
}

// claim takes this publication's connection parked under id, or nil.
func (p *publication) claim(id uint64) net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.pending[id]
	if !ok {
		return nil
	}
	delete(p.pending, id)
	return c
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
// socket), the descriptor, every connection still parked, and the crossing
// itself. What the host scans and what the host can dial disappear together.
func (p *publication) close() {
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		p.shut = true
		ln := p.ln
		parked := make([]net.Conn, 0, len(p.pending))
		for id, c := range p.pending {
			parked = append(parked, c)
			delete(p.pending, id)
		}
		p.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		for _, c := range parked {
			_ = c.Close()
		}
		_ = RemoveDescriptor(p.relay.dir, p.name)
		_ = p.conn.Close()
		p.relay.drop(p.name, p)
		p.relay.say("%s: gone", p.name)
	})
}

// bufferConn returns a connection whose reads start with whatever the header
// read already pulled off the wire, so a byte read early is not a byte lost.
func bufferConn(conn net.Conn, br *bufio.Reader) net.Conn {
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
