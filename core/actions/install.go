package actions

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjmerino/dabs/core/params"
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
	fmt.Printf("Install %s\n  to %s\nProceed? [y/N] ", t.summary, dst)
	if !confirm() {
		fmt.Println("cancelled")
		return nil
	}
	if err := writeFS(r.harness, t.srcRel, dst); err != nil {
		return err
	}
	fmt.Printf("installed %s %s → %s\n", t.name, t.what, dst)
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
		fmt.Printf("%s %s not installed (%s)\n", t.name, t.what, dst)
		return nil
	}
	fmt.Printf("Remove %s %s at %s? [y/N] ", t.name, t.what, dst)
	if !confirm() {
		fmt.Println("cancelled")
		return nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	fmt.Printf("removed %s %s\n", t.name, t.what)
	return nil
}

func printInstallHelp(targets map[string]harnessTarget) error {
	fmt.Println("dabs install <harness> — install the dabash integration for a harness.")
	fmt.Println()
	for _, name := range []string{"pi", "claude"} {
		t := targets[name]
		dst, _ := t.dest()
		fmt.Printf("  dabs install %-7s %s\n", name, t.summary)
		fmt.Printf("  %-20s→ %s\n", "", dst)
	}
	fmt.Println()
	fmt.Println("Each asks for confirmation. Remove with `dabs uninstall <harness>`.")
	return nil
}

func home(parts ...string) (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("install: %w", err)
	}
	return filepath.Join(append([]string{h}, parts...)...), nil
}

func confirm() bool {
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(sc.Text()))
	return a == "y" || a == "yes"
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
