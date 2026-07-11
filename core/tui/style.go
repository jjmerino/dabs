package tui

// This file is the look of dabs: one palette, a handful of semantic styles, and
// the string-returning render helpers the actions call. Keeping every color and
// glyph here (not sprinkled through core/actions) is what lets the actions stay
// about logic — they say WHAT to report; tui decides how it looks.
//
// Everything returns a string rather than printing, so callers keep owning the
// io.Writer (stdout vs stderr) and the output stays testable. lipgloss disables
// color automatically when the destination is not a terminal, so piped/captured
// output degrades to clean, uncolored (but still aligned) text.

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// stdoutIsTTY gates the pretty look. The style helpers below render the
// command's OWN output (stdout), so when stdout is not a terminal — piped,
// redirected, captured by a script — they must degrade to plain text: no
// glyphs, no ANSI. lipgloss already drops color for a non-TTY, but the literal
// Unicode glyphs (✓ ✗ ⚠) survive and corrupt machine parsing (e.g. a script
// doing `dabs up | awk '{print $1}'` would grab the ✓). Deciding once here
// keeps piped output clean and parsable while a real terminal stays pretty.
var stdoutIsTTY = isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

// The palette. Adaptive so a light terminal and a dark terminal each get a
// legible shade; kept deliberately small so the UI reads as one system.
var (
	accent  = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"} // brand violet
	green   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	amber   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	red     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	grayCol = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
)

// Semantic styles built from the palette.
var (
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	successStyle = lipgloss.NewStyle().Foreground(green)
	warnStyle    = lipgloss.NewStyle().Foreground(amber)
	dangerStyle  = lipgloss.NewStyle().Foreground(red)
	mutedStyle   = lipgloss.NewStyle().Foreground(grayCol)
	badgeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(accent).Padding(0, 1)
	arrowStyle   = lipgloss.NewStyle().Foreground(grayCol)
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)
)

// Glyphs. Unicode, so they survive being piped; color comes from the styles.
const (
	checkGlyph = "✓"
	crossGlyph = "✗"
	warnGlyph  = "⚠"
	arrowGlyph = "→"
	dotGlyph   = "•"
)

// Heading styles a section title (fleet member, install header, …).
func Heading(s string) string {
	if !stdoutIsTTY {
		return s
	}
	return headingStyle.Render(s)
}

// Success renders a green "✓ <msg>" line — the shape every "X up / built /
// removed" confirmation takes. Piped (non-TTY), it drops the glyph and color so
// the line is just "<msg>" and stays machine-parsable.
func Success(format string, a ...any) string {
	if !stdoutIsTTY {
		return fmt.Sprintf(format, a...)
	}
	return successStyle.Render(checkGlyph+" ") + fmt.Sprintf(format, a...)
}

// Failure renders a red "✗ <msg>" line. Piped, drops the glyph and color.
func Failure(format string, a ...any) string {
	if !stdoutIsTTY {
		return fmt.Sprintf(format, a...)
	}
	return dangerStyle.Render(crossGlyph+" ") + fmt.Sprintf(format, a...)
}

// Warn renders an amber "⚠ <msg>" line for non-fatal notices (unreachable
// server, unreadable worktree). Piped, drops the glyph and color.
func Warn(format string, a ...any) string {
	if !stdoutIsTTY {
		return fmt.Sprintf(format, a...)
	}
	return warnStyle.Render(warnGlyph + " " + fmt.Sprintf(format, a...))
}

// Muted dims secondary text ("(no instances)", details, help lines).
func Muted(format string, a ...any) string {
	if !stdoutIsTTY {
		return fmt.Sprintf(format, a...)
	}
	return mutedStyle.Render(fmt.Sprintf(format, a...))
}

// Accent colors a fragment in the brand color without bolding — for values a
// reader's eye should land on (recipe names, instance names).
func Accent(s string) string {
	if !stdoutIsTTY {
		return s
	}
	return lipgloss.NewStyle().Foreground(accent).Render(s)
}

// Badge renders a small inverse tag, e.g. the "default" marker on a recipe.
func Badge(s string) string {
	if !stdoutIsTTY {
		return s
	}
	return badgeStyle.Render(s)
}

// Arrow is the styled "→" used in "<origin> → <path>" source lines.
func Arrow() string {
	if !stdoutIsTTY {
		return arrowGlyph
	}
	return arrowStyle.Render(arrowGlyph)
}

// Dot is the styled bullet used to lead source/detail lines.
func Dot() string {
	if !stdoutIsTTY {
		return dotGlyph
	}
	return mutedStyle.Render(dotGlyph)
}

// Status colors a sandbox status string by a coarse reading of its text:
// running-ish is green, stopped/exited/dead is red, anything else stays muted.
func Status(s string) string {
	if !stdoutIsTTY {
		return s
	}
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "run") || strings.Contains(l, "up"):
		return successStyle.Render(s)
	case strings.Contains(l, "exit") || strings.Contains(l, "dead") || strings.Contains(l, "stop") || strings.Contains(l, "down"):
		return dangerStyle.Render(s)
	default:
		return mutedStyle.Render(s)
	}
}

// WorkState colors a worktree state cell: "HAS WORK" draws the eye (accent),
// "clean" recedes (muted).
func WorkState(hasWork bool) string {
	label := "clean"
	if hasWork {
		label = "HAS WORK"
	}
	if !stdoutIsTTY {
		return label
	}
	if hasWork {
		return headingStyle.Render(label)
	}
	return mutedStyle.Render(label)
}

// Box wraps a block in a rounded accent border — the frame around a
// look-before-run summary.
func Box(s string) string {
	if !stdoutIsTTY {
		return s
	}
	return boxStyle.Render(s)
}

// Rows aligns a grid of cells into columns. headers may be nil for no header
// row. Cells may already be styled: column widths are measured by visible width
// (lipgloss.Width ignores ANSI), so a green status or a badge still lines up.
func Rows(headers []string, rows [][]string) string {
	n := len(headers)
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	if n == 0 {
		return ""
	}
	widths := make([]int, n)
	measure := func(cells []string) {
		for i, c := range cells {
			if w := lipgloss.Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if headers != nil {
		measure(headers)
	}
	for _, r := range rows {
		measure(r)
	}

	pad := func(s string, w int) string { return s + strings.Repeat(" ", w-lipgloss.Width(s)) }
	var b strings.Builder
	line := func(cells []string, header bool) {
		for i := 0; i < n; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if header {
				cell = headingStyle.Render(cell)
			}
			if i < n-1 { // pad interior columns; trailing column needs no padding
				cell = pad(cell, widths[i])
				b.WriteString(cell)
				b.WriteString("  ")
			} else {
				b.WriteString(cell)
			}
		}
		b.WriteByte('\n')
	}
	if headers != nil {
		line(headers, true)
	}
	for _, r := range rows {
		line(r, false)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Indent shifts every line of s right by n spaces.
func Indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
