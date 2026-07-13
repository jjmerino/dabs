package tui

import (
	"strings"
	"testing"
)

// The test process's stdout is a pipe, not a terminal, so stdoutIsTTY is false
// and these assertions exercise the piped (non-TTY) degradation path — the same
// path a script capturing `dabs recipe --detach` output hits. On a real terminal the same
// helpers would prepend the glyph and wrap the text in ANSI color; here they
// must not, so machine parsing (`dabs recipe --detach | awk '{print $1}'`) is not corrupted.

func hasANSI(s string) bool { return strings.Contains(s, "\x1b[") }

func TestSuccessPlainWhenPiped(t *testing.T) {
	if stdoutIsTTY {
		t.Skip("stdout is a TTY; the piped-degradation path is not exercised")
	}
	got := Success("myproj-abc up")
	if strings.Contains(got, checkGlyph) {
		t.Errorf("Success piped output must not contain the %q glyph: %q", checkGlyph, got)
	}
	if hasANSI(got) {
		t.Errorf("Success piped output must not contain ANSI escapes: %q", got)
	}
	if got != "myproj-abc up" {
		t.Errorf("Success piped output = %q, want plain %q", got, "myproj-abc up")
	}
}

func TestStyleHelpersPlainWhenPiped(t *testing.T) {
	if stdoutIsTTY {
		t.Skip("stdout is a TTY; the piped-degradation path is not exercised")
	}
	cases := map[string]string{
		"Failure": Failure("boom"),
		"Warn":    Warn("heads up"),
		"Muted":   Muted("(none)"),
		"Accent":  Accent("name"),
		"Heading": Heading("Fleet"),
		"Badge":   Badge("default"),
		"Status":  Status("running"),
		"WorkNo":  WorkState(false),
		"WorkYes": WorkState(true),
		"Box":     Box("summary"),
		"Arrow":   Arrow(),
		"Dot":     Dot(),
	}
	for name, out := range cases {
		if hasANSI(out) {
			t.Errorf("%s piped output must not contain ANSI escapes: %q", name, out)
		}
		for _, g := range []string{checkGlyph, crossGlyph, warnGlyph} {
			if strings.Contains(out, g) {
				t.Errorf("%s piped output must not contain glyph %q: %q", name, g, out)
			}
		}
	}
}
