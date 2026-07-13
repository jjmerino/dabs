// Per-command help. `dabs <cmd> --help` (or -h) prints THAT command's own
// usage — its argument shape and its own flags — and nothing else: no
// top-level command menu. The render is driven by the command's entry in the
// Commands table (Args, Help) plus, for flag-bearing commands, the command's
// own *flag.FlagSet (its flags are the single source of truth).
package cli

import (
	"flag"
	"strings"

	"github.com/jjmerino/dabs/core/tui"
)

// HelpRequestedError signals that the user asked for a command's own help
// (-h/--help). It carries the fully rendered usage text; main prints it to
// stdout and exits 0. It is NOT a usage error — no top-level menu is shown.
type HelpRequestedError struct{ Text string }

func (HelpRequestedError) Error() string { return "help requested" }

// wantsHelp reports whether the first argument is a help flag. Help is only
// recognized as the FIRST token so that pass-through commands (exec/recipe)
// still forward a later `--help` to the command run in the box.
func wantsHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}

// helpText renders one command's own help: a usage line, its one-line
// description, and (from fs, when non-nil) each of its own flags. fs is the
// command's real parser FlagSet, so the flag list can never drift from what
// the command actually accepts.
func helpText(name string, fs *flag.FlagSet) string {
	doc := commandDocs[name]
	var b strings.Builder
	b.WriteString(tui.Heading("usage:") + " dabs " + doc.Args + "\n")
	if doc.Help != "" {
		b.WriteString(tui.Indent(doc.Help, 2) + "\n")
	}
	if rows := flagRows(fs); len(rows) > 0 {
		b.WriteString("\n" + tui.Heading("flags:") + "\n")
		b.WriteString(tui.Indent(tui.Rows(nil, rows), 2) + "\n")
	}
	return b.String()
}

// flagRows turns a FlagSet's flags into [name, description] rows, sorted by
// name (flag.VisitAll already visits in lexical order).
func flagRows(fs *flag.FlagSet) [][]string {
	if fs == nil {
		return nil
	}
	var rows [][]string
	fs.VisitAll(func(f *flag.Flag) {
		rows = append(rows, []string{tui.Accent("--" + f.Name), f.Usage})
	})
	return rows
}
