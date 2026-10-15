package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func releaseCheckDashboard(t *testing.T, current string) dashboard {
	t.Helper()
	testutil.Set(t, &currentVersion, func() string { return current })
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 30
	return value
}

func navigationTitle(value dashboard) string {
	return value.renderNavigation(30, 10)[0]
}

func TestDashboardMarksANewerRelease(t *testing.T) {
	value := releaseCheckDashboard(t, "v0.34.0")
	if title := navigationTitle(value); strings.Contains(title, "romty*") {
		t.Fatalf("title before the check = %q, want no marker", title)
	}

	command := send(&value, releaseCheckedMsg{latest: "v0.35.0"})
	if command == nil {
		t.Fatal("a finished check did not schedule the next one")
	}
	if title := navigationTitle(value); !strings.Contains(title, " romty* ") {
		t.Fatalf("title = %q, want the newer-release marker", title)
	}
	value.modal = aboutModal
	if about := strings.Join(value.renderModalBody(100, 30), "\n"); !strings.Contains(about, "v0.35.0 is available") {
		t.Fatalf("About = %q, want the newer release named", about)
	}

	// A failed check keeps the answer it has; the release is still out there.
	send(&value, releaseCheckedMsg{err: errors.New("offline")})
	if title := navigationTitle(value); !strings.Contains(title, " romty* ") {
		t.Fatalf("title after a failed check = %q, want the marker kept", title)
	}
}

func TestDashboardDoesNotMarkTheCurrentRelease(t *testing.T) {
	for _, latest := range []string{"v0.34.0", "v0.33.2"} {
		value := releaseCheckDashboard(t, "v0.34.0")
		value.newerRelease = "v0.34.0"
		send(&value, releaseCheckedMsg{latest: latest})
		if title := navigationTitle(value); strings.Contains(title, "romty*") {
			t.Fatalf("latest %s: title = %q, want no marker", latest, title)
		}
	}
}
