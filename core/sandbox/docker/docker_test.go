package docker

import (
	"reflect"
	"testing"
)

// execFlags must allocate a pseudo-TTY only when the caller's stdin is a real
// terminal: -t on a non-TTY stdin makes `docker exec` fail, which would break
// every non-interactive run (pipes, scripts, agents, CI).
func TestExecFlags(t *testing.T) {
	if got, want := execFlags(true), []string{"exec", "-i", "-t"}; !reflect.DeepEqual(got, want) {
		t.Errorf("execFlags(true) = %v, want %v", got, want)
	}
	if got, want := execFlags(false), []string{"exec", "-i"}; !reflect.DeepEqual(got, want) {
		t.Errorf("execFlags(false) = %v, want %v", got, want)
	}
}
