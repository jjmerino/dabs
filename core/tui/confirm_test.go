package tui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// CONTRACT: the look-before-run gate never blocks a caller that cannot answer.
// A stdin that is a pipe nobody writes to — what an agent harness or a CI step
// hands a child process — has no answer coming, so Confirm gives the
// default-deny answer promptly. This pins the hang where a scripted
// `dabs recipe <name> <cmd…>` waited forever on the confirmation.
func TestConfirmDeclinesWithoutBlockingWhenStdinIsNotATerminal(t *testing.T) {
	// An open pipe with no writer on the other end: a read would block until the
	// deadline of the test binary itself.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer errR.Close()
	defer errW.Close()

	origIn, origErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = stdinR, errW
	defer func() { os.Stdin, os.Stderr = origIn, origErr }()

	done := make(chan bool, 1)
	go func() { done <- Confirm("recipe \"hello\"\ncommand: sh -c 'echo hi'") }()

	select {
	case proceed := <-done:
		if proceed {
			t.Fatalf("Confirm approved a run nobody consented to")
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Confirm blocked on a stdin nobody can answer")
	}

	errW.Close()
	buf := make([]byte, 4096)
	n, _ := errR.Read(buf)
	out := string(buf[:n])
	if !strings.Contains(out, "sh -c 'echo hi'") {
		t.Fatalf("the refused command must still be shown; got:\n%s", out)
	}
}

// CONTRACT: a pipe that DOES carry an answer is still honoured — bounding the
// wait must not cost `echo y | dabs …` its yes.
func TestConfirmHonoursAnAnswerOnAPipe(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer stdinR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer errR.Close()
	defer errW.Close()
	go func() { io.Copy(io.Discard, errR) }()

	if _, err := stdinW.WriteString("y\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdinW.Close()

	origIn, origErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = stdinR, errW
	defer func() { os.Stdin, os.Stderr = origIn, origErr }()

	if !Confirm("recipe \"hello\"") {
		t.Fatalf("a piped \"y\" must approve the run")
	}
}
