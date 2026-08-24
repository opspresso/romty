package version_test

import (
	"regexp"
	"testing"

	"github.com/opspresso/romty/internal/version"
)

// A stamped release reports its tag with the v release tooling strips.
func TestStringRestoresTheTagPrefix(t *testing.T) {
	original := version.Value
	t.Cleanup(func() { version.Value = original })

	for _, probe := range []struct{ stamped, want string }{
		{stamped: "0.3.1", want: "v0.3.1"},
		{stamped: "v0.3.1", want: "v0.3.1"},
		{stamped: " 0.3.1 ", want: "v0.3.1"},
		{stamped: "nightly", want: "nightly"},
	} {
		version.Value = probe.stamped
		if got := version.String(); got != probe.want {
			t.Fatalf("String() with %q stamped = %q, want %q", probe.stamped, got, probe.want)
		}
	}
}

// An unstamped build still has to name itself: About shows whatever this
// returns, and an empty line there tells a bug report nothing.
func TestStringNamesAnUnstampedBuild(t *testing.T) {
	original := version.Value
	t.Cleanup(func() { version.Value = original })

	version.Value = ""
	got := version.String()
	if !regexp.MustCompile(`^(dev|[0-9a-f]{7,40}|v.+)(-dirty)?$`).MatchString(got) {
		t.Fatalf("String() with no stamp = %q, want a revision, a module version, or dev", got)
	}
}
