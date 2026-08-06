package door

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// CONTRACT: the host scans a box's registry while its relay is writing into it,
// so a descriptor must never be readable half-written. A reader hammering the
// path across many writes must see either no file or a whole one — and no
// scratch file may be left in a directory the host reads.
func TestDescriptorIsNeverReadableHalfWritten(t *testing.T) {
	dir, err := os.MkdirTemp("", "d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, NameDescriptor("web"))

	stop := make(chan struct{})
	torn := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue // not there yet; absent is a legal state
			}
			var d Descriptor
			if json.Unmarshal(b, &d) != nil || d.Port == 0 {
				select {
				case torn <- b:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 3000; i++ {
		if err := WriteDescriptor(dir, "web", TypeWebUI, 5000+i%100); err != nil {
			t.Fatalf("WriteDescriptor: %v", err)
		}
	}
	close(stop)
	<-done
	select {
	case b := <-torn:
		t.Fatalf("a reader saw a half-written descriptor: %q", b)
	default:
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != NameDescriptor("web") {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only %s", names, NameDescriptor("web"))
	}
}

// CONTRACT: removing a descriptor un-publishes the service, and removing one
// that is already gone is not an error — a relay closing a publication twice
// must not fail the second time.
func TestRemovingADescriptorUnpublishesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDescriptor(dir, "web", TypeGeneral, 8080); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RemoveDescriptor(dir, "web"); err != nil {
			t.Fatalf("RemoveDescriptor (call %d): %v", i+1, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, NameDescriptor("web"))); !os.IsNotExist(err) {
		t.Errorf("the descriptor is still there: %v", err)
	}
}
