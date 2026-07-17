// Package tui holds terminal-UI helpers beyond plain printing — the palette,
// the confirmation prompt, spinners, and the string-returning render helpers
// (see style.go). Anything fancier than fmt.Print lives here so the actions stay
// about logic, not presentation.
package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
)

// interactive reports whether both stdin and stderr are terminals — the
// precondition for a huh prompt or an animated spinner. When either is
// redirected we stay in plain, non-animated mode so captured output is clean.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stderr} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// Interactive reports whether there is a person here to answer. Callers that must
// DEFAULT to keeping something check this before asking: Confirm blocks on a read
// when stdin is a pipe, and a reap that waits forever for an answer nobody can
// give is worse than one that keeps the files.
func Interactive() bool { return interactive() }

// Confirm shows prompt, then asks the user to proceed, returning true only for
// an explicit yes. On a terminal it renders prompt inside a framed box and runs
// a huh yes/no confirm; everything is drawn on stderr so a captured stdout (the
// command's own output) stays clean. When stdin/stderr is not a terminal it
// falls back to a plain "Proceed? [y/N]" line-read — and a non-answer (EOF /
// piped) is a no, keeping the original default-deny contract.
//
// This is the look-before-run gate: anything that runs a caller-supplied
// command routes through it.
func Confirm(prompt string) bool {
	if !interactive() {
		fmt.Fprintln(os.Stderr, prompt)
		fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		// Piped stdin echoes nothing, so the prompt line is still open — close
		// it, or whatever prints next runs into "Proceed? [y/N] ".
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return false // no answer (EOF / not a terminal) is a no — default-deny
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
