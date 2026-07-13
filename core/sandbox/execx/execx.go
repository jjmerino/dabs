// Package execx surfaces the failures of a box subprocess, one place, for
// every driver. Two policies live here because dabs runs two kinds of
// subprocess: the box's OWN command, whose non-zero exit is the user's result
// and not a dabs error, and dabs's own machinery, whose failure is a dabs
// error carrying the vendor CLI's output. stdlib-only, so every driver and
// main may import it without a cycle.
package execx

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// BoxErr surfaces a box command's failure: a non-zero EXIT is the command's
// own result, so the *exec.ExitError is returned BARE — the caller propagates
// the code and prints no dabs-error line. Anything else (the vendor CLI could
// not spawn the command) is a driver failure, wrapped under prefix with any
// captured output. prefix is the full context (e.g. "docker: exec in box-x");
// out is nil for a streamed command that captured nothing.
func BoxErr(prefix string, out []byte, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee
	}
	return WrapOut(prefix, out, err)
}

// WrapOut wraps a driver-machinery failure under prefix, appending the trimmed
// subprocess output when there is any — that output is usually the only
// explanation the vendor CLI gives. With no output it wraps err with %w so the
// chain stays inspectable; an empty prefix omits the leading context.
func WrapOut(prefix string, out []byte, err error) error {
	trimmed := strings.TrimSpace(string(out))
	switch {
	case prefix == "" && trimmed == "":
		return err
	case prefix == "":
		return fmt.Errorf("%v: %s", err, trimmed)
	case trimmed == "":
		return fmt.Errorf("%s: %w", prefix, err)
	default:
		return fmt.Errorf("%s: %v: %s", prefix, err, trimmed)
	}
}

// Stderr renders a failed Output() call with the subprocess's own stderr,
// which Output() strips into the *exec.ExitError. Without an ExitError (or an
// empty stderr) it falls back to the error's own text.
func Stderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return fmt.Sprintf("%v: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err.Error()
}
