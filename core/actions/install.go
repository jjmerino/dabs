package actions

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jjmerino/dabs/core/params"
	"github.com/jjmerino/dabs/core/tui"
)

// harnessTarget describes where a harness integration is shipped from (in
// this dabs install) and where it installs to on the user's machine.
type harnessTarget struct {
	name    string
	what    string // "extension" | "skill"
	dest    func() (string, error)
	srcRel  string // path within the dabs source tree
	summary string
}

func harnessTargets() map[string]harnessTarget {
	return map[string]harnessTarget{
		"pi": {
			name: "pi", what: "extension", srcRel: "harnesses/pi/extensions/dabash",
			dest:    func() (string, error) { return home(".pi", "extensions", "dabash") },
			summary: "dabash pi extension — a sandbox-only shell tool",
		},
		"claude": {
			name: "claude", what: "skill", srcRel: "harnesses/claude/skills/dabs",
			dest:    func() (string, error) { return home(".claude", "skills", "dabs") },
			summary: "dabs Claude skill — run commands and sub-agents in a dabs box",
		},
	}
}

// Install copies a harness integration into place after y/n confirmation.
// With no harness named, it prints instructions.
func (r Real) Install(p params.Install) error {
	targets := harnessTargets()
	if p.Harness == "" {
		return printInstallHelp(targets)
	}
	t, ok := targets[p.Harness]
	if !ok {
		return fmt.Errorf("unknown harness %q (known: pi, claude)", p.Harness)
	}
	dst, err := t.dest()
	if err != nil {
		return err
	}
	if !tui.Confirm(fmt.Sprintf("Install %s\n  to %s", t.summary, dst)) {
		fmt.Fprintln(os.Stdout, tui.Muted("cancelled"))
		return nil
	}
	if err := writeFS(r.harness, t.srcRel, dst); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, tui.Success("installed %s %s %s %s", t.name, t.what, tui.Arrow(), dst))
	return nil
}

// Uninstall removes a previously installed harness integration.
func (r Real) Uninstall(p params.Uninstall) error {
	targets := harnessTargets()
	t, ok := targets[p.Harness]
	if !ok {
		return fmt.Errorf("unknown harness %q (known: pi, claude)", p.Harness)
	}
	dst, err := t.dest()
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		fmt.Fprintln(os.Stdout, tui.Muted("%s %s not installed (%s)", t.name, t.what, dst))
		return nil
	}
	if !tui.Confirm(fmt.Sprintf("Remove %s %s at %s", t.name, t.what, dst)) {
		fmt.Fprintln(os.Stdout, tui.Muted("cancelled"))
		return nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	fmt.Fprintln(os.Stdout, tui.Success("removed %s %s", t.name, t.what))
	return nil
}

func printInstallHelp(targets map[string]harnessTarget) error {
	fmt.Fprintln(os.Stdout, tui.Heading("dabs install <harness>")+tui.Muted(" — install the dabash integration for a harness."))
	fmt.Fprintln(os.Stdout)
	for _, name := range []string{"pi", "claude"} {
		t := targets[name]
		dst, _ := t.dest()
		fmt.Fprintf(os.Stdout, "  %s %s\n", tui.Accent(fmt.Sprintf("dabs install %-7s", name)), t.summary)
		fmt.Fprintf(os.Stdout, "  %-20s%s %s\n", "", tui.Arrow(), tui.Muted("%s", dst))
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, tui.Muted("Each asks for confirmation. Remove with `dabs uninstall <harness>`."))
	return nil
}

func home(parts ...string) (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	return filepath.Join(append([]string{h}, parts...)...), nil
}

// writeFS copies the srcRel subtree of the embedded harness FS to dst,
// replacing the destination.
func writeFS(fsys fs.FS, srcRel, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	return fs.WalkDir(fsys, srcRel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
		rel, err := filepath.Rel(srcRel, p)
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		return os.WriteFile(out, data, 0o644)
	})
}
