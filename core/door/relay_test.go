package door_test

// Contract tests for what the relay costs the host: what it holds while a box
// keeps asking, what it leaves behind when it closes, who else may answer a
// door, and who may read the registry.

import (
	"bufio"
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

// CONTRACT: a relay that closes leaves NOTHING behind — no socket standing in
// the registry, no descriptor describing it — however a publication and the
// close raced. A publication half-opened as the relay shut down would otherwise
// outlive it: a live listener and a file naming a service nobody answers for.
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
// listeners. Past the cap the publish is refused BY NAME — the box learns what
// it hit instead of watching a publish silently do nothing.
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
	defer conn.Close()
	err := door.ReadReply(br)
	if err == nil {
		t.Fatal("a publication past the cap was accepted")
	}
	if !strings.Contains(err.Error(), "the most one door carries") {
		t.Errorf("refused with %q, want it to name the limit", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "registry", door.NameSocket("one-too-many"))); statErr == nil {
		t.Error("a refused publication was given a listener anyway")
	}
}

// CONTRACT: one box cannot make the host hold an unbounded number of crossings
// that never say what they are. Past the cap the crossing is turned away by
// name rather than costing a goroutine and a buffer until it times out.
func TestCrossingsThatSayNothingAreCapped(t *testing.T) {
	dir := sockDir(t)
	doorPath := filepath.Join(dir, "door.sock")
	// The quiet crossings must still be waiting when the cap is hit.
	openRelay(t, dir, doorPath, io.Discard, func(r *door.Relay) { r.HeaderWait = 10 * time.Second })

	quiet := make([]net.Conn, 0, door.MaxOpeningCrossings)
	defer func() {
		for _, c := range quiet {
			_ = c.Close()
		}
	}()
	for i := 0; i < door.MaxOpeningCrossings; i++ {
		conn, _ := openCrossing(t, doorPath, "")
		quiet = append(quiet, conn)
	}
	// The relay accepts asynchronously; the cap is reached once it has taken all
	// of them, which is what this waits for.
	var err error
	waitFor(t, "the door to turn a crossing away", func() bool {
		conn, br := openCrossing(t, doorPath, "")
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		err = door.ReadReply(br)
		return err != nil && strings.Contains(err.Error(), "already waiting")
	})
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
