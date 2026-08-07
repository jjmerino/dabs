package door

import (
	"io"
	"net"
)

// halfCloser is a connection that can close one direction while the other keeps
// flowing — what a tunnel needs to pass an EOF through without tearing the
// whole connection down.
type halfCloser interface {
	io.ReadWriter
	CloseWrite() error
}

// Couple pumps bytes both ways between two connections and returns once BOTH
// directions have finished. EOF on one side half-closes the other, so a client
// that shuts down its write side after the request still receives the whole
// response.
func Couple(a, b net.Conn) {
	ha, aok := a.(halfCloser)
	hb, bok := b.(halfCloser)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		if bok {
			_ = hb.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		if aok {
			_ = ha.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
