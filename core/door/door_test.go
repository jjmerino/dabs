package door_test

// Contract tests for the box door: what a crossing must say to be taken, what
// the registry may claim, and what a box gets when the door is late, gone, or
// never granted at all.

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/door"
)

// sockDir is a short-named directory for unix sockets. A socket path is capped
// by the kernel at about a hundred bytes, and the usual temp dir on macOS
// spends most of that before the filename.
func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dabsdoor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// syncBuffer collects a relay's log while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// quickRelay returns a relay wired to timings a test can wait for, already
// serving the door path it returns.
func quickRelay(t *testing.T, dir string) (*door.Relay, string) {
	t.Helper()
	doorPath := filepath.Join(dir, "door.sock")
	r := openRelay(t, dir, doorPath, io.Discard)
	return r, doorPath
}

// openRelay opens a relay on an exact door path, reporting what it does to log,
// and serves it until the test ends.
func openRelay(t *testing.T, dir, doorPath string, log io.Writer) *door.Relay {
	t.Helper()
	r := door.NewRelay(filepath.Join(dir, "registry"), log)
	r.PingEvery, r.Idle, r.HeaderWait, r.StreamWait = 20*time.Millisecond, 400*time.Millisecond, time.Second, time.Second
	if err := r.Open(doorPath); err != nil {
		t.Fatalf("open the door: %v", err)
	}
	go func() { _ = r.Serve() }()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// quickPublisher returns a box-side publisher on the same scale.
func quickPublisher(doorPath string) door.Publisher {
	p := door.NewPublisher(doorPath, io.Discard)
	p.Idle, p.Redial, p.DialEvery, p.DialAtMost = 400*time.Millisecond, 2*time.Second, 10*time.Millisecond, 50*time.Millisecond
	return p
}

// boxService stands a TCP responder on box-local loopback — what a program in
// the box would have bound — and returns its port.
func boxService(t *testing.T, body string) int {
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
				b := make([]byte, 64)
				n, _ := c.Read(b)
				_, _ = c.Write([]byte(body + string(b[:n])))
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// publishing starts a publisher in the background and reports what it finally
// returned, so a test can assert on a refusal without waiting for one that
// never comes.
func publishing(t *testing.T, p door.Publisher, name, typ string, port int) <-chan error {
	t.Helper()
	out := make(chan error, 1)
	go func() { out <- p.Publish(name, typ, port) }()
	return out
}

// waitFor polls until cond holds, failing with why if it never does.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", why)
}

// exists reports whether a path is there.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CONTRACT: a service published over the door answers on the host socket the
// relay stands in front of it, both directions, with the box never listening on
// anything the host had to find.
func TestAPublishedServiceCarriesBytesBothWays(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	port := boxService(t, "from-the-box:")
	failed := publishing(t, quickPublisher(doorPath), "web", door.TypeWebUI, port)

	sock := filepath.Join(dir, "registry", door.SocketName("web"))
	waitFor(t, "the relay to stand a listener at "+sock, func() bool { return exists(sock) })

	for i := 0; i < 3; i++ { // every client is its own crossing
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatalf("dial %s: %v", sock, err)
		}
		if _, err := conn.Write([]byte("hello")); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		got, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(got) != "from-the-box:hello" {
			t.Errorf("crossing %d carried %q, want the box service's answer", i, got)
		}
	}
	select {
	case err := <-failed:
		t.Fatalf("the publisher stopped: %v", err)
	default:
	}
}

// CONTRACT: the registry describes only what the door is actually holding — the
// descriptor and the socket appear together when the crossing is taken, and
// both are gone once the box stops answering. A descriptor left behind would
// report a service nothing can reach.
func TestTheRegistryHoldsOnlyWhatTheDoorHolds(t *testing.T) {
	dir := sockDir(t)
	relay, doorPath := quickRelay(t, dir)
	port := boxService(t, "x")
	p := quickPublisher(doorPath)
	p.Redial = 10 * time.Millisecond // stop trying once the relay is closed
	publishing(t, p, "web", door.TypeGeneral, port)

	desc := filepath.Join(dir, "registry", door.DescriptorName("web"))
	sock := filepath.Join(dir, "registry", door.SocketName("web"))
	waitFor(t, "the registry to describe web", func() bool { return exists(desc) && exists(sock) })

	_ = relay.Close()
	waitFor(t, "the registry to forget web", func() bool { return !exists(desc) && !exists(sock) })
}

// CONTRACT: a box that was not granted a door is refused BY NAME and at once —
// not left retrying, and not handed a bare "no such file", which reads as a
// broken box rather than a denied request.
func TestAnUngrantedBoxIsRefusedByName(t *testing.T) {
	dir := sockDir(t)
	missing := filepath.Join(dir, "door.sock")
	err := quickPublisher(missing).Publish("web", door.TypeGeneral, 8080)
	var not door.NotGranted
	if !errors.As(err, &not) {
		t.Fatalf("Publish = %v, want a NotGranted refusal", err)
	}
	if !strings.Contains(err.Error(), "publish: true") {
		t.Errorf("the refusal does not say what the recipe must set: %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the refusal does not name the door it looked for: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused publish left %v behind", entries)
	}
}

