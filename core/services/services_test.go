package services_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjmerino/dabs/core/services"
	"github.com/jjmerino/dabs/egressforwarder/forwarder"
)

// shortTempDir is a temp directory with a SHORT path: a unix socket path has a
// hard length limit (about 100 bytes), and a directory named after the test
// blows through it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "d")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// writeDescriptor puts a descriptor in dir, with or without its socket.
func writeDescriptor(t *testing.T, dir, name, typ string, port int, withSocket bool) string {
	t.Helper()
	b, err := json.Marshal(forwarder.Descriptor{Type: typ, Port: port})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, forwarder.DescriptorName(name)), b, 0o644); err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	sock := filepath.Join(dir, forwarder.SocketName(name))
	if withSocket {
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("socket: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
	}
	return sock
}

// CONTRACT: a descriptor is a service only when its socket is there — a
// descriptor left behind by a publisher that died names nothing dialable.
func TestScanDirReportsOnlyServicesWithSockets(t *testing.T) {
	dir := shortTempDir(t)
	writeDescriptor(t, dir, "web", forwarder.TypeWebUI, 5173, true)
	writeDescriptor(t, dir, "orphan", forwarder.TypeGeneral, 9000, false)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("stray file: %v", err)
	}
	found, err := services.ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(found) != 1 || found[0].Name != "web" || found[0].Type != forwarder.TypeWebUI || found[0].BoxPort != 5173 {
		t.Fatalf("ScanDir = %+v, want just web/webui/5173", found)
	}
}

func TestScanDirOfAnAbsentDirectoryFindsNothing(t *testing.T) {
	found, err := services.ScanDir(filepath.Join(shortTempDir(t), "never"))
	if err != nil || len(found) != 0 {
		t.Fatalf("ScanDir(absent) = %+v, %v; want no services and no error", found, err)
	}
}

// CONTRACT: a socket that no longer accepts is listed but reported down —
// listing and reachability are different questions.
func TestUpDistinguishesAnAnsweringSocket(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, forwarder.SocketName("web"))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	svc := services.Service{Name: "web", Socket: sock}
	if !svc.Up() {
		t.Errorf("Up() = false while the socket accepts")
	}
	_ = ln.Close()
	if svc.Up() {
		t.Errorf("Up() = true after the listener closed")
	}
}

// CONTRACT: a name keeps its port for ever — that is what makes a URL a human
// wrote down survive the box that served it.
func TestAssignedPortsAreStableAndPersisted(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "ports.json")
	p, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	free := func(int) bool { return true }
	web, err := p.Assign("web", free)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	api, err := p.Assign("api", free)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if web == api {
		t.Fatalf("two services share port %d", web)
	}
	for _, port := range []int{web, api} {
		if port < services.PortRangeLow || port > services.PortRangeHigh {
			t.Errorf("port %d is outside %d-%d", port, services.PortRangeLow, services.PortRangeHigh)
		}
	}
	again, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := again.Port("web")
	if !ok || got != web {
		t.Errorf("reloaded web port = %d,%v; want %d", got, ok, web)
	}
}

// CONTRACT: a port something else already holds is skipped, not handed out.
func TestAssignSkipsPortsAlreadyBound(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "ports.json")
	p, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	busy := services.PortRangeLow
	port, err := p.Assign("web", func(candidate int) bool { return candidate != busy })
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if port == busy {
		t.Errorf("Assign handed out the bound port %d", port)
	}
}

