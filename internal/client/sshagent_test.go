package client

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func listenUnix(t *testing.T, path string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen(%s) error = %v", path, err)
	}
	t.Cleanup(func() { listener.Close() })
}

// Each connecting client points the link at its own agent; tabs get the link,
// so a tab from an earlier login reaches the agent of the latest one.
func TestLinkSSHAgentFollowsTheLatestLogin(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	first, second := filepath.Join(directory, "a1"), filepath.Join(directory, "a2")
	listenUnix(t, first)
	listenUnix(t, second)
	// The link names the resolved socket; the temporary directory may sit
	// behind a link of its own, as /var does on macOS.
	first, _ = filepath.EvalSymlinks(first)
	second, _ = filepath.EvalSymlinks(second)
	link := filepath.Join(directory, sshAgentLink)

	environment := LinkSSHAgent(directory, []string{"HOME=/h", "SSH_AUTH_SOCK=" + first})
	if !slices.Contains(environment, "SSH_AUTH_SOCK="+link) || !slices.Contains(environment, "HOME=/h") {
		t.Fatalf("environment = %q, want SSH_AUTH_SOCK naming the link", environment)
	}
	if target, _ := os.Readlink(link); target != first {
		t.Fatalf("link -> %q, want the first agent", target)
	}
	LinkSSHAgent(directory, []string{"SSH_AUTH_SOCK=" + second})
	if target, _ := os.Readlink(link); target != second {
		t.Fatalf("link -> %q, want the latest agent", target)
	}

	// A shell inside a tab names the link already; it does not loop the link.
	LinkSSHAgent(directory, []string{"SSH_AUTH_SOCK=" + link})
	if target, _ := os.Readlink(link); target != second {
		t.Fatalf("link -> %q after a nested client, want it unchanged", target)
	}
	// A login whose agent is gone leaves the link on the last good one.
	LinkSSHAgent(directory, []string{"SSH_AUTH_SOCK=" + filepath.Join(directory, "gone")})
	if target, _ := os.Readlink(link); target != second {
		t.Fatalf("link -> %q after a missing agent, want it unchanged", target)
	}
	// No agent at all leaves the environment as it was.
	if got := LinkSSHAgent(directory, []string{"HOME=/h"}); !slices.Equal(got, []string{"HOME=/h"}) {
		t.Fatalf("environment without an agent = %q", got)
	}
}

// A shell profile that links its own SSH_AUTH_SOCK and exports the link, as
// many do, must not close a loop with romty's link.
func TestLinkSSHAgentDoesNotLoopWithAProfileLink(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	agent := filepath.Join(directory, "agent")
	listenUnix(t, agent)
	resolved, _ := filepath.EvalSymlinks(agent)
	profile := filepath.Join(directory, "profile.sock")
	link := filepath.Join(directory, sshAgentLink)

	// The login links its agent and exports the profile link.
	if err := os.Symlink(agent, profile); err != nil {
		t.Fatal(err)
	}
	LinkSSHAgent(directory, []string{"SSH_AUTH_SOCK=" + profile})
	// Inside a tab the profile runs again and points its link at romty's.
	if err := os.Remove(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, profile); err != nil {
		t.Fatal(err)
	}
	LinkSSHAgent(directory, []string{"SSH_AUTH_SOCK=" + profile})
	if target, _ := os.Readlink(link); target != resolved {
		t.Fatalf("link -> %q, want the agent socket %q", target, resolved)
	}
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("the link does not reach the agent: %v", err)
	}
}

// A dial that finds nothing listening is reported as model.ErrDaemonUnavailable,
// which is how callers outside this package recognise a stopped daemon.
func TestNothingListeningIsReportedAsUnavailable(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "none.sock")
	_, err := New(socket).Snapshot()
	if !errors.Is(err, model.ErrDaemonUnavailable) || !Unavailable(err) {
		t.Fatalf("Snapshot() with no daemon = %v, want ErrDaemonUnavailable", err)
	}
}