// CONTRACT: a door that is not answering yet is dialed again — a box that
// publishes a moment before the host side is ready ends up published, and one
// failed dial never costs the box its ability to publish.
func TestADoorThatAnswersLateIsStillPublishedOn(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	// The path exists (the box was granted a door) but nothing accepts on it yet.
	stale, err := net.Listen("unix", doorPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = stale.Close()
	if err := os.WriteFile(doorPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	port := boxService(t, "late:")
	failed := publishing(t, quickPublisher(doorPath), "web", door.TypeGeneral, port)

	time.Sleep(200 * time.Millisecond)
	openRelay(t, dir, doorPath, io.Discard)
	sock := filepath.Join(dir, "registry", door.SocketName("web"))
	waitFor(t, "the late door to publish web", func() bool { return exists(sock) })
	select {
	case err := <-failed:
		t.Fatalf("the publisher gave up: %v", err)
	default:
	}
}

// CONTRACT: a door that never answers fails the publish with a named error
// rather than hanging for the box's whole life.
func TestADoorThatNeverAnswersFailsThePublish(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	if err := os.WriteFile(doorPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := quickPublisher(doorPath)
	p.Redial = 100 * time.Millisecond
	err := p.Publish("web", door.TypeGeneral, 8080)
	if err == nil {
		t.Fatal("Publish returned nil for a door that never answered")
	}
	if !strings.Contains(err.Error(), doorPath) {
		t.Errorf("the error does not name the door: %v", err)
	}
}

// CONTRACT: a door that ACCEPTS and then says nothing is a transport failure,
// not a refusal: it is retried for the whole redial window and then fails with
// a named error. It is what a box sees when the host-side listener is gone but
// the path it dials still accepts — the very case that must never be read as
// "the door decided against this service".
func TestADoorThatAcceptsAndGoesQuietIsRetriedNotRefused(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	ln, err := net.Listen("unix", doorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dialled := make(chan struct{}, 32)
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, conn) // accepted, never answered
			select {
			case dialled <- struct{}{}:
			default:
			}
		}
	}()

	p := quickPublisher(doorPath)
	p.Idle, p.Redial = 100*time.Millisecond, 700*time.Millisecond
	err = p.Publish("web", door.TypeGeneral, 8080)
	var refused door.Refused
	if errors.As(err, &refused) {
		t.Fatalf("a quiet door was read as a refusal: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "has not answered") {
		t.Fatalf("Publish = %v, want it to say the door never answered", err)
	}
	if len(dialled) < 2 {
		t.Errorf("the publisher dialled %d times, want it to have tried again", len(dialled))
	}
}

// CONTRACT: one name is one publication in one box, and the second claimant is
// told so — a refusal the box must not retry, because retrying decides nothing.
func TestASecondClaimOfALiveNameIsRefused(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	port := boxService(t, "x")
	publishing(t, quickPublisher(doorPath), "web", door.TypeGeneral, port)
	waitFor(t, "web to be published", func() bool {
		return exists(filepath.Join(dir, "registry", door.SocketName("web")))
	})

	err := quickPublisher(doorPath).Publish("web", door.TypeGeneral, port)
	var refused door.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("second claim = %v, want a Refused", err)
	}
	if !strings.Contains(err.Error(), "already published") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// CONTRACT: a crossing that stops answering the heartbeat is dropped. A live
// connection proves nothing — a box may hold a socket whose acceptor is gone —
// so the publication is judged by an answer that had to be produced.
func TestACrossingThatStopsAnsweringIsDropped(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	conn, err := net.Dial("unix", doorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := door.WriteHeader(conn, door.PublishHeader("mute", door.TypeGeneral, 8080)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	if err := door.ReadReply(br); err != nil {
		t.Fatalf("the door refused a valid publish: %v", err)
	}
	sock := filepath.Join(dir, "registry", door.SocketName("mute"))
	waitFor(t, "mute to be published", func() bool { return exists(sock) })
	// The crossing is open and the socket file is there, but nobody answers the
	// pings the relay is now sending.
	waitFor(t, "the relay to drop the mute crossing", func() bool { return !exists(sock) })
}

// CONTRACT: an IDLE service is not a dead one. A publisher nobody dials must
// stay published across many idle periods — which is what the heartbeat buys:
// without something to answer, silence and death would look the same and a
// healthy service would be dropped for being quiet.
func TestAnIdleServiceStaysPublished(t *testing.T) {
	dir := sockDir(t)
	log := &syncBuffer{}
	doorPath := filepath.Join(dir, "door.sock")
	relay := openRelay(t, dir, doorPath, log)
	port := boxService(t, "still-here:")
	failed := publishing(t, quickPublisher(doorPath), "web", door.TypeGeneral, port)

	sock := filepath.Join(dir, "registry", door.SocketName("web"))
	waitFor(t, "web to be published", func() bool { return exists(sock) })
	// Nothing dials it for several times as long as the relay waits before
	// judging a crossing dead.
	time.Sleep(3 * relay.Idle)
	if !exists(sock) {
		t.Fatal("an idle service was dropped")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial after idling: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write after idling: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read after idling: %v", err)
	}
	if string(got) != "still-here:hello" {
		t.Errorf("after idling the crossing carried %q", got)
	}
	// The SAME crossing all along: a service dropped for being quiet and
	// re-published looks identical from outside, and is not what was asked for.
	if n := strings.Count(log.String(), "published"); n != 1 {
		t.Errorf("the relay published %d times while the service idled:\n%s", n, log.String())
	}
	if strings.Contains(log.String(), "gone") {
		t.Errorf("the relay dropped the idle crossing:\n%s", log.String())
	}
	select {
	case err := <-failed:
		t.Fatalf("the publisher stopped while idle: %v", err)
	default:
	}
}

// CONTRACT: a crossing that does not open with this protocol's banner is
// refused, whatever else it says — the door is dabs's, and something else
// speaking into it is not a service.
func TestACrossingWithoutTheBannerIsRefused(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	for _, opening := range []string{
		"GET / HTTP/1.1",
		"DABS-DOOR/2 PUBLISH web general 80",
		"PUBLISH web general 80",
	} {
		conn, err := net.Dial("unix", doorPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, opening+"\n"); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		rest, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatalf("%q: read: %v", opening, err)
		}
		if len(rest) != 0 {
			t.Errorf("%q was answered with %q, want the door to say nothing and close", opening, rest)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "registry")); len(entries) != 0 {
		t.Errorf("a refused crossing published %v", entries)
	}
}

// CONTRACT: a header the door cannot make sense of is answered with a NAMED
// refusal and nothing is provisioned — a peer must never be left waiting on a
// door that silently decided against it.
func TestABadPublishHeaderIsAnsweredWithWhy(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	for _, tc := range []struct{ line, want string }{
		{door.Banner + " PUBLISH", "takes a name"},
		{door.Banner + " PUBLISH web general nope", "is not a port"},
		{door.Banner + " PUBLISH WEB general 80", "is not [a-z0-9._-]"},
		{door.Banner + " PUBLISH web gui 80", "is not webui or general"},
		{door.Banner + " STREAM 99", "nothing is waiting"},
		{door.Banner + " KNOCK", "is not a verb"},
	} {
		conn, err := net.Dial("unix", doorPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, tc.line+"\n"); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(conn)
		err = door.ReadReply(br)
		conn.Close()
		if err == nil {
			t.Errorf("%q was accepted", tc.line)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q refused with %q, want it to say %q", tc.line, err, tc.want)
		}
	}
}

// CONTRACT: a line that never ends is refused rather than buffered without
// limit — the box is the untrusted side of this socket.
func TestALineOverTheCapIsRefused(t *testing.T) {
	dir := sockDir(t)
	_, doorPath := quickRelay(t, dir)
	conn, err := net.Dial("unix", doorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, door.Banner+" PUBLISH "+strings.Repeat("n", door.MaxLine*2)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("the door answered an endless line with %q", rest)
	}
}

// CONTRACT: a header round-trips through the wire form it is written in, so the
// two independently built sides read the same crossing out of the same bytes.
func TestAHeaderRoundTripsThroughItsWireForm(t *testing.T) {
	for _, h := range []door.Header{
		door.PublishHeader("web.one_2-3", door.TypeWebUI, 5173),
		door.StreamHeader(42),
	} {
		var buf strings.Builder
		if err := door.WriteHeader(&buf, h); err != nil {
			t.Fatal(err)
		}
		got, err := door.ReadHeader(bufio.NewReader(strings.NewReader(buf.String())))
		if err != nil {
			t.Fatalf("%q: %v", buf.String(), err)
		}
		if got.Verb != h.Verb || strings.Join(got.Args, " ") != strings.Join(h.Args, " ") {
			t.Errorf("%q read back as %+v, want %+v", buf.String(), got, h)
		}
		if !strings.HasPrefix(buf.String(), door.Banner+" ") {
			t.Errorf("%q does not open with the banner", buf.String())
		}
	}
}

// CONTRACT: a refusal crosses the wire as one line whatever the reason says, so
// a multi-line message can never forge the peer's next line.
func TestARefusalStaysOnOneLine(t *testing.T) {
	var buf strings.Builder
	if err := door.WriteReply(&buf, errors.New("bad\nDABS-DOOR/1 OK")); err != nil {
		t.Fatal(err)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("a refusal crossed as %q, want one line", buf.String())
	}
	if err := door.ReadReply(bufio.NewReader(strings.NewReader(buf.String()))); err == nil {
		t.Error("the forged reply read as OK")
	}
}
