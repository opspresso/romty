// The status bar's one message: who raised it and when it may be cleared.

package ui

// errorSource records what raised the message in the status bar. There is one
// slot, so without an owner any handler could erase any other's failure — and
// the refresh a user runs right after a failure did exactly that.
type errorSource int

// setError takes the status bar for one source; clearError gives it up only if
// that source still holds it.
func (m *dashboard) setError(source errorSource, message string) {
	m.errorMessage = message
	m.errorFrom = source
	m.noticeMessage = false
}

// setNotice takes the status bar the way setError does, for a state romty is
// reporting rather than a failure. Scrollback with nothing to show is not a
// fault of the terminal or of the user, so the bar says NOTE in the muted
// colours instead of raising a red ERROR.
func (m *dashboard) setNotice(source errorSource, message string) {
	m.setError(source, message)
	m.noticeMessage = true
}

func (m *dashboard) clearError(source errorSource) {
	if m.errorFrom == source || m.errorFrom == noError {
		m.errorMessage = ""
		m.errorFrom = noError
	}
}

func (m *dashboard) clearAnyError() {
	m.errorMessage = ""
	m.errorFrom = noError
}

const (
	noError errorSource = iota
	// treeError is about the workspace tree or the daemon behind it, so a
	// fresh snapshot answers it.
	treeError
	// terminalError is about the open terminal and stands until that terminal
	// is opened, closed, or replaced.
	terminalError
	// settingError is about config or shutting the daemon down, which a
	// snapshot says nothing about.
	settingError
	// gitError is about an explicit Git action and is cleared only by another
	// Git action, not by a workspace or terminal refresh.
	gitError
	// daemonError says the daemon is gone. It stands until the daemon
	// answers again, whatever else fails in the meantime.
	daemonError
)
