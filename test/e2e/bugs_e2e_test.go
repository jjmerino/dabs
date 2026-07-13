//go:build e2e

// Regression tests for the edition-2 synthetic-user bug hunt (BUGHUNT.md,
// E2-*). Each test drives the real `dabs` CLI non-interactively — exactly the
// environment the hunt ran in — and asserts the CORRECTED behavior, so a test
// that is red pins an open bug and goes green when it is fixed.
//
// Grouped by the root pattern the hunt found, not by feature:
//
//	A  a verb reports success having done nothing / the DoS hang
//	B  `up` succeeds but the box cannot run its command
//	D  a check never looks at what is actually on disk
//	F  a name or path reaches a sink unsanitized
//	G  argv/driver injection and resolution
//
// Inner boxes here use image `dabs-e2e` (the only image staged in the e2e box:
// alpine + git + sh); there is no `shell` image and no docker in the box.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// bugRecipe writes a dabs.yaml into dir with one recipe (default) built on the
// staged dabs-e2e image, whose command is `sh` and whose sources are the given
// YAML block (already indented under `sources:`). An empty sources block yields
// a boxless-safe recipe with no mounts.
func bugRecipe(t *testing.T, dir, name, sourcesYAML string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "default: " + name + "\nrecipes:\n  " + name +
		":\n    image: dabs-e2e\n    command: [sh]\n"
	if sourcesYAML != "" {
		yaml += "    sources:\n" + sourcesYAML
	}
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nodesDir is where node records live in the box.
func nodesDir() string { return filepath.Join(home, ".dabs", "nodes") }

// --- Family A: reports success having done nothing / the DoS hang -------------

// E2-30: a node record that is a bare `{}` (empty id, empty parent) made
// byID[""] point at itself, so the parent-chain walk in `dabs ls` looped
// forever. `ls` must ignore an id-less record and return.
func TestLsDoesNotHangOnEmptyNodeRecord(t *testing.T) {
	clean(t)
	bad := filepath.Join(nodesDir(), "zz-e2ebug-empty")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "dabs-node.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(bad) })

	out, timedOut := runTimeout(20*time.Second, "dabs ls")
	if timedOut {
		t.Fatalf("dabs ls hung on an empty-{} node record (E2-30):\n%s", out)
	}
}

// --- Family G: argv/driver injection ------------------------------------------

// E2-7: `exec <box> -- <argv>` must run argv IN the box. A command whose first
// token starts with '-' (here `--version`) used to be swallowed by the sandbox
// driver's own option parser — `-- --version` printed bubblewrap's version
// instead of running `--version` in the box.
func TestExecArgvDoesNotLeakToDriver(t *testing.T) {
	clean(t)
	inst := up(t)
	out, _ := run("dabs exec " + inst + " -- --version")
	if containsFold(out, "bubblewrap") {
		t.Fatalf("`exec -- --version` reached the sandbox driver, not the box (E2-7):\n%s", out)
	}
}

// --- Family A: a destructive verb acted without consent (E2-1) -----------------

// E2-1: a single-node `rm` of a LIVE box used to stop+remove it with NO consent
// and exit 0 (the consent gate only fired for a cascade of >1 node). The agent
// chain was: boot a box, then `dabs rm <handle>` with no -y on a non-TTY stdin.
// Now that must KEEP the live box and exit non-zero; only `--yes` reaps it.
func TestRmLiveBoxWithoutConsentKeepsItE2E(t *testing.T) {
	clean(t)
	inst := up(t)
	out, code := run("dabs rm " + inst) // no --yes, non-interactive
	if code == 0 {
		t.Fatalf("rm of a live box without --yes exited 0 (E2-1):\n%s", out)
	}
	ls, _ := run("dabs ls")
	if !containsFold(ls, inst) || !containsFold(ls, "live") {
		t.Fatalf("a live box was reaped without consent (E2-1); ls:\n%s", ls)
	}
	if _, code := run("dabs rm " + inst + " --yes"); code != 0 {
		t.Fatalf("rm --yes should reap the box (E2-1)")
	}
}

// --- Family D/G: prune must not break a live box (E2-26) ------------------------

// E2-26: `dabs prune` (then `images prune`) reclaimed an image a LIVE box runs on,
// bricking it, with no warning. The agent chain: boot a box, then prune. Now prune
// must keep the image of any live box (naming it) unless --force, so the box keeps
// working.
func TestPruneKeepsImageOfLiveBoxE2E(t *testing.T) {
	clean(t)
	inst := up(t)
	pruneOut, _ := run("dabs prune") // no --force
	chk, code := run("dabs exec " + inst + " -- echo alive")
	if code != 0 || !containsFold(chk, "alive") {
		t.Fatalf("prune broke a live box (E2-26):\nprune=%s\nexec(rc=%d)=%s", pruneOut, code, chk)
	}
}

// --- Family H: the box's canonical handle and where its bytes live ------------

// E2-12: `--detach` printed `id: <instance>` — the driver instance name — though
// the NODE ID is the canonical, stable handle rm/exec resolve first. The boot
// output must show the node id as `id:`, keep the instance on its own line, and
// the node id must be a REAL handle: `dabs ls` shows it in the NODE column and
// `dabs rm <id> --yes` reaps the box.
func TestBootOutputShowsNodeIdE2E(t *testing.T) {
	clean(t)
	out, code := run("dabs recipe " + baseDir + " --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	nodeID := nodeIDFrom(t, out)
	inst := instanceFrom(t, out)
	if nodeID == inst {
		t.Fatalf("id: line shows the instance, not the node id (E2-12):\n%s", out)
	}
	// The node id is a real handle: ls shows it (the NODE column carries it).
	ls, _ := run("dabs ls")
	if !containsFold(ls, nodeID) {
		t.Fatalf("node id %q not shown by ls (E2-12):\n%s", nodeID, ls)
	}
	// And rm resolves it — proving it is the canonical handle, not a mere label.
	if _, code := run("dabs rm " + nodeID + " --yes"); code != 0 {
		t.Fatalf("dabs rm <node-id> did not reap the box (E2-12)")
	}
}

// nodeIDFrom pulls the canonical node id off the `id:` line of `--detach` output.
func nodeIDFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "id: "); ok {
			return strings.TrimSpace(id)
		}
	}
	t.Fatalf("no id line in output:\n%s", out)
	return ""
}

