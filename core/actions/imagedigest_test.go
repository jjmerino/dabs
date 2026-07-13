package actions

// Unit tests for the pure content-fingerprint the stale-image check is built on.
// The function must be deterministic, order-independent, and sensitive to any
// change in a file's path or bytes — and must not let two distinct file sets
// collide by concatenation.

import "testing"

func TestFingerprintIsDeterministic(t *testing.T) {
	files := []fileContent{{"Dockerfile", []byte("FROM alpine")}, {"run.sh", []byte("echo hi")}}
	if fingerprintFiles(files) != fingerprintFiles(files) {
		t.Fatal("same input produced different digests")
	}
}

func TestFingerprintIsOrderIndependent(t *testing.T) {
	a := []fileContent{{"a", []byte("1")}, {"b", []byte("2")}}
	b := []fileContent{{"b", []byte("2")}, {"a", []byte("1")}}
	if fingerprintFiles(a) != fingerprintFiles(b) {
		t.Fatal("reordering the same files changed the digest")
	}
}

func TestFingerprintReactsToContentChange(t *testing.T) {
	base := []fileContent{{"Dockerfile", []byte("RUN apk add git")}}
	edited := []fileContent{{"Dockerfile", []byte("RUN apk add git curl")}}
	if fingerprintFiles(base) == fingerprintFiles(edited) {
		t.Fatal("a content change did not change the digest")
	}
}

func TestFingerprintReactsToNameChange(t *testing.T) {
	a := []fileContent{{"Dockerfile", []byte("x")}}
	b := []fileContent{{"Dockerfile.dev", []byte("x")}}
	if fingerprintFiles(a) == fingerprintFiles(b) {
		t.Fatal("a path change did not change the digest")
	}
}

// A length-delimited hash must not let ("ab","c") and ("a","bc") collide — the
// boundary between path and bytes is real, not just concatenation.
func TestFingerprintNoBoundaryCollision(t *testing.T) {
	x := []fileContent{{"ab", []byte("c")}}
	y := []fileContent{{"a", []byte("bc")}}
	if fingerprintFiles(x) == fingerprintFiles(y) {
		t.Fatal("distinct file sets collided across the path/data boundary")
	}
}

// The empty set is stable and distinct from any non-empty set.
func TestFingerprintEmpty(t *testing.T) {
	if fingerprintFiles(nil) != fingerprintFiles([]fileContent{}) {
		t.Fatal("nil and empty slices should fingerprint the same")
	}
	if fingerprintFiles(nil) == fingerprintFiles([]fileContent{{"a", nil}}) {
		t.Fatal("empty set collided with a one-file set")
	}
}
