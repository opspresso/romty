// The workspace tree and tab cursors: what is selected, what moving does, and
// how a selection survives a snapshot that rearranged everything under it.

package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/model"
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	git       gitState
	hasGit    bool
	isRoot    bool
	separator bool
	// failure is set on a root romty could not read, so the tree can say why
	// it is empty instead of looking as though the root has no directories.
	failure string
}

// isFocusToggle reports the keys that move between the panes. Ctrl+/ is the one
// romty advertises, and it arrives under two names: a terminal that speaks the
// Kitty keyboard protocol reports the key that was pressed, while every other
// one sends 0x1F, which decodes as Ctrl+_ because that is the other key the
// byte stands for. A phone's SSH client is in the second group, and it is the
// one the narrow layout exists for, so both have to reach the toggle.
//
// Ctrl+\ is kept unadvertised. It is what romty bound before Ctrl+/ and what
// hands already reach for; taking it away bought nothing, so it stays out of
// help and the status bar rather than out of the binding.
func isFocusToggle(key string) bool {
	return key == "ctrl+/" || key == "ctrl+_" || key == "ctrl+\\"
}

// toggleFocus moves between the panes in one key. Both F7 and Ctrl+/ use it so
// either chord can stand in for one intercepted by a desktop environment.
func (m dashboard) toggleFocus() (tea.Model, tea.Cmd) {
	if m.scrollback {
		// Scrollback fills the screen with the terminal, so leaving the
		// terminal means leaving scrollback with it. stopScrollback lands in
		// the terminal, which the branch below then moves away from.
		m.stopScrollback()
	}
	if m.focus == terminalPane {
		m.focusNavigation()
		return m, m.refresh()
	}
	m.focusTerminal()
	return m, nil
}

func (m *dashboard) moveNavigation(delta int) {
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		m.navOffset = 0
		m.tabIndex = 0
		return
	}
	// Stopping at the ends rather than wrapping, as the picker, help and
	// scrollback all do; the tree used to be alone in sending a press past the
	// last row back to the first.
	m.navIndex = min(max(m.navIndex+delta, 0), len(items)-1)
	m.cursorPath = items[m.navIndex].workspace.Path
	m.syncTabCursor(runningTabs(items[m.navIndex].tabs))
	m.ensureNavigationVisible()
}

// scrollNavigation moves the workspace viewport without changing its cursor or
// keyboard focus. A wheel gesture is for reading another part of the tree; it
// must not silently change which workspace Enter will open.
func (m *dashboard) scrollNavigation(delta int) {
	items := m.navigationItems()
	start, _ := navigationWindow(items, m.navOffset, m.navigationCapacity())
	start, _ = navigationWindow(items, start+delta, m.navigationCapacity())
	m.navOffset = start
}

func (m dashboard) navigationCapacity() int {
	return max(m.dimensions().bodyHeight-2, 0)
}

func (m *dashboard) clampNavigationOffset() {
	start, _ := navigationWindow(m.navigationItems(), m.navOffset, m.navigationCapacity())
	m.navOffset = start
}

// ensureNavigationVisible moves the viewport by the least number of complete
// items needed to keep the keyboard cursor on screen. Moving inside the window
// leaves it still; crossing an edge scrolls only that edge into view.
func (m *dashboard) ensureNavigationVisible() {
	items := m.navigationItems()
	if len(items) == 0 {
		m.navOffset = 0
		return
	}
	available := m.navigationCapacity()
	start, end := navigationWindow(items, m.navOffset, available)
	m.navOffset = start
	cursor := min(max(m.navIndex, 0), len(items)-1)
	switch {
	case cursor < start:
		m.navOffset = cursor
	case cursor >= end:
		used := 0
		for index := start; index <= cursor; index++ {
			used += navigationRows(items[index])
		}
		for start < cursor && used > available {
			used -= navigationRows(items[start])
			start++
		}
		m.navOffset = start
	}
}

func (m *dashboard) moveTab(delta int) {
	if _, ok := m.navigationItem(); !ok {
		m.tabIndex = 0
		return
	}
	count := len(m.navigationTabs()) + 1
	m.tabIndex = (m.tabIndex + delta + count) % count
}