// CONTRACT: a synced server forwards a loopback port straight into the
// service's socket, and drops the listener when the service goes away.
func TestServerForwardsLoopbackIntoTheSocket(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, forwarder.SocketName("web"))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "in the box")
	}))
	writeDescriptor(t, dir, "web", forwarder.TypeWebUI, 5173, false)

	ports, err := services.LoadPorts(filepath.Join(dir, "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	live := true
	src := func() ([]services.Service, error) {
		if !live {
			return nil, nil
		}
		return []services.Service{{Name: "web", Type: forwarder.TypeWebUI, BoxPort: 5173, Socket: sock, Instance: "demo-0"}}, nil
	}
	srv := services.NewServer(src, ports, io.Discard)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	serving := srv.Serving()
	if len(serving) != 1 {
		t.Fatalf("Serving = %+v, want one", serving)
	}
	// A pooled connection would answer from the box even after the listener is
	// gone, which is not what is being asked; every request here is its own
	// connection.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(serving[0].URL())
	if err != nil {
		t.Fatalf("through the host port: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "in the box" {
		t.Errorf("body = %q, want %q", body, "in the box")
	}

	live = false
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := srv.Serving(); len(got) != 0 {
		t.Errorf("Serving = %+v after the service went away, want none", got)
	}
	if _, err := client.Get(serving[0].URL()); err == nil {
		t.Errorf("the host port still answers after the service went away")
	}
}

// CONTRACT: the index links a webui and only names a general service — the
// type changes the rendering and nothing else.
func TestIndexLinksWebUIsOnly(t *testing.T) {
	dir := shortTempDir(t)
	ports, err := services.LoadPorts(filepath.Join(dir, "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	src := func() ([]services.Service, error) {
		return []services.Service{
			{Name: "web", Type: forwarder.TypeWebUI, Socket: filepath.Join(dir, "web.sock")},
			{Name: "db", Type: forwarder.TypeGeneral, Socket: filepath.Join(dir, "db.sock")},
		}, nil
	}
	srv := services.NewServer(src, ports, io.Discard)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	page := renderIndex(t, srv)
	webPort, _ := ports.Port("web")
	dbPort, _ := ports.Port("db")
	if !strings.Contains(page, "<a href=\"http://127.0.0.1:"+strconv.Itoa(webPort)+"\">web</a>") {
		t.Errorf("index does not link the webui:\n%s", page)
	}
	if strings.Contains(page, "<a href=\"http://127.0.0.1:"+strconv.Itoa(dbPort)+"\">db</a>") {
		t.Errorf("index links a general service:\n%s", page)
	}
	if !strings.Contains(page, "127.0.0.1:"+strconv.Itoa(dbPort)) {
		t.Errorf("index omits the general service's address:\n%s", page)
	}
}

func TestIndexSaysSoWhenNothingIsPublished(t *testing.T) {
	ports, err := services.LoadPorts(filepath.Join(shortTempDir(t), "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	srv := services.NewServer(func() ([]services.Service, error) { return nil, nil }, ports, io.Discard)
	if page := renderIndex(t, srv); !strings.Contains(page, "No box is publishing") {
		t.Errorf("empty index = %q", page)
	}
}

// renderIndex asks the server for its index page.
func renderIndex(t *testing.T, srv *services.Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}

// serveBody runs an HTTP server answering body on sock, torn down at test end.
func serveBody(t *testing.T, sock, body string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
}

// getBody fetches a URL on a connection of its own, so a pooled connection to a
// socket that has since moved cannot answer for the new one.
func getBody(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// CONTRACT: a re-upped box publishes the same name from a NEW node directory,
// and the port a human wrote down follows it there — the old socket is dead and
// forwarding into it for ever would make the stable port worthless.
func TestServiceThatMovesSocketKeepsItsPortAndReachesTheNewBox(t *testing.T) {
	dir := shortTempDir(t)
	oldSock, newSock := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	serveBody(t, oldSock, "old box")
	serveBody(t, newSock, "new box")
	ports, err := services.LoadPorts(filepath.Join(dir, "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	sock := oldSock
	src := func() ([]services.Service, error) {
		return []services.Service{{Name: "web", Type: forwarder.TypeWebUI, Socket: sock}}, nil
	}
	srv := services.NewServer(src, ports, io.Discard)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	url := srv.Serving()[0].URL()
	if got := getBody(t, url); got != "old box" {
		t.Fatalf("before the move = %q, want %q", got, "old box")
	}
	sock = newSock
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := srv.Serving()[0].URL(); got != url {
		t.Errorf("the port moved with the box: %s, want the same %s", got, url)
	}
	if got := getBody(t, url); got != "new box" {
		t.Errorf("after the move = %q, want %q — the port still forwards into the dead socket", got, "new box")
	}
}

// CONTRACT: one name is one host port. A second box claiming a name is REPORTED,
// and never presented as sharing the first one's address.
func TestASecondBoxClaimingANameIsReportedAndNotServed(t *testing.T) {
	dir := shortTempDir(t)
	first, second := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	serveBody(t, first, "first box")
	serveBody(t, second, "second box")
	marked := services.MarkConflicts([]services.Service{
		{Name: "web", Node: "box-a", Socket: first},
		{Name: "web", Node: "box-b", Socket: second},
	})
	if marked[0].Conflict || !marked[1].Conflict {
		t.Fatalf("MarkConflicts = %+v, want only the second flagged", marked)
	}
	ports, err := services.LoadPorts(filepath.Join(dir, "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	var log strings.Builder
	srv := services.NewServer(func() ([]services.Service, error) { return marked, nil }, ports, &log)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	serving := srv.Serving()
	if len(serving) != 1 || serving[0].Node != "box-a" {
		t.Fatalf("Serving = %+v, want only the first claimant", serving)
	}
	if got := getBody(t, serving[0].URL()); got != "first box" {
		t.Errorf("served body = %q, want the first claimant's", got)
	}
	if !strings.Contains(log.String(), "also published by box-b") {
		t.Errorf("the collision was not reported: %q", log.String())
	}
	// Reported once, not once per scan.
	before := strings.Count(log.String(), "also published by")
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if after := strings.Count(log.String(), "also published by"); after != before {
		t.Errorf("the collision was reported again on the next scan (%d → %d)", before, after)
	}
}

// CONTRACT: an assignment that can no longer be bound — something else took the
// port while nothing was serving — is MOVED and persisted, not retried for ever.
func TestAnUnbindableAssignmentIsMovedAndPersisted(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "ports.json")
	ports, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	assigned, err := ports.Assign("web", services.FreeOnLoopback)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	squatter, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(assigned))
	if err != nil {
		t.Fatalf("squat on %d: %v", assigned, err)
	}
	defer squatter.Close()

	sock := filepath.Join(dir, "a.sock")
	serveBody(t, sock, "in the box")
	src := func() ([]services.Service, error) {
		return []services.Service{{Name: "web", Type: forwarder.TypeWebUI, Socket: sock}}, nil
	}
	srv := services.NewServer(src, ports, io.Discard)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	serving := srv.Serving()
	if len(serving) != 1 {
		t.Fatalf("Serving = %+v, want the service moved to a free port", serving)
	}
	if serving[0].Port == assigned {
		t.Fatalf("still on the taken port %d", assigned)
	}
	if got := getBody(t, serving[0].URL()); got != "in the box" {
		t.Errorf("moved port = %q, want the service", got)
	}
	reloaded, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, _ := reloaded.Port("web"); got != serving[0].Port {
		t.Errorf("persisted port = %d, want the new one %d", got, serving[0].Port)
	}
}

// CONTRACT: the store survives being written and read at once — one dabs
// process serves while another lists, and a torn file would fail the reader.
func TestPortsStoreSurvivesConcurrentWritersAndReaders(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "ports.json")
	ports, err := services.LoadPorts(path)
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if _, err := ports.Assign("svc"+strconv.Itoa(i), func(int) bool { return true }); err != nil {
				errs <- err
			}
		}(i)
		go func() {
			defer wg.Done()
			if _, err := services.LoadPorts(path); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent store use: %v", err)
	}
}

// CONTRACT: the box writes its own descriptor and its own filenames, so what
// the host prints is reduced to printable characters — a name carrying a
// newline would otherwise forge a row in the listing, whatever the forwarder
// allows.
func TestScanDirStripsUnprintablesFromWhatTheHostPrints(t *testing.T) {
	dir := shortTempDir(t)
	forged := "web\n  evil  general  127.0.0.1:1  up"
	writeDescriptor(t, dir, forged, "webui\x1b[31m", 5173, true)
	found, err := services.ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("ScanDir = %+v, want one service", found)
	}
	if strings.ContainsAny(found[0].Name, "\n\r\x1b") {
		t.Errorf("Name = %q, still carries control characters", found[0].Name)
	}
	if strings.ContainsAny(found[0].Type, "\n\r\x1b") {
		t.Errorf("Type = %q, still carries control characters", found[0].Type)
	}
	// The socket keeps the raw name — it is a real path and has to be dialable.
	if !strings.Contains(found[0].Socket, forged) {
		t.Errorf("Socket = %q, want the raw filename", found[0].Socket)
	}
	if len([]rune(services.Printable(strings.Repeat("w", 200)))) != services.MaxCellLen {
		t.Errorf("Printable does not cap at %d", services.MaxCellLen)
	}
}

// CONTRACT: a service that is published and NOT served is exactly what someone
// opens the index to find out about — it is named there, without a link.
func TestIndexNamesConflictsWithoutLinkingThem(t *testing.T) {
	dir := shortTempDir(t)
	ports, err := services.LoadPorts(filepath.Join(dir, "ports.json"))
	if err != nil {
		t.Fatalf("LoadPorts: %v", err)
	}
	src := func() ([]services.Service, error) {
		return services.MarkConflicts([]services.Service{
			{Name: "web", Type: forwarder.TypeWebUI, Node: "box-a", Socket: filepath.Join(dir, "a.sock")},
			{Name: "web", Type: forwarder.TypeWebUI, Node: "box-b", Socket: filepath.Join(dir, "b.sock")},
		}), nil
	}
	srv := services.NewServer(src, ports, io.Discard)
	if err := srv.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	page := renderIndex(t, srv)
	if !strings.Contains(page, "conflict: also published by box-b") {
		t.Errorf("the index hides the conflict:\n%s", page)
	}
	if strings.Count(page, "<a href=") != 1 {
		t.Errorf("the index links something it does not serve:\n%s", page)
	}
}
