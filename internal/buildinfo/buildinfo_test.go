package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// TestVersionPrefersTheStamp checks that a stamped version wins over anything
// the toolchain recorded.
func TestVersionPrefersTheStamp(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = "v0.40"
	if got := Version(); got != "v0.40" {
		t.Errorf("Version() = %q, want v0.40", got)
	}
}

// TestVersionNeverEmpty checks that Version always returns something usable,
// so a deployed binary can say what it is.
func TestVersionNeverEmpty(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = ""
	if got := Version(); got == "" {
		t.Error("Version() = \"\", want a stamp, a VCS revision, or \"dev\"")
	}
}

func TestString(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = "v0.40"
	want := "wagon v0.40 (" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := String("wagon"); got != want {
		t.Errorf("String(\"wagon\") = %q, want %q", got, want)
	}
}

func TestVCS(t *testing.T) {
	tests := []struct {
		name      string
		settings  []debug.BuildSetting
		wantRev   string
		wantDirty bool
	}{
		{
			name:     "no vcs settings",
			settings: []debug.BuildSetting{{Key: "GOARCH", Value: "arm64"}},
		},
		{
			name:     "revision is shortened to seven",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "e63f036a1b2c3d4e5f6071829304a5b6c7d8e9f0"}},
			wantRev:  "e63f036",
		},
		{
			name:     "short revision is left alone",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
			wantRev:  "abc123",
		},
		{
			name: "dirty tree",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "e63f036a1b2c3d4e"},
				{Key: "vcs.modified", Value: "true"},
			},
			wantRev:   "e63f036",
			wantDirty: true,
		},
		{
			name: "clean tree",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "e63f036a1b2c3d4e"},
				{Key: "vcs.modified", Value: "false"},
			},
			wantRev: "e63f036",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev, dirty := vcs(&debug.BuildInfo{Settings: tt.settings})
			if rev != tt.wantRev || dirty != tt.wantDirty {
				t.Errorf("vcs(%v) = (%q, %v), want (%q, %v)", tt.settings, rev, dirty, tt.wantRev, tt.wantDirty)
			}
		})
	}
}

// TestVersionRejectsDevelModuleVersion checks that "(devel)", the module version
// of a plain local build, is never surfaced as the version.
func TestVersionRejectsDevelModuleVersion(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = ""
	if strings.Contains(Version(), "(devel)") {
		t.Errorf("Version() = %q, want a revision or \"dev\", never the module's (devel)", Version())
	}
}
