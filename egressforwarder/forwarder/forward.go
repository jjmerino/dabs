// Package forwarder is the in-box half of proxy egress: a single-purpose static
// binary (built from cmd/forward, carried by dabs as embedded bytes) plus the
// contract the drivers use to mount and invoke it. A proxy box's only way out is
// a host proxy socket mounted into its filesystem, but proxy-speaking programs
// want a TCP address in HTTP_PROXY — so the mounted forwarder listens on
// loopback, pipes each connection into the socket, and (given a command after
// `--`) execs the box's real command as its child AFTER the listener is bound,
// so the command can never race a proxy that is not yet there. It is plain
// plumbing: no policy, no parsing of what flows through, and — unlike a mounted
// dabs — nothing but the forwarder.
package forwarder

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
)

// The in-box contract, identical in every proxy box whatever the host looks
// like: the host proxy's unix socket lands at SockPath, the forwarder binary at
// ForwardPath, and proxy-speaking programs are pointed at 127.0.0.1:Port. One
// fixed port keeps the env vars predictable; an image that serves that port
// itself on loopback cannot use proxy egress. The forwarder is a SEPARATE,
// single-purpose static binary (cmd/forward) — dabs carries it as embedded
// bytes and drops a copy into the box, so the box never gets the dabs CLI.
const (
	Port        = 18080
	SockPath    = "/run/dabs/egress.sock"
	ForwardPath = "/run/dabs/forward"
)

// WrapCommand rewrites a box's command so the forwarder brackets it: the
// mounted forwarder binary binds the loopback listener, THEN runs the original
// argv as its child, serving the proxy bridge for exactly as long as the
// command lives.
func WrapCommand(argv []string) []string {
	return append([]string{ForwardPath, SockPath, strconv.Itoa(Port), "--"}, argv...)
}

// Materialize writes the embedded forwarder binary into dir as an executable and
// returns its path, ready for a driver to mount at ForwardPath. It fails if this
// dabs was built without the forwarder embedded (see EmbeddedBinary).
func Materialize(dir string) (string, error) {
	b, err := EmbeddedBinary()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "forward")
	if err := os.WriteFile(path, b, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// Run binds 127.0.0.1:port, serves the bridge to the unix socket at sockPath,
// and — when argv is non-empty — runs argv as a child with inherited stdio,
// returning its exit code once it finishes. With no argv it serves forever.
func Run(sockPath string, port int, argv []string) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 1, err
	}
	defer ln.Close()
	go serve(ln, sockPath)
	if len(argv) == 0 {
		select {} // serve until killed
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	// The forwarder stands between the box's real command and whoever signals
	// it (Ctrl-C's process group, `docker stop`'s TERM to PID 1) — relay
	// signals to the child and live exactly as long as it does, so the command
	// shuts down as gracefully as it would unwrapped.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	signal.Stop(sigs)
	close(sigs)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal()), nil
			}
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// serve runs the accept loop until the listener fails.
func serve(ln net.Listener, sockPath string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go pipe(conn.(*net.TCPConn), sockPath)
	}
}

// pipe couples one TCP connection to one fresh unix connection. EOF on one
// side half-closes the other, and the tunnel stays up until BOTH directions
// finish — a client that shuts down its write side after the request still
// receives the whole response.
func pipe(conn *net.TCPConn, sockPath string) {
	defer conn.Close()
	sock, err := net.Dial("unix", sockPath)
	if err != nil {
		return
	}
	usock := sock.(*net.UnixConn)
	defer usock.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(usock, conn)
		_ = usock.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, usock)
		_ = conn.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
}
