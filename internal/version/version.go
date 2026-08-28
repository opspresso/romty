// Package version reports which romty is running: the tag a release was
// stamped with, or the commit any other build was made from.
package version

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// Value is the release the binary was built as, stamped at link time by
// .goreleaser.yml. It is empty in every other build, which is why String falls
// back rather than reporting nothing.
var Value string

// pseudoVersion matches what the Go toolchain invents for a build that no tag
// describes: a timestamp and a commit appended to the next version. It names a
// release that was never made, so the commit alone is the honest answer.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}(\+.*)?$`)
var semanticVersion = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// IsRelease reports whether the running binary came from a tagged release.
// Local go run, go build, and go install builds have a development module
// version and must not be used as persistent hook commands.
func IsRelease() bool {
	if value := strings.TrimSpace(Value); value != "" {
		return isReleaseVersion(value)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	return isReleaseVersion(info.Main.Version)
}

func isReleaseVersion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "0.0.0" && value != "v0.0.0" && semanticVersion.MatchString(value) && !pseudoVersion.MatchString(value)
}

// String is what romty calls itself in About. A release reports its tag, a
// `go install` of a tagged module reports that tag, and anything else reports
// the commit it was built from — each is the most precise answer that build
// can give about which romty is running.
func String() string {
	if value := strings.TrimSpace(Value); value != "" {
		return withPrefix(value)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return fromBuildInfo(info)
}

func fromBuildInfo(info *debug.BuildInfo) string {
	module := info.Main.Version
	if module != "" && module != "(devel)" && !pseudoVersion.MatchString(module) {
		return withPrefix(module)
	}
	return revision(info)
}

// revision reads the commit the Go toolchain records for a build made inside a
// repository. A build made outside one records nothing, hence the fallback.
func revision(info *debug.BuildInfo) string {
	value := "dev"
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if len(setting.Value) > 7 {
				value = setting.Value[:7]
			} else if setting.Value != "" {
				value = setting.Value
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if modified {
		// Uncommitted work does not match the commit it is stamped with, and a
		// bug report naming that commit alone would send the reader to code
		// that never ran.
		return value + "-dirty"
	}
	return value
}

// withPrefix restores the v that release tooling strips from a tag, so About
// shows the tag as the release page and the Homebrew formula spell it.
func withPrefix(value string) string {
	if value == "" || value[0] < '0' || value[0] > '9' {
		return value
	}
	return "v" + value
}
