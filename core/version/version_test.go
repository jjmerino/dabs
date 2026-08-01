package version

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	info.Main.Version = mainVersion
	for k, v := range settings {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return info
}

func TestFormatLine(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{
			name:    "stamped tag wins over build info",
			stamped: "v0.6.0",
			info:    buildInfo("v9.9.9", map[string]string{"vcs.revision": "abcdefabcdefabcdef"}),
			ok:      true,
			want:    "dabs v0.6.0",
		},
		{
			name:    "stamped tag without a v gets one",
			stamped: "0.6.0",
			ok:      false,
			want:    "dabs v0.6.0",
		},
		{
			name:    "module version is used when nothing is stamped",
			stamped: "",
			info:    buildInfo("v0.5.1", nil),
			ok:      true,
			want:    "dabs v0.5.1",
		},
		{
			name:    "module version outranks the revision",
			stamped: "",
			info:    buildInfo("v0.5.1", map[string]string{"vcs.revision": "1a2b3c4d5e6f7a8b9c0d", "vcs.modified": "true"}),
			ok:      true,
			want:    "dabs v0.5.1",
		},
		{
			name:    "a revision one character over the limit is cut to the limit",
			stamped: "",
			info:    buildInfo("", map[string]string{"vcs.revision": "1a2b3c4d5e6f7"}),
			ok:      true,
			want:    "dabs 1a2b3c4d5e6f (built from source)",
		},
		{
			name:    "a revision exactly at the limit is kept whole",
			stamped: "",
			info:    buildInfo("", map[string]string{"vcs.revision": "1a2b3c4d5e6f"}),
			ok:      true,
			want:    "dabs 1a2b3c4d5e6f (built from source)",
		},
		{
			name:    "devel module version falls through to the revision",
			stamped: "",
			info:    buildInfo("(devel)", map[string]string{"vcs.revision": "1a2b3c4d5e6f7a8b9c0d", "vcs.modified": "false"}),
			ok:      true,
			want:    "dabs 1a2b3c4d5e6f (built from source)",
		},
		{
			name:    "a dirty tree is marked",
			stamped: "",
			info:    buildInfo("", map[string]string{"vcs.revision": "1a2b3c4d5e6f7a8b9c0d", "vcs.modified": "true"}),
			ok:      true,
			want:    "dabs 1a2b3c4d5e6f-dirty (built from source)",
		},
		{
			name:    "a short revision is not padded or truncated",
			stamped: "",
			info:    buildInfo("", map[string]string{"vcs.revision": "1a2b3c"}),
			ok:      true,
			want:    "dabs 1a2b3c (built from source)",
		},
		{
			name:    "no revision and no version is unknown",
			stamped: "",
			info:    buildInfo("", nil),
			ok:      true,
			want:    "dabs (unknown)",
		},
		{
			name:    "no build info at all is unknown",
			stamped: "",
			info:    nil,
			ok:      false,
			want:    "dabs (unknown)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatLine(tt.stamped, tt.info, tt.ok); got != tt.want {
				t.Errorf("formatLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The line identifies a binary, so it must be exactly one line and must name
// dabs whatever the inputs are.
func TestLineIsOneLine(t *testing.T) {
	got := Line()
	if got == "" || got[len(got)-1] == '\n' {
		t.Fatalf("Line() = %q, want a bare single line", got)
	}
	for _, r := range got {
		if r == '\n' {
			t.Fatalf("Line() = %q, want no newline", got)
		}
	}
	if len(got) < 5 || got[:5] != "dabs " {
		t.Errorf("Line() = %q, want it to start with %q", got, "dabs ")
	}
}
