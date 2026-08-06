package door_test

// Contract tests for what the relay costs the host: what it holds while a box
// keeps asking, what it leaves behind when it closes, who else may answer a
// door, and who may read the registry.

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/door"
)

// openCrossing dials a door and sends one raw line, returning the connection
// and a reader positioned to read the answer.
func openCrossing(t *testing.T, doorPath, line string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("unix", doorPath)
	if err != nil {
		t.Fatalf("dial %s: %v", doorPath, err)
	}
	if line != "" {
		if _, err := io.WriteString(conn, line+"\n"); err != nil {
			t.Fatalf("write %q: %v", line, err)
		}
	}
	return conn, bufio.NewReader(conn)
}

// publishLine is the header a box opens a publishing crossing with.
func publishLine(name string, port int) string {
	return door.Banner + " " + door.VerbPublish + " " + name + " " + door.TypeGeneral + " " + strconv.Itoa(port)
}

// CONTRACT: a relay that closes while a publication is being stood up leaves
// NOTHING behind. The window is held open on purpose here — a publication is
// registered, the close lands, and only then does the listener go up — because
// racing for it proves nothing: a half-opened publication that outlives its
// relay is a live listener, a socket in the registry and a descriptor naming a
// service nobody answers for.
func TestClosingARelayMidPublishStrandsNothing(t *testing.T) {
	dir := sockDir(t)
	registry := filepath.Join(dir, "registry")
	doorPath := filepath.Join(dir, "door.sock")
	r := door.NewRelay(registry, io.Discard)
	inside, resume, opened := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	r.PauseBeforeOpen(func() {
		close(inside)
		<-resume
	})
	r.ReportAfterOpen(func(err error) { opened <- err })
	if err := r.Open(doorPath); err != nil {
		t.Fatalf("open the door: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })

	answered := make(chan error, 1)
	go func() {
		conn, err := net.Dial("unix", doorPath)
		if err != nil {
			answered <- err
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, publishLine("web", 8080)+"\n"); err != nil {
			answered <- err
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		answered <- door.ReadReply(bufio.NewReader(conn))
	}()

	<-inside      // the publication is registered, its listener is not up yet
	_ = r.Close() // the close lands exactly there
	close(resume)
	<-opened // the listener attempt is over, whatever it decided

	if err := <-answered; err == nil {
		t.Error("a publication begun as the relay closed was told it succeeded")
	}
	entries, err := os.ReadDir(registry)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the closed relay left %s in the registry", e.Name())
	}
	if conn, err := net.Dial("unix", filepath.Join(registry, door.NameSocket("web"))); err == nil {
		conn.Close()
		t.Error("a listener survived the relay that made it")
	}
}

// CONTRACT: the same holds however the two actually interleave, with nothing
// held open — the window above is one point in a range, and no point in it may
// strand a socket or a descriptor.
func TestClosingARelayStrandsNothingWhateverRacesIt(t *testing.T) {
	for i := 0; i < 40; i++ {
		dir := sockDir(t)
		registry := filepath.Join(dir, "registry")
		doorPath := filepath.Join(dir, "door.sock")
		r := openRelay(t, dir, doorPath, io.Discard)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("unix", doorPath)
			if err != nil {
				return // the relay closed first; nothing to publish through
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, publishLine("web", 8080)+"\n")
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, _ = bufio.NewReader(conn).ReadString('\n')
		}()
		go func() {
			defer wg.Done()
			_ = r.Close()
		}()
		wg.Wait()

		entries, err := os.ReadDir(registry)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, e := range entries {
			t.Fatalf("run %d: the closed relay left %s in the registry", i, e.Name())
		}
		if conn, err := net.Dial("unix", filepath.Join(registry, door.NameSocket("web"))); err == nil {
			conn.Close()
			t.Fatalf("run %d: a listener survived the relay that made it", i)
		}
	}
}

// CONTRACT: one box cannot make the host stand an unbounded number of
// listeners. Past the cap the publish is turned away BY NAME and as BUSY, not
// as a refusal: the limit is a state of the moment, and a publisher that comes
// back once a name is given up gets in.
func TestABoxCannotPublishPastTheCap(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	held := make([]net.Conn, 0, door.MaxPublications)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < door.MaxPublications; i++ {
		conn, br := openCrossing(t, doorPath, publishLine("svc"+strconv.Itoa(i), 8080))
		if err := door.ReadReply(br); err != nil {
			t.Fatalf("publication %d was refused: %v", i, err)
		}
		held = append(held, conn)
	}
	conn, br := openCrossing(t, doorPath, publishLine("one-too-many", 8080))
	err := door.ReadReply(br)
	conn.Close()
	if err == nil {
		t.Fatal("a publication past the cap was accepted")
	}
	var busy door.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("over-cap answered %#v, want a BusyError the box comes back from", err)
	}
	if !strings.Contains(err.Error(), "the most one door carries") {
		t.Errorf("turned away with %q, want it to name the limit", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "registry", door.NameSocket("one-too-many"))); statErr == nil {
		t.Error("a turned-away publication was given a listener anyway")
	}
	// Room again: the same publish now gets in, which is what makes the limit a
	// moment rather than a verdict.
	_ = held[0].Close()
	held = held[1:]
	waitFor(t, "the door to take a publication once one is given up", func() bool {
		conn, br := openCrossing(t, doorPath, publishLine("one-too-many", 8080))
		if err := door.ReadReply(br); err != nil {
			conn.Close()
			return false
		}
		held = append(held, conn)
		return true
	})
}

