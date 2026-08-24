package version

import (
	"runtime/debug"
	"testing"
)

// An unstamped build has to name itself from what the toolchain recorded. The
// module version is only worth showing when a tag actually describes the
// build: a pseudo-version names a release that was never made, and it is long
// enough to crowd the About box while saying less than the commit does.
func TestFromBuildInfoPrefersATagOverAPseudoVersion(t *testing.T) {
	commit := debug.BuildSetting{Key: "vcs.revision", Value: "b7c47e75c6c202af69dae65dd04dbeda9c9d1a9a"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}

	for _, probe := range []struct {
		name     string
		module   string
		settings []debug.BuildSetting
		want     string
	}{
		{name: "tagged module", module: "v0.5.0", want: "v0.5.0"},
		{name: "tag without the v", module: "0.5.0", want: "v0.5.0"},
		{
			name:     "pseudo-version falls back to the commit",
			module:   "v0.5.1-0.20260824111154-b7c47e75c6c2",
			settings: []debug.BuildSetting{commit},
			want:     "b7c47e7",
		},
		{
			name:     "uncommitted work says so",
			module:   "(devel)",
			settings: []debug.BuildSetting{commit, dirty},
			want:     "b7c47e7-dirty",
		},
		{name: "no module and no repository", module: "(devel)", want: "dev"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			info := &debug.BuildInfo{Settings: probe.settings}
			info.Main.Version = probe.module
			if got := fromBuildInfo(info); got != probe.want {
				t.Fatalf("fromBuildInfo(%q) = %q, want %q", probe.module, got, probe.want)
			}
		})
	}
}
