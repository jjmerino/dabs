// Package tui holds terminal-UI helpers beyond plain printing — spinners,
// progress, and the like. Anything fancier than fmt.Print lives here so the
// actions stay about logic, not animation.
package tui

import (
	"fmt"
	"os"
	"time"
)

// Spinner animates "<frame> loading <label>…" on stderr until the returned
// stop func is called. It is a no-op when stderr is not a terminal
// (piped/redirected), so captured output stays clean.
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
				fmt.Fprintf(os.Stderr, "\r%c loading %s…", frames[i%len(frames)], label)
				i++
			}
		}
	}()
	// stop is synchronous: it waits for the line to be cleared, so the
	// caller's next output can't race the spinner's final frame.
	return func() {
		close(done)
		<-cleared
	}
}