// CONTRACT: a publisher meeting a door at its limit KEEPS TRYING. A load limit
// answered as a refusal would end the publisher on the spot — the failure the
// whole redial window exists to prevent — so a busy door must never look like a
// decision.
func TestAPublisherComesBackFromABusyDoor(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	crowd := make([]net.Conn, 0, door.MaxPublications)
	defer func() {
		for _, c := range crowd {
			_ = c.Close()
		}
	}()
	for i := 0; i < door.MaxPublications; i++ {
		conn, br := openCrossing(t, doorPath, publishLine("svc"+strconv.Itoa(i), 8080))
		if err := door.ReadReply(br); err != nil {
			t.Fatalf("publication %d was turned away: %v", i, err)
		}
		crowd = append(crowd, conn)
	}
	port := boxService(t, "late:")
	p := quickPublisher(doorPath)
	p.Redial = 5 * time.Second
	failed := publishing(t, p, "mine", door.TypeGeneral, port)
	// It must still be trying a moment later, not gone.
	select {
	case err := <-failed:
		t.Fatalf("the publisher gave up on a busy door: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// One publication ends, and the waiting publisher takes the room.
	_ = crowd[0].Close()
	crowd = crowd[1:]
	waitFor(t, "the waiting publisher to get in", func() bool {
		return exists(filepath.Join(dir, "registry", door.NameSocket("mine")))
	})
	select {
	case err := <-failed:
		t.Fatalf("the publisher stopped after getting in: %v", err)
	default:
	}
}

// holdingService stands a box-local responder that echoes and NEVER hangs up,
// so a client that got an answer is a crossing still in use — the shape of a
// web UI holding its connection.
func holdingService(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				b := make([]byte, 1)
				for {
					if _, err := c.Read(b); err != nil {
						return
					}
					if _, err := c.Write(b); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// CONTRACT: the opening cap counts only crossings that have NOT yet said what
// they are. A crossing that stated its business — a held publication, a stream
// carrying a client — gives its slot back at once, so ordinary use (a web UI
// with connections held open) can never crowd out the next publish.
func TestPublicationsAndStreamsDoNotHoldOpeningSlots(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	port := holdingService(t)
	publishing(t, quickPublisher(doorPath), "web", door.TypeGeneral, port)
	sock := filepath.Join(dir, "registry", door.NameSocket("web"))
	waitFor(t, "web to be published", func() bool { return exists(sock) })

	// Far more clients than the opening cap, each holding its connection — every
	// one is a crossing of its own, and none of them is opening any more. Each is
	// round-tripped before the next, so by the end every one of them is a stream
	// the box is really carrying.
	clients := make([]net.Conn, 0, door.MaxOpeningCrossings+8)
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	for i := 0; i < door.MaxOpeningCrossings+8; i++ {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		clients = append(clients, conn)
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		b := make([]byte, 1)
		if _, err := conn.Read(b); err != nil {
			t.Fatalf("client %d never reached the box service: %v", i, err)
		}
	}
	// A fresh publish still gets in.
	conn, br := openCrossing(t, doorPath, publishLine("another", 9090))
	defer conn.Close()
	if err := door.ReadReply(br); err != nil {
		t.Fatalf("a publish was turned away while %d clients were connected: %v", len(clients), err)
	}
}

// CONTRACT: one box cannot make the host hold an unbounded number of crossings
// that never say what they are. Past the cap the crossing is turned away by
// name, and as BUSY, rather than costing a goroutine and a buffer until it
// times out. The cap is driven small here so the state under test is exact:
// every crossing below the limit is confirmed to be IN before the one over it
// is dialed.
func TestCrossingsThatSayNothingAreCapped(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	const cap = 2
	openRelay(t, dir, doorPath, io.Discard, func(r *door.Relay) {
		r.OpeningCap = cap
		r.HeaderWait = 10 * time.Second // they must still be waiting when the cap is hit
	})

	quiet := make([]net.Conn, 0, cap)
	defer func() {
		for _, c := range quiet {
			_ = c.Close()
		}
	}()
	for i := 0; i < cap; i++ {
		conn, br := openCrossing(t, doorPath, "")
		quiet = append(quiet, conn)
		// It is holding a slot only once the relay has taken it and is waiting for
		// its line — which is exactly the state of saying nothing back.
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if err := door.ReadReply(br); err == nil {
			t.Fatalf("crossing %d was answered before it said anything", i)
		} else if !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("crossing %d did not get in: %v", i, err)
		}
		_ = conn.SetReadDeadline(time.Time{})
	}

	conn, br := openCrossing(t, doorPath, "")
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	err := door.ReadReply(br)
	if err == nil {
		t.Fatal("a crossing over the cap was taken")
	}
	if !strings.Contains(err.Error(), "already opening") {
		t.Fatalf("turned away with %q, want it to name the limit", err)
	}
	var busy door.BusyError
	if !errors.As(err, &busy) {
		t.Errorf("a turned-away crossing got %#v, want a BusyError it comes back from", err)
	}
}

// CONTRACT: the registry is the host's own. The sockets in it are the only
// route into a box's published service, so neither the directory nor the
// descriptors are readable by other users on this machine.
func TestTheRegistryIsNotOtherUsersBusiness(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	conn, br := openCrossing(t, doorPath, publishLine("web", 8080))
	defer conn.Close()
	if err := door.ReadReply(br); err != nil {
		t.Fatalf("publish: %v", err)
	}
	registry := filepath.Join(dir, "registry")
	desc := filepath.Join(registry, door.NameDescriptor("web"))
	waitFor(t, "web to be described", func() bool { return exists(desc) })
	for _, tc := range []struct {
		path string
		want os.FileMode
	}{
		{registry, 0o700},
		{desc, 0o600},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s is %o, want %o", tc.path, got, tc.want)
		}
	}
}

// CONTRACT: one door is one relay. Binding a unix socket unlinks whatever
// stands there, so a second relay aimed at a live door would take the box's
// crossings with nothing failing — it is refused by name instead, and the door
// becomes claimable again once the relay holding it is gone.
func TestASecondRelayMayNotTakeALiveDoor(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	first := openRelay(t, dir, doorPath, io.Discard)

	second := door.NewRelay(filepath.Join(dir, "registry"), io.Discard)
	err := second.Open(doorPath)
	if err == nil {
		_ = second.Close()
		t.Fatal("a second relay took a live door")
	}
	if !strings.Contains(err.Error(), "already answering") {
		t.Errorf("refused with %q, want it to say a relay already answers", err)
	}
	// The first relay still answers: the refusal cost it nothing.
	conn, br := openCrossing(t, doorPath, publishLine("web", 8080))
	defer conn.Close()
	if err := door.ReadReply(br); err != nil {
		t.Fatalf("the first relay stopped answering: %v", err)
	}

	_ = first.Close()
	third := door.NewRelay(filepath.Join(dir, "registry"), io.Discard)
	if err := third.Open(doorPath); err != nil {
		t.Fatalf("the door stayed claimed after its relay closed: %v", err)
	}
	_ = third.Close()
}