// E2-5: an `ls` box row set WHERE to the instance name only, so a box's on-disk
// location — its node dir, where volume/ephemeral bytes live — was never shown.
// The row must carry BOTH the box's location AND (still) the instance name.
func TestLsBoxRowShowsLocationE2E(t *testing.T) {
	clean(t)
	inst := up(t)
	out, _ := run("dabs ls")
	var boxLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, inst) {
			boxLine = line
		}
	}
	if boxLine == "" {
		t.Fatalf("no box row mentioning instance %q in ls (E2-5):\n%s", inst, out)
	}
	if !containsFold(boxLine, ".dabs/nodes/") {
		t.Fatalf("box row shows no on-disk location (E2-5); row:\n%q\nls:\n%s", boxLine, out)
	}
}

// containsFold is a tiny case-insensitive substring check (avoids importing
// strings just for this in a focused test file).
func containsFold(hay, needle string) bool {
	h, n := []byte(hay), []byte(needle)
	if len(n) == 0 {
		return true
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := 0; j < len(n); j++ {
			if lower(h[i+j]) != lower(n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// E2-36: `dabs ls` prints a node's id and its WHERE path raw. A node record
// whose field carries a newline (splitting the tree into phantom rows) or an
// ANSI escape (moving the cursor / spoofing the terminal) must be neutralized
// before it is drawn: no raw ESC survives, and the value stays on one row.
func TestLsSanitizesNodeFieldsE2E(t *testing.T) {
	clean(t)
	bad := filepath.Join(nodesDir(), "ff-e2ebug-sanitize")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	// dir carries an ESC-based color sequence and a newline. JSON escapes them
	// as \u001b and \n; the decoded record holds the raw bytes ls draws.
	rec := "{\"id\":\"ff-e2ebug-sanitize\",\"kind\":\"project\"," +
		"\"dir\":\"/tmp/ev\\u001b[31mil\\nBOOMROW\",\"created\":\"t\"}"
	if err := os.WriteFile(filepath.Join(bad, "dabs-node.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(bad) })

	out, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d) on a node with control chars in its dir:\n%s", code, out)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("raw ESC (0x1b) reached the terminal from a node field (E2-36):\n%q", out)
	}
	// The newline in the dir must not split the row: the id and the text that
	// followed the newline stay on the SAME line.
	var idLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ff-e2ebug-sanitize") {
			idLine = line
		}
	}
	if idLine == "" {
		t.Fatalf("node row not found in ls:\n%s", out)
	}
	if !strings.Contains(idLine, "BOOMROW") {
		t.Fatalf("embedded newline split the row into phantom lines (E2-36); id row:\n%q\nfull:\n%s", idLine, out)
	}
}

// E2-15: two sources mounting to the SAME box path are silently accepted, and
// one masks the other. The recipe must be REJECTED at validation, non-zero,
// with no box left behind.
func TestDuplicateMountPathRejectedE2E(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-duppath")
	bugRecipe(t, dir, "dup",
		"      - mkmount: $NODE_VOLUME/a\n        path: /work/dup\n"+
			"      - mkmount: $NODE_VOLUME/b\n        path: /work/dup\n")

	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("two sources at the same box path were accepted (E2-15):\n%s", out)
	}
	if !containsFold(out, "same box path") {
		t.Fatalf("duplicate-path rejection gave no clear reason (E2-15):\n%s", out)
	}
	ls, _ := run("dabs ls")
	if strings.Contains(ls, sandboxName+"-") {
		t.Fatalf("rejected recipe still left a live box (E2-15):\n%s", ls)
	}
}

// E2-8: an ambiguous prefix — one that matches more than one box — must ERROR
// for exec, listing the candidates, not silently pick one and run in it. (The
// cross-namespace case, a prefix that names one box's node id and a different
// box's instance name, is pinned by the unit test; here two boxes share an
// instance-name prefix, the deterministic e2e collision.)
func TestExecAmbiguousPrefixErrorsE2E(t *testing.T) {
	clean(t)
	a := up(t)
	up(t)
	out, code := run("dabs exec " + sandboxName + " -- touch /work/should-not-run")
	if code == 0 {
		t.Fatalf("ambiguous prefix ran in a box instead of erroring (E2-8):\n%s", out)
	}
	if !containsFold(out, "ambiguous") {
		t.Fatalf("ambiguous prefix gave no ambiguity error (E2-8):\n%s", out)
	}
	// It must have run in NO box: the marker file exists in neither.
	chk, _ := run("dabs exec " + a + " -- ls /work")
	if containsFold(chk, "should-not-run") {
		t.Fatalf("ambiguous exec silently ran in a box (E2-8):\n%s", chk)
	}
}

// --- Family E: reach-in commands drop the host stdin ---------------------------

// E2-44: `echo hi | dabs exec <box> cat` produced nothing — the box command's
// stdin was not connected to the host stdin, so any pipe-into-box workflow
// silently got empty input. Both the exact-argv form (`-- cat`) and the shell
// form must forward stdin to the box process.
func TestExecForwardsStdinE2E(t *testing.T) {
	clean(t)
	inst := up(t)

	out := runStdin("hello-stdin\n", "dabs exec "+inst+" -- cat")
	if !strings.Contains(out, "hello-stdin") {
		t.Fatalf("exact-argv exec dropped stdin (E2-44): want %q in output, got %q", "hello-stdin", out)
	}

	out = runStdin("shell-stdin\n", "dabs exec "+inst+" cat")
	if !strings.Contains(out, "shell-stdin") {
		t.Fatalf("shell-form exec dropped stdin (E2-44): want %q in output, got %q", "shell-stdin", out)
	}
}

// --- Family B: `up` succeeds but the box cannot run its command ----------------

// E2-16/45/49: `dabs recipe <r> --detach` printed success (recipe up / id:)
// even when the box could not be ENTERED — a source over `/`, a `workdir:`
// missing from the image, or an ro parent masking an rw child. Every later exec
// then failed `bwrap: Can't chdir`. A boot that cannot enter is not up: it must
// exit NONZERO, surface the driver's message, and leave NO live box behind.
func TestBootFailsWhenBoxUnusableE2E(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-badworkdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A recipe whose workdir is a path absent from the dabs-e2e image: the box
	// boots but bwrap cannot chdir into it, so no exec (not even the smoke check)
	// can run.
	yaml := "default: badwd\nrecipes:\n  badwd:\n    image: dabs-e2e\n" +
		"    command: [sh]\n    workdir: /no/such/dir\n"
	if err := os.WriteFile(filepath.Join(dir, "dabs.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("boot into a missing workdir reported success (E2-45):\n%s", out)
	}
	if strings.Contains(out, "id:") {
		t.Fatalf("failed boot still printed a success id line (E2-45):\n%s", out)
	}

	// No live box may linger: the failed boot must have reaped it. Any surviving
	// dabs-e2e instance would show in `dabs ls`.
	ls, lsCode := run("dabs ls")
	if lsCode != 0 {
		t.Fatalf("ls failed (%d): %s", lsCode, ls)
	}
	if strings.Contains(ls, sandboxName+"-") {
		t.Fatalf("failed boot left a live box behind (E2-16/45/49):\n%s", ls)
	}
}

// --- Family D: a check never looks at what is actually on disk ----------------

// E2-4: spaceHolds did a shallow ReadDir, so a space whose only content was an
// EMPTY subdirectory (an `mkmount:` that created $NODE_EPHEMERAL/e but never
// wrote a file) read as ⚠ "holds files". `dabs ls` must mark that box's
// ephemeral EMPTY (✓), not held (⚠) — otherwise the warning is trained noise.
func TestEmptyEphemeralSpaceNotMarkedHeldE2E(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-eph")
	bugRecipe(t, dir, "eph", "      - mkmount: $NODE_EPHEMERAL/e\n        path: /work/e\n")

	out, code := run("dabs recipe " + dir + " --detach")
	if code != 0 {
		t.Fatalf("up failed (%d): %s", code, out)
	}
	var inst string
	for _, line := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "id: "); ok {
			inst = strings.TrimSpace(id)
		}
	}
	if inst == "" {
		t.Fatalf("up printed no id line: %q", out)
	}

	// The mkmount created <box-node>/ephemeral/e as an EMPTY directory — no file
	// anywhere. The node dir is named by node id (distinct from the instance), so
	// find it by glob; in this clean box it is the only such space.
	matches, _ := filepath.Glob(filepath.Join(nodesDir(), "*", "ephemeral", "e"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one ephemeral/e space, got %v", matches)
	}
	if entries, err := os.ReadDir(matches[0]); err != nil || len(entries) != 0 {
		t.Fatalf("ephemeral/e should be an empty dir: entries=%v err=%v", entries, err)
	}

	ls, code := run("dabs ls")
	if code != 0 {
		t.Fatalf("ls failed (%d): %s", code, ls)
	}
	// Find the box's own row (its Where cell is the instance name) and assert its
	// space cells carry no held glyph — the empty subdir must not read as ⚠.
	var row string
	for _, line := range strings.Split(ls, "\n") {
		if strings.Contains(line, inst) {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("box row for %q not found in ls:\n%s", inst, ls)
	}
	if strings.Contains(row, "⚠") {
		t.Fatalf("empty ephemeral marked held (E2-4): row=%q\nfull ls:\n%s", row, ls)
	}
	if !strings.Contains(row, "✓") {
		t.Fatalf("box row shows no empty glyph, expected ✓: row=%q\nfull ls:\n%s", row, ls)
	}
}

// --- Family F: a name or path reaches a sink unsanitized -----------------------

// E2-14: a source path built from a dabs space var ($NODE_VOLUME) used `..` to
// climb OUT of the space it named, so dabs mkmount-created a directory anywhere
// on the host (e.g. /tmp/dabs-escape). The agent chain: write a recipe whose
// mkmount origin is `$NODE_VOLUME/../../../../../../tmp/dabs-escape-<pid>`. dabs
// must REJECT the recipe (nonzero) and create NO directory outside its node tree.
func TestRecipeSourcePathCannotEscapeNodeTreeE2E(t *testing.T) {
	clean(t)
	escape := filepath.Join("/tmp", "dabs-escape-e2e14")
	os.RemoveAll(escape)
	t.Cleanup(func() { os.RemoveAll(escape) })

	dir := filepath.Join(home, "e2e-escape")
	bugRecipe(t, dir, "esc",
		"      - mkmount: $NODE_VOLUME/../../../../../../tmp/dabs-escape-e2e14\n        path: /work/x\n")

	out, code := run("dabs recipe " + dir + " --detach")
	if code == 0 {
		t.Fatalf("a space path escaping the node tree was accepted (E2-14):\n%s", out)
	}
	if !containsFold(out, "escape") {
		t.Fatalf("escape rejection gave no clear reason (E2-14):\n%s", out)
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatalf("dabs provisioned a directory outside its node tree at %s (E2-14)", escape)
	}
}

// E2-31: several `dabs recipe --detach` boots racing in the SAME directory each
// scanned the node store, saw no project node for the cwd, and each minted one —
// duplicate project nodes for a single path. The resolve-or-create must be
// atomic: however many boots race, exactly ONE project node marks the directory.
func TestConcurrentDetachMintsOneProjectNode(t *testing.T) {
	clean(t)
	dir := filepath.Join(home, "e2e-proj-race")
	bugRecipe(t, dir, "race", "")

	const boots = 6
	outs := make([]string, boots)
	codes := make([]int, boots)
	var wg sync.WaitGroup
	for i := 0; i < boots; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], codes[i] = runIn(dir, "dabs recipe race --detach")
		}(i)
	}
	wg.Wait()
	for i := range codes {
		if codes[i] != 0 {
			t.Fatalf("boot %d failed (%d): %s", i, codes[i], outs[i])
		}
	}

	// Read the store itself: exactly one record of kind "project" whose Dir is
	// the directory the boots ran from.
	names, err := os.ReadDir(nodesDir())
	if err != nil {
		t.Fatal(err)
	}
	var projects []string
	for _, e := range names {
		b, err := os.ReadFile(filepath.Join(nodesDir(), e.Name(), "dabs-node.json"))
		if err != nil {
			continue // not a node record
		}
		var n struct {
			Kind string `json:"kind"`
			Dir  string `json:"dir"`
		}
		if json.Unmarshal(b, &n) != nil {
			continue
		}
		if n.Kind == "project" && n.Dir == dir {
			projects = append(projects, e.Name())
		}
	}
	if len(projects) != 1 {
		t.Fatalf("want exactly 1 project node for %s, got %d: %v (E2-31)", dir, len(projects), projects)
	}
}
