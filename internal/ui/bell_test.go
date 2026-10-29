package ui

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

// A guest's copy reaches the host clipboard; a read is never answered, the
// replayed history copies nothing, and Config can block copying.
func TestGuestCopiesReachTheHostClipboard(t *testing.T) {
	terminal := newEmbeddedTerminalWithReplay("t", newMemoryStream(""), []byte(osc52("from the replay")), 40, 4)
	defer terminal.close()
	if _, copies := terminal.takeEffects(); len(copies) != 0 {
		t.Fatalf("the replay copied %q", copies)
	}
	terminal.writeOutput([]byte(osc52("hello") + "\x1b]52;c;?\a"))
	if _, copies := terminal.takeEffects(); len(copies) != 1 || copies[0] != "hello" {
		t.Fatalf("copies = %q, want the write alone", copies)
	}

	value := terminalDashboard(t)
	value.terminal.writeOutput([]byte(osc52("selected")))
	messages := commandMessages(value.terminalEffects())
	if len(messages) != 1 || !strings.Contains(strings.ToLower(typeName(messages[0])), "clipboard") {
		t.Fatalf("effects = %#v, want one clipboard write", messages)
	}
	value.config.BlockClipboard = true
	value.terminal.writeOutput([]byte(osc52("blocked")))
	if command := value.terminalEffects(); command != nil {
		t.Fatalf("a blocked copy produced %#v", commandMessages(command))
	}
}

func typeName(value any) string {
	return fmt.Sprintf("%T", value)
}

// The open terminal's bell goes to the host terminal.
func TestOpenTerminalBellRingsTheHost(t *testing.T) {
	value := terminalDashboard(t)
	value.terminal.writeOutput([]byte("done\a"))
	messages := commandMessages(value.terminalEffects())
	if len(messages) != 1 || messages[0] != (tea.RawMsg{Msg: "\a"}) {
		t.Fatalf("effects = %#v, want BEL to the host", messages)
	}
}

// A bell in a tab not on screen is marked and announced once; opening the tab
// marks it heard.
func TestBellInAnotherTabIsMarkedAndAnnounced(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(openTabs(2))
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 110, 16
	value.focus = leftPane
	send(&value, snapshotMsg{value: snapshot})

	rang := snapshot
	rang.Revision++
	rang.Roots[0].Directories[0].Tabs = append([]model.Tab(nil), snapshot.Roots[0].Directories[0].Tabs...)
	rang.Roots[0].Directories[0].Tabs[1].BellAt = time.Now()
	command := send(&value, snapshotMsg{value: rang})
	announced := false
	for _, message := range commandMessages(command) {
		if raw, ok := message.(tea.RawMsg); ok && strings.Contains(raw.Msg.(string), "romty bell · alpha / 2") {
			announced = true
		}
	}
	if !announced {
		t.Fatal("the bell was not announced")
	}
	if summary := value.attention().summary(); summary != "!1" {
		t.Fatalf("attention = %q, want the bell counted", summary)
	}
	if row := ansi.Strip(strings.Join(value.renderNavigation(30, 10), "\n")); !strings.Contains(row, "!") {
		t.Fatalf("tree lacks the bell mark:\n%s", row)
	}
	value.markBellSeen("tab-2")
	if value.attention().bells != 0 {
		t.Fatal("opening the tab did not mark its bell heard")
	}
}
