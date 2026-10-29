// The workspace tree and tab cursors: what is selected, what moving does, and
// how a selection survives a snapshot that rearranged everything under it.

package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	closed    []model.ClosedSession
	git       gitrepo.State
	hasGit    bool
	isRoot    bool
	separator bool
	// collapsed is set on a root whose workspaces the tree hides, and on each
	// of those workspaces.
	collapsed bool
	// hiddenTabs are the running tabs of a collapsed root's workspaces, so the
	// root row still shows what is running under it.
	hiddenTabs []model.Tab
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
	// Stop at the ends rather than wrap, as the picker, help and scrollback
	// do.
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

// cursorOnScreen reports the cursor's index and whether its item is inside
// the viewport.
func (m dashboard) cursorOnScreen() (int, bool) {
	items := m.navigationItems()
	start, end := navigationWindow(items, m.navOffset, m.navigationCapacity())
	return m.navIndex, m.navIndex >= start && m.navIndex < end
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

// moveSelectedTab moves the tab under the tab cursor delta places along the
// row on screen, and the cursor moves with it. The new-tab slot is not a tab
// and does not move.
//
// One move is out at a time: the row on screen is the order before the move
// until the daemon answers, and a second move chosen from it would pick the
// wrong tab. The cursor is put back on the moved tab by its ID from the
// answer, or left where it was when the move failed.
func (m dashboard) moveSelectedTab(delta int) (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.tabMovePending {
		return m, nil
	}
	tabs := m.visibleTabs()
	if m.tabIndex < 0 || m.tabIndex >= len(tabs) {
		return m, nil
	}
	if target := min(max(m.tabIndex+delta, 0), len(tabs)-1); target == m.tabIndex {
		return m, nil
	}
	tabID, backend := tabs[m.tabIndex].ID, m.backend
	m.tabMovePending = true
	return m, func() tea.Msg {
		snapshot, err := backend.MoveTab(tabID, delta)
		return tabMovedMsg{tabID: tabID, snapshot: snapshot, err: err}
	}
}

// tabMovedMsg is the daemon's answer to a tab move.
type tabMovedMsg struct {
	tabID    string
	snapshot model.Snapshot
	err      error
}

func (m dashboard) handleTabMoved(message tabMovedMsg) (tea.Model, tea.Cmd) {
	m.tabMovePending = false
	if message.err != nil {
		if !m.noteDaemonError(message.err) {
			m.setError(terminalError, message.err.Error())
		}
		return m, nil
	}
	m.snapshotOrder++
	m.applySnapshot(m.snapshotOrder, message.snapshot)
	if index := slices.IndexFunc(m.visibleTabs(), func(tab model.Tab) bool { return tab.ID == message.tabID }); index >= 0 {
		m.tabIndex = index
	}
	return m, nil
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
	items := m.workspaceItems()
	// Anchored on the workspace that is open rather than on the cursor, so the
	// chord walks one cycle from either pane and every press lands somewhere
	// new. Anchoring on the cursor would make the key do nothing whenever it
	// pointed at the workspace before the open one. The cursor stands in only
	// when nothing is open for the walk to start from.
	anchor := workspaceIndex(items, m.selectedPath)
	if anchor < 0 {
		anchor = workspaceIndex(items, m.cursorPath)
	}
	next, ok := nextOccupiedWorkspace(items, anchor, delta)
	if !ok {
		return m, nil
	}
	save := m.revealWorkspace(items[next].workspace.Path)
	// The first terminal of the workspace being moved to. Which of its tabs to
	// land on is what Left and Right are for.
	m.tabIndex = 0
	return m, tea.Batch(save, m.selectWorkspace())
}

// revealWorkspace expands the collapsed root that hides the workspace at path
// and puts the cursor on it. A move that opens a workspace has to land on its
// row, and Enter would otherwise open the root in its place. It returns the
// save of the changed config, or nil when no root had to open.
func (m *dashboard) revealWorkspace(path string) tea.Cmd {
	var save tea.Cmd
	all := m.workspaceItems()
	if index := workspaceIndex(all, path); index >= 0 && all[index].collapsed && !all[index].isRoot {
		m.setRootCollapsed(all[index].root.Path, false)
		save = m.saveConfig()
	}
	if index := workspaceIndex(m.navigationItems(), path); index >= 0 {
		m.setNavigation(index)
	}
	return save
}

// toggleRoot collapses or expands the root under the cursor. On a workspace
// it collapses the workspace's root, and the cursor moves up to the root row
// so it stays on something the tree still draws.
func (m dashboard) toggleRoot() (tea.Model, tea.Cmd) {
	item, ok := m.navigationItem()
	if !ok {
		return m, nil
	}
	m.setRootCollapsed(item.root.Path, !(item.isRoot && item.collapsed))
	if index := workspaceIndex(m.navigationItems(), item.root.Path); index >= 0 {
		m.setNavigation(index)
		m.syncTabCursor(runningTabs(m.navigationItems()[index].tabs))
	}
	return m, m.saveConfig()
}

// setRootCollapsed records whether the root at path is collapsed. Roots that
// are no longer in the tree are dropped from the list at the same time, so a
// forgotten root does not stay in the config.
func (m *dashboard) setRootCollapsed(path string, collapsed bool) {
	roots := slices.DeleteFunc(slices.Clone(m.config.CollapsedRoots), func(root string) bool {
		return root == path || !slices.ContainsFunc(m.state.Roots, func(view model.RootView) bool {
			return view.Root.Path == root
		})
	})
	if collapsed {
		roots = append(roots, path)
	}
	if len(roots) == 0 {
		roots = nil
	}
	m.config.CollapsedRoots = roots
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
	for _, o := range slices.Backward(occupied) {
		if o < anchor {
			return o, true
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
	return slices.IndexFunc(items, func(item navItem) bool { return item.workspace.Path == path })
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
// than by position. The tree is rebuilt from the daemon on every refresh, and
// a directory that appears above the cursor — a `git clone` finishing in
// another terminal — must not move the highlight onto its neighbour.
func (m *dashboard) ensureWorkspaceCursor() {
	defer m.clampNavigationOffset()
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		return
	}
	if index := workspaceIndex(items, m.cursorPath); index >= 0 {
		m.navIndex = index
		return
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
	if item, ok := m.placeCursor(m.selectedPath); ok {
		m.syncTabCursor(runningTabs(item.tabs))
	}
}

// placeCursor puts the cursor on the workspace at path, or on the row of the
// collapsed root that hides it, and reports the row it landed on. Showing
// where a workspace is does not expand its root; opening it from a jump does.
func (m *dashboard) placeCursor(path string) (navItem, bool) {
	items := m.navigationItems()
	index := workspaceIndex(items, path)
	if index < 0 {
		if item, ok := m.workspaceItem(path); ok {
			index = workspaceIndex(items, item.root.Path)
		}
	}
	if index < 0 {
		return navItem{}, false
	}
	m.setNavigation(index)
	return items[index], true
}

// workspaceItem is the workspace at path anywhere in the tree, whether or not
// a collapsed root hides it.
func (m dashboard) workspaceItem(path string) (navItem, bool) {
	items := m.workspaceItems()
	if index := workspaceIndex(items, path); index >= 0 {
		return items[index], true
	}
	return navItem{}, false
}

// syncTabCursor puts the tab cursor on the open terminal when it is among
// tabs, and otherwise on the tab last opened in their workspace, so Enter
// returns to where the user was.
func (m *dashboard) syncTabCursor(tabs []model.Tab) {
	m.tabIndex = 0
	want := ""
	if m.terminal != nil {
		want = m.terminal.id
	}
	if !slices.ContainsFunc(tabs, func(tab model.Tab) bool { return tab.ID == want }) && len(tabs) > 0 {
		want = m.lastTabs[tabs[0].WorkspaceID]
	}
	if index := slices.IndexFunc(tabs, func(tab model.Tab) bool { return tab.ID == want }); index >= 0 {
		m.tabIndex = index
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
	if tabID == "" {
		return
	}
	item, ok := m.workspaceItem(path)
	if !ok {
		return
	}
	for tabIndex, tab := range runningTabs(item.tabs) {
		if tab.ID == tabID {
			m.placeCursor(path)
			m.tabIndex = tabIndex
			m.selectedWorkspaceID = tab.WorkspaceID
			m.selectedPath = path
			m.restorePending = true
			return
		}
	}
}

func (m *dashboard) rememberSelection(tabID string) bool {
	item, ok := m.workspaceItem(m.selectedPath)
	if !ok || !slices.ContainsFunc(runningTabs(item.tabs), func(tab model.Tab) bool { return tab.ID == tabID }) {
		return false
	}
	changed := m.config.LastWorkspacePath != m.selectedPath || m.config.LastTabID != tabID
	m.config.LastWorkspacePath, m.config.LastTabID = m.selectedPath, tabID
	return changed
}

// navigationItems is the tree as it is drawn: every root, and the workspaces
// of the roots that are not collapsed. The cursor, the viewport and the mouse
// all count rows in it.
func (m dashboard) navigationItems() []navItem {
	if m.treeQuery != "" {
		return m.filteredItems()
	}
	return slices.DeleteFunc(m.workspaceItems(), func(item navItem) bool {
		return item.collapsed && !item.isRoot
	})
}

// filteredItems is the tree narrowed to the workspaces whose name contains
// the filter, ignoring case, under their roots. A collapsed root does not
// hide a match: finding a workspace is the reason to filter. A root whose own
// name matches is kept even when none of its workspaces do.
func (m dashboard) filteredItems() []navItem {
	query := strings.ToLower(m.treeQuery)
	matches := func(item navItem) bool {
		return strings.Contains(strings.ToLower(item.workspace.Name), query)
	}
	result := make([]navItem, 0)
	all := m.workspaceItems()
	for index := 0; index < len(all); {
		root := all[index]
		end := index + 1
		for end < len(all) && !all[end].isRoot {
			end++
		}
		children := make([]navItem, 0)
		for _, child := range all[index+1 : end] {
			if matches(child) {
				child.collapsed = false
				children = append(children, child)
			}
		}
		if len(children) > 0 || matches(root) {
			root.separator = len(result) > 0
			result = append(result, root)
			result = append(result, children...)
		}
		index = end
	}
	return result
}

// handleTreeFilterKey edits the tree filter while / has the keyboard. The
// arrows walk what matches, Enter opens it and ends the filter, and Esc ends
// the filter where the cursor is.
func (m dashboard) handleTreeFilterKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.endTreeFilter()
	case "tab":
		// Leaving the pane leaves the filter, so the tree is whole when the
		// keyboard comes back.
		m.endTreeFilter()
		m.focusTerminal()
	case "up":
		m.moveNavigation(-1)
	case "down":
		m.moveNavigation(1)
	case "enter":
		item, ok := m.navigationItem()
		m.endTreeFilter()
		if !ok {
			return m, nil
		}
		save := m.revealWorkspace(item.workspace.Path)
		updated, command := m.activateWorkspace()
		return updated, tea.Batch(save, command)
	default:
		query := editText(m.treeQuery, message)
		if query == m.treeQuery {
			return m, nil
		}
		m.treeQuery = query
		m.navOffset = 0
		m.cursorToFirstMatch()
	}
	return m, nil
}

// cursorToFirstMatch puts the cursor on the first workspace the filter keeps,
// or on the first row when only roots match.
func (m *dashboard) cursorToFirstMatch() {
	items := m.navigationItems()
	index := slices.IndexFunc(items, func(item navItem) bool { return !item.isRoot })
	if index < 0 {
		index = 0
	}
	if len(items) > 0 {
		m.setNavigation(index)
		m.syncTabCursor(runningTabs(items[index].tabs))
	}
}

// endTreeFilter shows the whole tree again with the cursor where it was.
func (m *dashboard) endTreeFilter() {
	path := m.cursorPath
	m.treeFiltering, m.treeQuery = false, ""
	if path != "" {
		m.placeCursor(path)
	}
	m.ensureWorkspaceCursor()
}

// workspaceItems is the whole tree, the workspaces of collapsed roots
// included. Finding the open workspace, and the moves that open one the tree
// may be hiding, read it rather than what is on screen.
func (m dashboard) workspaceItems() []navItem {
	result := make([]navItem, 0)
	for _, root := range m.state.Roots {
		// A root is only a workspace once it holds a terminal, so a snapshot
		// names its identifier nowhere else. Its own tabs carry it, and
		// without it the root row compares as a workspace that never matches.
		rootWorkspaceID := ""
		if len(root.Tabs) > 0 {
			rootWorkspaceID = root.Tabs[0].WorkspaceID
		}
		collapsed := slices.Contains(m.config.CollapsedRoots, root.Root.Path)
		rootItem := navItem{
			root: root.Root,
			workspace: model.Workspace{
				ID:     rootWorkspaceID,
				RootID: root.Root.ID,
				Name:   root.Root.Name,
				Path:   root.Root.Path,
			},
			tabs:      root.Tabs,
			closed:    root.Closed,
			isRoot:    true,
			collapsed: collapsed,
			// One blank row sets a root apart from what is above it.
			separator: len(result) > 0,
			failure:   root.Error,
		}
		if collapsed {
			for _, directory := range root.Directories {
				rootItem.hiddenTabs = append(rootItem.hiddenTabs, runningTabs(directory.Tabs)...)
			}
		}
		result = append(result, rootItem)
		for _, directory := range root.Directories {
			directoryGit, directoryHasGit := m.gitStates[directory.Workspace.Path]
			item := navItem{
				root:      root.Root,
				workspace: directory.Workspace,
				tabs:      directory.Tabs,
				closed:    directory.Closed,
				git:       directoryGit,
				hasGit:    directoryHasGit,
				collapsed: collapsed,
			}
			result = append(result, item)
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
// Drawing the rail, hit-testing a click on it and hovering it all read it.
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
