package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// CONTRACT: the host scans the services directory while a box is publishing
// into it, so the descriptor must never be readable half-written. A reader
// hammering the path across many writes must see either no file or a whole one
// — and no scratch file may be left in a directory the host reads.
func TestDescriptorIsNeverReadableHalfWritten(t *testing.T) {
	dir, err := os.MkdirTemp("", "d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, DescriptorName("web"))

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
		if err := writeDescriptor(dir, "web", TypeWebUI, 5000+i%100, 6000+i%100); err != nil {
			t.Fatalf("writeDescriptor: %v", err)
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
	if len(entries) != 1 || entries[0].Name() != DescriptorName("web") {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only %s", names, DescriptorName("web"))
	}
}