func (m dashboard) newTab() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.tabPending {
		return m, nil
	}
	if m.focus == leftPane && !m.scrollback {
		if _, ok := m.navigationItem(); !ok {
			return m, nil
		}
		m.tabIndex = len(m.navigationTabs())
		return m, m.selectWorkspace()
	}
	if m.terminal == nil || m.selectedWorkspaceID == "" {
		return m, nil
	}
	m.tabIndex = len(m.selectedTabs())
	m.tabPending = true
	return m, m.createTab()
}

// switchTab opens the tab one step along the row the user is looking at, which
// is what Left/Right and Enter do together. The row is the one renderTerminal
// draws: the open terminal's tabs everywhere but the workspace pane, where the
// cursor may sit on a workspace other than the open one. Switching along the
// row that is not on screen would open a tab from a workspace the user is not
// looking at.
func (m dashboard) switchTab(delta int) (tea.Model, tea.Cmd) {
	if m.modal != noModal {
		// A modal is a question waiting for an answer, and the switch would
		// land behind it.
		return m, nil
	}
	cursorRow := m.focus == leftPane && !m.scrollback
	tabs := m.selectedTabs()
	if cursorRow {
		tabs = m.navigationTabs()
	}
	next, ok := m.nextTab(tabs, delta)
	if !ok {
		return m, nil
	}
	m.tabIndex = next
	if cursorRow {
		// The workspace under the cursor is not necessarily the open one, so
		// it has to be selected before one of its tabs can be opened.
		return m, m.selectWorkspace()
	}
	return m, m.openSelectedTerminal()
}

// switchWorkspace opens a terminal in the workspace delta steps along the ones
// that have a terminal running.
//
// Only those are stops. A root lists every child directory whether it has ever
// been used or not, so stepping through all of them would make the chord a
// slower way to hold Down — the tree already has plain Up and Down for walking
// everything, and the tab markers are what say where the work is.
func (m dashboard) switchWorkspace(delta int) (tea.Model, tea.Cmd) {
	if m.modal != noModal {
		// A modal is a question waiting for an answer, and the switch would
		// land behind it.
		return m, nil
	}
	items := m.navigationItems()
	// Anchored on the workspace that is open rather than on the cursor, so the
	// chord walks one cycle from either pane and every press lands somewhere
	// new. Anchoring on the cursor would make the key do nothing whenever it
	// pointed at the workspace before the open one. The cursor stands in only
	// when nothing is open for the walk to start from.
	anchor := workspaceIndex(items, m.selectedPath)
	if anchor < 0 {
		anchor = m.navIndex
	}
	next, ok := nextOccupiedWorkspace(items, anchor, delta)
	if !ok {
		return m, nil
	}
	m.setNavigation(next)
	// The first terminal of the workspace being moved to. Which of its tabs to
	// land on is what Left and Right are for.
	m.tabIndex = 0
	return m, m.selectWorkspace()
}

// nextOccupiedWorkspace is the workspace with a terminal running that lies one
// step from anchor in the direction of delta, wrapping at both ends. The
// anchor need not be one itself: a cursor sitting on an empty directory is
// between two stops, and stepping from there lands on the nearer one.
//
// It reports false when there is nowhere to go — no workspace has a terminal,
// or the only one that does is where the anchor already sits, and reopening
// that would tear a live terminal down and attach a new one in its place.
func nextOccupiedWorkspace(items []navItem, anchor, delta int) (int, bool) {
	occupied := make([]int, 0, len(items))
	for index, item := range items {
		if len(runningTabs(item.tabs)) > 0 {
			occupied = append(occupied, index)
		}
	}
	if len(occupied) == 0 {
		return 0, false
	}
	if delta >= 0 {
		for _, index := range occupied {
			if index > anchor {
				return index, true
			}
		}
		first := occupied[0]
		return first, first != anchor
	}
	for position := len(occupied) - 1; position >= 0; position-- {
		if occupied[position] < anchor {
			return occupied[position], true
		}
	}
	last := occupied[len(occupied)-1]
	return last, last != anchor
}

// workspaceIndex is where the workspace at path sits in the tree, or -1 when
// it is not in it: a root that was forgotten takes its workspaces with it, and
// nothing is open before the first terminal is.
func workspaceIndex(items []navItem, path string) int {
	if path == "" {
		return -1
	}
	for index, item := range items {
		if item.workspace.Path == path {
			return index
		}
	}
	return -1
}

