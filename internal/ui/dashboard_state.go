// The dashboard's state, by the feature that owns it. The dashboard embeds
// each part, so code reads a field as m.navIndex wherever it lives; this file
// is where each one's owner and meaning are written down.

package ui

import (
	"time"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// daemonLink is the connection to the daemon: whether it answers, its build, the change stream, and the order of the snapshots it sent.
type daemonLink struct {
	// restartDaemon starts the daemon again after it stopped under the TUI,
	// the way starting romty does. daemonDown is set while it is gone, and
	// daemonRestarting while F5 is starting it.
	restartDaemon    func() error
	daemonDown       bool
	daemonRestarting bool
	// daemonBuild is the running daemon's build as it reported it, and
	// daemonOutdated is set when that is not this romty's build: an upgrade
	// replaced the binary, but the daemon that runs the terminals is the old
	// one until it restarts.
	daemonBuild    string
	daemonOutdated bool
	// events is the daemon's change stream while subscribed; polling is set
	// instead for a daemon that predates the stream.
	events  model.EventStream
	polling bool
	// snapshotOrder breaks ties between snapshots of the same daemon state,
	// such as two directory refreshes that complete in reverse order.
	snapshotOrder   uint64
	snapshotApplied uint64
}

// alertState is what romty has heard from agents and bells, so a transition is announced once and an animation runs only while one is busy.
type alertState struct {
	agentAnimationFrame   int
	agentAnimationActive  bool
	agentAnimationPending bool
	agentSoundReady       bool
	// heardAgents is each tab's agent status as the last agent poll reported
	// it. Sounds are judged against it rather than against the tree, because
	// every snapshot carries the daemon's current statuses too: one landing
	// between two polls would otherwise swallow the transition.
	heardAgents map[string]model.AgentStatus
	// lastAlertTab is the waiting agent the last notification named, which
	// a is first to open.
	lastAlertTab string
	// seenBells is when the user last had each tab open, and knownBells the
	// last bell of each tab as the previous tree reported it. Both are
	// replaced, never written in place, because dashboard copies share them.
	seenBells  map[string]time.Time
	knownBells map[string]time.Time
}

// treeState is the workspace tree's cursor, viewport and filter.
type treeState struct {
	navIndex int
	// navOffset is the first workspace tree item in the viewport. The wheel
	// moves it without moving the cursor; keyboard navigation changes it only
	// when the cursor would otherwise leave the screen.
	navOffset int
	// cursorPath is what the cursor is actually on. navIndex is only where
	// that lands in the tree as it stands, and the tree is rebuilt on every
	// refresh.
	cursorPath string
	// treeFiltering is set while / has the keyboard in the workspace pane,
	// and treeQuery narrows the tree to the workspaces that match it.
	treeFiltering bool
	treeQuery     string
}

// tabState is the tab cursor, the open workspace, and the tab requests in flight.
type tabState struct {
	tabIndex         int
	selectionRequest uint64
	tabPending       bool
	tabClosePending  string
	tabCloseActive   bool
	// closeTabTarget is the tab its confirmation is asking about, held for the
	// same reason, with the position it was drawn at so the cursor can take
	// the place of the tab that leaves.
	closeTabTarget model.Tab
	closeTabIndex  int
	// lastTabClick is the previous click on a tab label, which is what turns
	// a second click on the same tab into a double click.
	lastTabClick tabClick
	// lastTabs is the tab last opened in each workspace, by workspace ID, so
	// coming back to a workspace lands on the terminal the user left rather
	// than on its first one. It is replaced, never written in place, because
	// copies of the dashboard share the map.
	lastTabs map[string]string
	// tabMovePending is set while a tab move waits for the daemon.
	tabMovePending      bool
	selectedWorkspaceID string
	selectedPath        string
	restorePending      bool
}

// terminalState is the embedded terminal on screen and its scrollback.
type terminalState struct {
	terminal       *embeddedTerminal
	terminalExited bool
	// reattachTab and reattachAttempts back off repeated reattaches to a tab
	// whose connection keeps dropping: each attach replays the recording, and
	// a client that falls behind it is cut off again.
	reattachTab      string
	reattachAttempts int
	// terminalOpenedAt is when the open terminal attached, which is what tells
	// a drop that continues a loop from one that starts a new incident.
	terminalOpenedAt time.Time
	scrollback       bool
	// altScroll is the host's alternate scroll as romty last set it, so the
	// sequence is sent on the transitions and not on every message.
	altScroll bool
	// terminalFullWidth is the narrow layout as the PTY was last told it, so
	// hiding or restoring the workspace pane resizes once rather than every
	// message asking the daemon for the size it already has.
	terminalFullWidth bool
	scrollOffset      int
	search            scrollbackSearch
}

// modalState is the modal on screen and what each modal holds while it is open.
type modalState struct {
	modal              modal
	input              inputState
	shutdownPending    bool
	hookStatuses       []agenthooks.Status
	hooksSkipped       map[model.Agent]bool
	hookInstallPending bool
	// hooksDeferred holds the agent hook offer until the first root exists.
	hooksDeferred bool
	// removeTarget is the item the confirmation modal is asking about, held so
	// the answer applies to the item the question named.
	removeTarget navItem
	// removeConfirm is the name typed into the Delete workspace dialog.
	// Deleting removes the directory and everything in it, so the dialog
	// accepts only after the workspace's name is typed exactly.
	removeConfirm string
	helpOffset    int
	// helpQuery filters the Help reference; helpTyping is set while `/` has
	// the keyboard typing into it.
	helpQuery  string
	helpTyping bool
	// configIndex is the setting the Config modal's cursor is on, so it is
	// walked and run the way every other list romty draws is.
	configIndex int
	// browse is the root picker's state, kept on the dashboard so the modal
	// renders from it and the keys move it.
	browse  browser
	palette workspacePalette
	// branches is the branch picker while it is open, and branchRequest
	// numbers its branch reads.
	branches      branchPicker
	branchRequest uint64
	// newerRelease is the tag of a published release newer than this build,
	// or empty when there is none or the check has not answered.
	newerRelease string
}

// statusState is the message in the status bar and who raised it.
type statusState struct {
	errorMessage  string
	errorFrom     errorSource
	noticeMessage bool
}

// gitViewState is the Git state shown in the tree, the Git actions run, and the file view.
type gitViewState struct {
	gitStates    map[string]gitrepo.State
	gitFetchedAt time.Time
	gitRun       gitActionRun
	fileView     fileViewState
}
