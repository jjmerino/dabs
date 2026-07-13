package actions

// Stale-image detection. An image is skipped on a boot or build only when it
// EXISTS and the source it was built from is UNCHANGED. When dabs builds an
// image it records a content digest of that source beside its own state
// (~/.dabs/images-meta/<image>.json); before reusing an image it compares the
// recorded digest against the current source. This is what makes an image fix
// actually arrive: since #39 ("don't rebuild a built image"), image existence
// was the ONLY freshness check, so an edited Dockerfile — or a change to a
// bundled image's embedded files, e.g. curl added to images/shell — was served
// stale for ever until a `prune`. The record is dabs-owned and driver-agnostic,
// so it works for the apple and docker drivers alike, with no reliance on driver
// label support.
//
// SCOPE OF THE DIGEST. It covers the Dockerfile bytes (inline recipes) or every
// embedded file under images/<name> (bundled images). It deliberately does NOT
// fingerprint an inline recipe's host build CONTEXT (a `COPY .` that picks up
// edited files): a file-list+size+mtime fingerprint is reproducibility-fragile
// (a fresh checkout changes every mtime) and would need a directory-walk
// primitive the data seam does not expose. A context-only change is recovered
// with `dabs prune` (noted in `dabs build --help`). The reported bug — bundled
// and inline Dockerfile edits — is fully covered.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/jjmerino/dabs/core/recipe"
	"github.com/jjmerino/dabs/core/sandbox"
)

// imageMeta is the record at ~/.dabs/images-meta/<image>.json: the digest the
// image was last built from, and when — provenance beside dabs's own state.
type imageMeta struct {
	Digest   string `json:"digest"`
	Recorded string `json:"recorded"` // RFC3339
}

// fileContent is one file's path and bytes — the unit the fingerprint hashes.
type fileContent struct {
	name string
	data []byte
}

// fingerprintFiles is a pure content digest of a set of files: sha256 over each
// file's path and bytes, sorted by path (order-independent) and length-delimited
// so two distinct sets cannot collide by concatenation. Any change to a path or
// a byte changes the digest.
func fingerprintFiles(files []fileContent) string {
	sorted := append([]fileContent(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	h := sha256.New()
	for _, f := range sorted {
		// Length-delimit path and bytes so ("ab","c") cannot hash as ("a","bc").
		fmt.Fprintf(h, "%d:", len(f.name))
		h.Write([]byte(f.name))
		fmt.Fprintf(h, "%d:", len(f.data))
		h.Write(f.data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// currentInlineDigest is the digest of an inline recipe's build SOURCE: the
// Dockerfile bytes (the host context is intentionally excluded — see the file
// header).
func (r Real) currentInlineDigest(img recipe.ImageRef) (string, error) {
	dockerfile, err := r.absPath(img.Dockerfile)
	if err != nil {
		return "", err
	}
	b, err := r.data.ReadFile(dockerfile)
	if err != nil {
		return "", fmt.Errorf("image digest: read %s: %w", dockerfile, err)
	}
	return fingerprintFiles([]fileContent{{name: "Dockerfile", data: b}}), nil
}

// currentBundledDigest is the digest of a bundled image's SOURCE: every embedded
// file under images/<name>, so a change to ANY embedded file (the Dockerfile, a
// baked-in script) yields a new digest.
func (r Real) currentBundledDigest(name string) (string, error) {
	sub := "images/" + name
	var files []fileContent
	err := fs.WalkDir(r.images, sub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(r.images, p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sub, p)
		files = append(files, fileContent{name: rel, data: data})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("image digest: walk %s: %w", sub, err)
	}
	return fingerprintFiles(files), nil
}

// imageMetaPath is where an image's build record lives — dabs-owned state, not
// the goldens/project and not under any driver.
func (r Real) imageMetaPath(image string) (string, error) {
	home, err := r.data.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dabs", "images-meta", image+".json"), nil
}

// recordImageDigest writes the digest an image was just built from. A write
// failure is not fatal to the build that produced the image — it degrades to
// "no build record" on the next boot (one extra rebuild), never a stale reuse.
func (r Real) recordImageDigest(image, digest string) error {
	path, err := r.imageMetaPath(image)
	if err != nil {
		return err
	}
	if err := r.data.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(imageMeta{Digest: digest, Recorded: stampNow()})
	if err != nil {
		return err
	}
	return r.data.WriteFile(path, b, 0o644)
}

// recordedImageDigest reads the digest an image was last built from. found is
// false when there is no record (a legacy image built before digests, or a
// corrupt record) — treated as stale, so the image rebuilds once and records.
func (r Real) recordedImageDigest(image string) (digest string, found bool, err error) {
	path, err := r.imageMetaPath(image)
	if err != nil {
		return "", false, err
	}
	b, err := r.data.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var m imageMeta
	if err := json.Unmarshal(b, &m); err != nil || m.Digest == "" {
		return "", false, nil // a corrupt/empty record is no record
	}
	return m.Digest, true, nil
}

// imageReuse decides whether a built image may be served as-is: it must EXIST and
// its recorded source digest must equal current. When it may not, reason explains
// why a rebuild will run (for the operator or agent reading the output); reason
// is empty when the image simply is not built yet (a plain first build — no news).
func (r Real) imageReuse(drv sandbox.Driver, name, current string) (reuse bool, reason string, err error) {
	built, err := drv.HasImage(name)
	if err != nil {
		return false, "", err
	}
	if !built {
		return false, "", nil
	}
	recorded, found, err := r.recordedImageDigest(name)
	if err != nil {
		return false, "", err
	}
	if !found {
		return false, fmt.Sprintf("image %s: no build record — rebuilding", name), nil
	}
	if recorded != current {
		return false, fmt.Sprintf("image %s: Dockerfile changed — rebuilding", name), nil
	}
	return true, "", nil
}
