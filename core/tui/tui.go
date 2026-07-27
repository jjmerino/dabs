// Package tui holds terminal-UI helpers beyond plain printing — the palette,
// the confirmation prompt, spinners, and the string-returning render helpers
// (see style.go). Anything fancier than fmt.Print lives here so the actions stay
// about logic, not presentation.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
)

// isTerminal reports whether f is a character device. Terminals are character
// devices; so is /dev/null, which reads as an immediate EOF and so answers just
// as promptly. A pipe or a regular file is not.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether both stdin and stderr are terminals — the
// precondition for a huh prompt or an animated spinner. When either is
// redirected we stay in plain, non-animated mode so captured output is clean.
func interactive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

// Interactive reports whether there is a person here to answer. Callers that
// must DEFAULT to keeping something check this before asking, and say what they
// would have taken instead: off a terminal, Confirm can only ever come back
// with the default-deny answer.
func Interactive() bool { return interactive() }

// answerWait is how long the plain prompt waits for a line on a stdin that no
// terminal is attached to. It is a bound on a wait, not a race: an answer that
// is already coming arrives in microseconds.
const answerWait = 3 * time.Second

// errNoAnswer is a wait that ended with nothing on stdin — no line, no EOF.
var errNoAnswer = errors.New("no answer on stdin")

// readAnswer reads one line from stdin for the plain prompt. On a character
// device it waits as long as it takes — a person is typing, or /dev/null ends
// it at once. Otherwise the read runs on its own goroutine and the wait for it
// stops at answerWait, so a stream carrying no answer produces errNoAnswer
// instead of holding the process. The blocked read is left to the exiting
// process; nothing else here reads stdin.
func readAnswer() (string, error) {
	if isTerminal(os.Stdin) {
		return bufio.NewReader(os.Stdin).ReadString('\n')
	}
	type answer struct {
		line string
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		ch <- answer{line, err}
	}()
	select {
	case a := <-ch:
		return a.line, a.err
	case <-time.After(answerWait):
		return "", errNoAnswer
	}
}

// Confirm shows prompt, then asks the user to proceed, returning true only for
// an explicit yes. On a terminal it renders prompt inside a framed box and runs
// a huh yes/no confirm; everything is drawn on stderr so a captured stdout (the
// command's own output) stays clean. When stdin/stderr is not a terminal it
// falls back to a plain "Proceed? [y/N]" line-read — and a non-answer (EOF) is
// a no, keeping the original default-deny contract.
//
// The wait for that line is BOUNDED when stdin is not a character device. A
// pipe carrying an answer (`echo y | dabs …`) delivers it at once; an inherited
// pipe nobody writes to — what an agent harness or a CI step hands a child —
// never delivers anything, and an unbounded read there waits forever. After
// answerWait such a caller gets the default-deny answer it was always going to
// get, instead of a hung command.
//
// This is the look-before-run gate: anything that runs a caller-supplied
// command routes through it.
func Confirm(prompt string) bool {
	if !interactive() {
		fmt.Fprintln(os.Stderr, prompt)
		fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
		line, err := readAnswer()
		// Piped stdin echoes nothing, so the prompt line is still open — close
		// it, or whatever prints next runs into "Proceed? [y/N] ".
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return false // no answer (EOF / nobody there) is a no — default-deny
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "y", "yes":
			return true
		default:
			return false
		}
	}

	fmt.Fprintln(os.Stderr, Box(prompt))
	proceed := false
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Proceed?").
				Affirmative("Yes").
				Negative("No").
				Value(&proceed),
		),
	).WithOutput(os.Stderr).WithTheme(huh.ThemeCharm())
	if err := form.Run(); err != nil {
		return false // aborted (ctrl-c / error) is a no — default-deny
	}
	return proceed
}

// Spinner animates "<frame> <label>…" on stderr until the returned stop func is
// called. It is a no-op when stderr is not a terminal (piped/redirected), so
// captured output stays clean.
func Spinner(label string) (stop func()) {
	if fi, err := os.Stderr.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return func() {}
	}
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	done := make(chan struct{})
	cleared := make(chan struct{})
	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\033[K") // clear the line
				close(cleared)
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r%s %s…", Accent(string(frames[i%len(frames)])), Muted("%s", label))
				i++
			}
		}
	}()
	// stop is synchronous: it waits for the line to be cleared, so the caller's
	// next output can't race the spinner's final frame.
	return func() {
		close(done)
		<-cleared
	}
}
