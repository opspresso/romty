package ui

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

// gone is the error a request gets when nothing listens on the socket.
var gone = fmt.Errorf("dial unix /x/daemon.sock: %w", syscall.ECONNREFUSED)

// When the daemon stops under the TUI, the screen names the way back instead
// of the socket error, and F5 starts it again.
func TestDaemonStopShowsTheWayBackAndF5RestartsIt(t *testing.T) {
	restarts := 0
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.width, value.height = 110, 16
	value.restartDaemon = func() error { restarts++; return nil }

	send(&value, agentSnapshotMsg{err: gone})
	screen := ansi.Strip(value.render())
	for _, want := range []string{"DAEMON", "press F5 to start it again", "The romty daemon stopped", "F5  start the daemon again"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("stopped screen lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "connection refused") {
		t.Fatalf("stopped screen shows the socket error:\n%s", screen)
	}

	// Another failure while it is gone does not replace the message.
	send(&value, snapshotMsg{err: errors.New("unrelated")})
	value.clearAnyError()
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "press F5 to start it again") {
		t.Fatalf("the way back left the screen:\n%s", screen)
	}

	command := send(&value, key(tea.KeyF5, ""))
	if !value.daemonRestarting || command == nil {
		t.Fatal("F5 did not start the daemon")
	}
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "Starting  romty daemon") && !strings.Contains(screen, "STARTING") {
		t.Fatalf("no activity while starting:\n%s", screen)
	}
	if again := send(&value, key(tea.KeyF5, "")); again != nil {
		t.Fatal("a second F5 started a second daemon")
	}
	send(&value, command())
	if restarts != 1 || value.daemonDown || value.daemonRestarting {
		t.Fatalf("after restart = (restarts %d, down %v, restarting %v)", restarts, value.daemonDown, value.daemonRestarting)
	}
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "romty daemon started again") {
		t.Fatalf("restart is not reported:\n%s", screen)
	}
}

func TestFailedRestartSaysWhy(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.width, value.height = 110, 16
	value.restartDaemon = func() error { return errors.New("daemon did not become ready") }
	send(&value, agentSnapshotMsg{err: gone})
	command := send(&value, key(tea.KeyF5, ""))
	send(&value, command())
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "romty daemon did not start: daemon did not become ready") {
		t.Fatalf("failed restart is not explained:\n%s", screen)
	}
	if !value.daemonDown {
		t.Fatal("a failed restart left the stopped state")
	}
}

// A daemon started from elsewhere is noticed by the next agent poll.
func TestDaemonStartedElsewhereEndsTheStoppedState(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	send(&value, agentSnapshotMsg{err: gone})
	command := send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{}})
	if value.daemonDown || command == nil {
		t.Fatal("a successful poll did not end the stopped state")
	}
}

// A terminal that ends with the daemon is not reattached in a loop.
func TestTerminalEndedWithTheDaemonIsNotReattached(t *testing.T) {
	value := terminalDashboard(t)
	value.state = projectsSnapshot(nil)
	send(&value, agentSnapshotMsg{err: gone})
	value.terminalExited = true
	updated, command := value.settleAfterExit()
	if command != nil || updated.(dashboard).focus != leftPane {
		t.Fatal("a terminal ended by the daemon was reattached")
	}
}

// Other errors are not mistaken for a stopped daemon.
func TestOrdinaryErrorsAreNotADaemonStop(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	send(&value, snapshotMsg{err: errors.New("root is unreadable")})
	if value.daemonDown || !strings.Contains(value.errorMessage, "root is unreadable") {
		t.Fatalf("ordinary error = (down %v, message %q)", value.daemonDown, value.errorMessage)
	}
}
