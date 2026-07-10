package actions_test

import (
	"strings"
	"testing"

	"github.com/jjmerino/dabs/core/params"
)

// Component tests for `dabs auth claude`, now that its I/O routes through the
// data seam. DABS_AUTH_IMAGE is set so no image is built; the fakes stand in for
// the box and the vault file.

// CONTRACT: success requires a real, non-empty credential after the login box
// exits — and the login runs `claude` in a box that mounts the vault.
func TestAuthSucceedsWithToken(t *testing.T) {
	fd := baseData()
	fd.env["DABS_AUTH_IMAGE"] = "fakeimg" // skip the build path
	fd.files = map[string][]byte{
		"/home/t/.dabs/auth/claude/.credentials.json": []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`),
	}
	drv := &fakeDriver{built: map[string]bool{}}
	if err := newReal("", fd, drv).Auth(params.Auth{Provider: "claude"}); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if len(drv.ups) != 1 || len(drv.runs) != 1 || len(drv.downs) != 1 {
		t.Fatalf("auth lifecycle off: ups=%d runs=%d downs=%d", len(drv.ups), len(drv.runs), len(drv.downs))
	}
	if drv.runs[0][0] != "claude" {
		t.Errorf("login did not run claude: %v", drv.runs)
	}
	if m := drv.ups[0].Mounts; len(m) != 1 || m[0].Path != "/root/.claude" {
		t.Errorf("vault not mounted at the config path: %+v", m)
	}
}

// CONTRACT: no credential produced (login not completed) is an error, and the
// box is still torn down.
func TestAuthErrorsWithoutToken(t *testing.T) {
	fd := baseData()
	fd.env["DABS_AUTH_IMAGE"] = "fakeimg" // no credentials file -> credAccessToken == ""
	drv := &fakeDriver{}
	err := newReal("", fd, drv).Auth(params.Auth{Provider: "claude"})
	if err == nil || !strings.Contains(err.Error(), "did not produce a credential") {
		t.Fatalf("want no-credential error, got %v", err)
	}
	if len(drv.downs) != 1 {
		t.Errorf("box not torn down after failed login: %v", drv.downs)
	}
}

// CONTRACT: an unknown provider fails before touching a box.
func TestAuthUnknownProvider(t *testing.T) {
	drv := &fakeDriver{}
	err := newReal("", baseData(), drv).Auth(params.Auth{Provider: "openai"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want unknown-provider error, got %v", err)
	}
	if len(drv.ups) != 0 {
		t.Errorf("brought a box up for an unknown provider: %v", drv.ups)
	}
}