// nextTab is the tab delta steps along tabs, wrapping at both ends and never
// landing on the new-tab slot: a key that switches tabs must not create one. It
// reports false when there is nothing to switch to, which includes the tab that
// is already open — reopening it would tear a live terminal down and attach a
// new one in its place.
func (m dashboard) nextTab(tabs []model.Tab, delta int) (int, bool) {
	if len(tabs) == 0 {
		return 0, false
	}
	current := m.tabIndex
	if current >= len(tabs) {
		// The new-tab slot sits past the last tab, so stepping right from it
		// wraps to the first tab and stepping left lands on the last.
		current = len(tabs)
		if delta > 0 {
			current = -1
		}
	}
	next := (current + delta + len(tabs)) % len(tabs)
	if m.terminal != nil && m.terminal.id == tabs[next].ID {
		return 0, false
	}
	return next, true
}

func (m *dashboard) clampTabIndex() {
	if _, ok := m.navigationItem(); !ok {
		m.tabIndex = 0
		return
	}
	count := len(m.navigationTabs())
	if m.tabIndex > count {
		m.tabIndex = count
	}
}

// ensureWorkspaceCursor keeps the cursor on the item it was on, by path rather
// than by position. The tree is rebuilt from the daemon on every refresh, so a
// directory appearing above the cursor — a `git clone` finishing in another
// terminal — used to slide the highlight onto its neighbour without the user
// touching a key, and Enter then opened the wrong workspace.
func (m *dashboard) ensureWorkspaceCursor() {
	defer m.clampNavigationOffset()
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		return
	}
	if m.cursorPath != "" {
		for index, item := range items {
			if item.workspace.Path == m.cursorPath {
				m.navIndex = index
				return
			}
		}
	}
	// The remembered item is gone, or there is none yet: fall back to the
	// position and record whatever that lands on.
	m.navIndex = min(max(m.navIndex, 0), len(items)-1)
	m.cursorPath = items[m.navIndex].workspace.Path
}

// setNavigation moves the cursor and records what it landed on, so a rebuilt
// tree can find the same item again. Setting navIndex alone leaves the two
// disagreeing, and the next refresh undoes the move.
func (m *dashboard) setNavigation(index int) {
	m.navIndex = index
	if item, ok := m.navigationItem(); ok {
		m.cursorPath = item.workspace.Path
	}
	m.ensureNavigationVisible()
}

// focusTerminal moves the keyboard into the terminal, if there is one still
// running to move into. A terminal whose shell has exited is not somewhere the
// keyboard can go, so the workspace pane keeps it.
func (m *dashboard) focusTerminal() {
	if m.terminal == nil {
		return
	}
	m.focus = terminalPane
	// Terminal focus stops the pointer motion that hover is drawn from, so a
	// highlight left standing here would outlive the pointer that made it.
	m.hover = hoverTarget{}
	m.syncTabCursor(m.selectedTabs())
}

func (m *dashboard) focusNavigation() {
	m.focus = leftPane
	items := m.navigationItems()
	for index, item := range items {
		if item.workspace.Path == m.selectedPath {
			m.setNavigation(index)
			m.syncTabCursor(runningTabs(item.tabs))
			return
		}
	}
}

func (m *dashboard) syncTabCursor(tabs []model.Tab) {
	m.tabIndex = 0
	if m.terminal == nil {
		return
	}
	for index, tab := range tabs {
		if tab.ID == m.terminal.id {
			m.tabIndex = index
			return
		}
	}
}

func (m *dashboard) syncSelection() {
	for _, root := range m.state.Roots {
		if root.Root.Path == m.selectedPath {
			if len(root.Tabs) > 0 {
				m.selectedWorkspaceID = root.Tabs[0].WorkspaceID
			}
			return
		}
		for _, directory := range root.Directories {
			if directory.Workspace.Path == m.selectedPath {
				m.selectedWorkspaceID = directory.Workspace.ID
				return
			}
		}
	}
	m.selectedWorkspaceID = ""
	m.selectedPath = ""
}

func (m *dashboard) restoreSelection() {
	path, tabID := m.config.LastWorkspacePath, m.config.LastTabID
	if path == "" || tabID == "" {
		return
	}
	for navIndex, item := range m.navigationItems() {
		if item.workspace.Path != path {
			continue
		}
		for tabIndex, tab := range runningTabs(item.tabs) {
			if tab.ID != tabID {
				continue
			}
			m.setNavigation(navIndex)
			m.tabIndex = tabIndex
			m.selectedWorkspaceID = tab.WorkspaceID
			m.selectedPath = path
			m.rememberedWorkspacePath = path
			m.rememberedTabID = tabID
			m.restorePending = true
			return
		}
	}
}

