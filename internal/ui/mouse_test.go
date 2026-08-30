package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/model"
)

// Terminal focus means the keyboard is at the guest and hover highlights have
// no audience, so the host is not asked to report every pointer move — over
// an SSH session each one crosses the wire. Button events keep the chrome
// clickable, and passthrough still hands a guest exactly what it asked for.
func TestMouseModeFollowsFocusAndPassthrough(t *testing.T) {
	snapshot := model.Snapshot{}
	base := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)

	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()

	for _, probe := range []struct {
		name  string
		setup func(m *dashboard)
		want  tea.MouseMode
	}{
		{
			name:  "workspace focus keeps motion for hover",
			setup: func(m *dashboard) { m.focus = leftPane },
			want:  tea.MouseModeAllMotion,
		},
		{
			name: "terminal focus drops to button events",
			setup: func(m *dashboard) {
				m.focus = terminalPane
				m.terminal = terminal
			},
			want: tea.MouseModeCellMotion,
		},
		{
			name: "a modal wants hover wherever focus is",
			setup: func(m *dashboard) {
				m.focus = terminalPane
				m.terminal = terminal
				m.modal = helpModal
			},
			want: tea.MouseModeAllMotion,
		},
		{
			name: "passthrough hands the guest the motion it asked for",
			setup: func(m *dashboard) {
				m.focus = terminalPane
				m.terminal = terminal
				m.mousePassthrough = true
				terminal.guestMouse[ansi.ModeMouseAnyEvent] = true
			},
			want: tea.MouseModeAllMotion,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := base
			value.modal = noModal
			value.terminal = nil
			value.mousePassthrough = false
			terminal.guestMouse = map[ansi.DECMode]bool{}
			probe.setup(&value)
			if got := value.mouseMode(); got != probe.want {
				t.Fatalf("mouseMode() = %v, want %v", got, probe.want)
			}
		})
	}
}

// Terminal focus stops the motion reports hover is drawn from, so the
// highlight under the pointer at that moment must not outlive it.
func TestFocusTerminalClearsHover(t *testing.T) {
	snapshot := model.Snapshot{}
	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()
	value.terminal = terminal
	value.hover = hoverTarget{kind: hoverTab}
	value.focusTerminal()
	if value.hover != (hoverTarget{}) {
		t.Fatalf("hover = %+v, want cleared", value.hover)
	}
}
