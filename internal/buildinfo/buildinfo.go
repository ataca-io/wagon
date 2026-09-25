// Package buildinfo reports which build of wagon is running. A release stamps
// the tag in with -ldflags; anything else falls back to what the Go toolchain
// recorded from the VCS tree, so an untagged local build still identifies
// itself rather than answering "dev".
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// version is set at build time via
// -ldflags "-X github.com/ataca-io/wagon/internal/buildinfo.version=vX.Y".
var version string

// Version returns the release tag when one was stamped in, otherwise a
// VCS-derived identifier for the commit ("a1b2c3d", or "a1b2c3d-dirty" when
// the tree had uncommitted changes), and "dev" when neither is available.
func Version() string {
	if version != "" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if rev, dirty := vcs(bi); rev != "" {
		if dirty {
			return rev + "-dirty"
		}
		return rev
	}
	// A `go install pkg@version` build carries a real module version; a plain
	// local build reports "(devel)", which says nothing.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "dev"
}

// String is the line wagon prints for --version, for example
// "wagon v0.40 (go1.27.0 darwin/arm64)".
func String(name string) string {
	return name + " " + Version() + " (" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// vcs pulls the short commit and the dirty flag out of the build settings the
// toolchain embeds. Absent when built with -buildvcs=false or from outside a
// repository.
func vcs(bi *debug.BuildInfo) (rev string, dirty bool) {
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}
