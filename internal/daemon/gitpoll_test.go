package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/testutil"
)

// A quiet repository is not read again until it is stale; a stage, a print in
// one of its terminals, or force reads it at once.
func TestGitPollerReadsOnlyWhatMayHaveChanged(t *testing.T) {
	repository := t.TempDir()
	testutil.RunGit(t, "init", "--initial-branch=main", repository)
	poller := &gitPoller{}
	quiet := func(string, time.Time) bool { return false }
	readAt := func() time.Time { return poller.reads[repository].at }

	states := poller.poll([]string{repository}, quiet, false, false)
	if states[repository].Branch != "main" {
		t.Fatalf("first poll = %+v", states)
	}
	first := readAt()
	poller.poll([]string{repository}, quiet, false, false)
	if !readAt().Equal(first) {
		t.Fatal("an unchanged quiet repository was read again")
	}

	testutil.WriteFile(t, repository, "a.txt", "a\n")
	testutil.RunGit(t, "-C", repository, "add", "a.txt")
	poller.poll([]string{repository}, quiet, false, false)
	if readAt().Equal(first) {
		t.Fatal("a stage did not cause a read")
	}
	if !poller.poll([]string{repository}, quiet, false, false)[repository].Dirty {
		t.Fatal("the staged file is not reported")
	}

	staged := readAt()
	poller.poll([]string{repository}, func(string, time.Time) bool { return true }, false, false)
	if readAt().Equal(staged) {
		t.Fatal("output in the workspace did not cause a read")
	}
	printed := readAt()
	poller.poll([]string{repository}, quiet, true, false)
	if readAt().Equal(printed) {
		t.Fatal("force did not cause a read")
	}
}

// ROMTY_MEASURE_GIT lists repositories, separated by commas, to time a full
// read against a quiet poll. It measures; it does not assert.
func TestMeasureGitPoller(t *testing.T) {
	list := os.Getenv("ROMTY_MEASURE_GIT")
	if list == "" {
		t.Skip("set ROMTY_MEASURE_GIT to measure")
	}
	paths := strings.Split(list, ",")
	poller := &gitPoller{}
	quiet := func(string, time.Time) bool { return false }
	start := time.Now()
	poller.poll(paths, quiet, true, false)
	full := time.Since(start)
	start = time.Now()
	poller.poll(paths, quiet, false, false)
	t.Logf("%d repositories: full read %v, quiet poll %v", len(paths), full, time.Since(start))
}