func (m *dashboard) rememberSelection(tabID string) bool {
	for _, item := range m.navigationItems() {
		if item.workspace.Path != m.selectedPath {
			continue
		}
		for _, tab := range runningTabs(item.tabs) {
			if tab.ID != tabID {
				continue
			}
			changed := m.rememberedWorkspacePath != item.workspace.Path || m.rememberedTabID != tab.ID
			m.rememberedWorkspacePath = item.workspace.Path
			m.rememberedTabID = tab.ID
			return changed
		}
	}
	return false
}

func (m dashboard) navigationItems() []navItem {
	result := make([]navItem, 0)
	for rootIndex, root := range m.state.Roots {
		// A root is only a workspace once it holds a terminal, so a snapshot
		// names its identifier nowhere else. Its own tabs carry it, and
		// without it the root row compares as a workspace that never matches.
		rootWorkspaceID := ""
		if len(root.Tabs) > 0 {
			rootWorkspaceID = root.Tabs[0].WorkspaceID
		}
		result = append(result, navItem{
			root: root.Root,
			workspace: model.Workspace{
				ID:     rootWorkspaceID,
				RootID: root.Root.ID,
				Name:   root.Root.Name,
				Path:   root.Root.Path,
			},
			tabs:      root.Tabs,
			isRoot:    true,
			separator: rootIndex > 0,
			failure:   root.Error,
		})
		for _, directory := range root.Directories {
			directoryGit, directoryHasGit := m.gitStates[directory.Workspace.Path]
			result = append(result, navItem{
				root:      root.Root,
				workspace: directory.Workspace,
				tabs:      directory.Tabs,
				git:       directoryGit,
				hasGit:    directoryHasGit,
			})
		}
	}
	return result
}

func (m dashboard) navigationItem() (navItem, bool) {
	items := m.navigationItems()
	if m.navIndex < 0 || m.navIndex >= len(items) {
		return navItem{}, false
	}
	return items[m.navIndex], true
}

func (m dashboard) navigationTabs() []model.Tab {
	item, ok := m.navigationItem()
	if !ok {
		return nil
	}
	return runningTabs(item.tabs)
}

// visibleTabs is the tab row that is actually on screen: the open terminal's
// tabs, except in the workspace pane, where the cursor may sit on a workspace
// other than the open one and the rail shows that workspace's tabs instead.
// Drawing the rail, hit-testing a click on it and hovering it are three
// readings of the same row, and each kept its own copy of that rule.
func (m dashboard) visibleTabs() []model.Tab {
	if m.focus == leftPane {
		return m.navigationTabs()
	}
	return m.selectedTabs()
}

func (m dashboard) selectedTabs() []model.Tab {
	for _, root := range m.state.Roots {
		for _, tab := range root.Tabs {
			if tab.WorkspaceID == m.selectedWorkspaceID {
				return runningTabs(root.Tabs)
			}
		}
		for _, directory := range root.Directories {
			if directory.Workspace.ID == m.selectedWorkspaceID {
				return runningTabs(directory.Tabs)
			}
		}
	}
	return nil
}

// selectedTabID names the tab the cursor is on among the open workspace's
// tabs, and is empty on the new-tab slot. It reads exactly what
// openSelectedTerminal reads, so it answers "is this still the tab romty asked
// for" rather than approximating it.
func (m dashboard) selectedTabID() string {
	tab, ok := m.openTab()
	if !ok {
		return ""
	}
	return tab.ID
}

// openTab is the tab the cursor names among the open terminal's own tabs. The
// new-tab slot is not one, so it reports false there.
func (m dashboard) openTab() (model.Tab, bool) {
	tabs := m.selectedTabs()
	if m.tabIndex < 0 || m.tabIndex >= len(tabs) {
		return model.Tab{}, false
	}
	return tabs[m.tabIndex], true
}

func runningTabs(tabs []model.Tab) []model.Tab {
	result := make([]model.Tab, 0, len(tabs))
	for _, tab := range tabs {
		if tab.Running {
			result = append(result, tab)
		}
	}
	return result
}
