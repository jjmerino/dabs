package clidriver

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/sandbox"
)

// CONTRACT: DetachLine is what makes a detached start non-blocking — the line
// it renders must background the command (`&`) and take its stdout AND stderr
// away from the exec'd shell into the box's detached log. Without both, the
// driver's exec would sit on the command's pipes for as long as it runs.
func TestDetachLineBackgroundsAndRedirects(t *testing.T) {
	got := DetachLine([]string{"serve"})
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("DetachLine = %q, want an `sh -c <line>` argv", got)
	}
	line := got[2]
	if !strings.HasSuffix(strings.TrimSpace(line), "&") {
		t.Errorf("line %q does not background the command", line)
	}
	if !strings.Contains(line, ">"+sandbox.DetachedLogPath) || !strings.Contains(line, "2>&1") {
		t.Errorf("line %q does not send both streams to %s", line, sandbox.DetachedLogPath)
	}
	// The log dir is a host directory the caller bound in. Creating it here would
	// mask a missing bind and strand the output inside the box.
	if strings.Contains(line, "mkdir") {
		t.Errorf("line %q creates the log dir instead of relying on the bound one", line)
	}
}

// CONTRACT: the command reaches the box exactly as the recipe wrote it. A recipe
// command carrying spaces, quotes or shell metacharacters is one ARGUMENT, not
// several, and must not be re-split or interpreted by the shell that starts it.
func TestDetachLineQuotesEachArgument(t *testing.T) {
	line := DetachLine([]string{"sh", "-c", "echo 'a b' && sleep 100"})[2]
	if !strings.Contains(line, `'echo '\''a b'\'' && sleep 100'`) {
		t.Errorf("line %q does not carry the argument as one quoted word", line)
	}
}
