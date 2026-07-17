package actions_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// CONTRACT: a driver that cannot list (a stopped docker daemon) does not turn
// `exec <bogus>` into its own noise — resolution skips it (with a warning) and
// the caller still gets the real answer: no box matches.
func TestExecResolutionSurvivesDriverLsFailure(t *testing.T) {
	drv := &fakeDriver{lsErrOnce: errors.New("docker ps: exit status 1")}
	err := newReal("", baseData(), drv).Exec(params.Exec{Instance: "ghost", Cmd: []string{"ls"}})
	if err == nil || !strings.Contains(err.Error(), `no box matches "ghost"`) {
		t.Fatalf("want the no-box answer despite the driver outage, got %v", err)
	}
}
