// Branch picker: the list a workspace's checked-out branch is switched from.
// Typing filters the list, and a name that matches no branch can be created.

package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// branchPicker is the open branch picker.
type branchPicker struct {
	target model.Workspace
	// agents names what runs in the workspace's terminals. They share its
	// directory, so a switch changes the files under them, and the picker
	// asks first.
	agents []string
	// request numbers the branch read, so the answer to a read made for a
	// picker since closed and reopened is not applied to the new one.
	request  uint64
	loading  bool
	branches []gitrepo.Branch
	err      string
	query    string
	index    int
	offset   int
	// confirming is the switch waiting for the user to accept that the
	// running programs will see the files change.
	confirming *gitrepo.SwitchRequest
}

type branchListMsg struct {
	path     string
	request  uint64
	branches []gitrepo.Branch
	err      error
}

// branchRow is one row of the picker: a branch, or the row that creates the
// typed name as a new branch.
type branchRow struct {
	branch gitrepo.Branch
	create bool
}

func (r branchRow) request() gitrepo.SwitchRequest {
	return gitrepo.SwitchRequest{Branch: r.branch.Name, Remote: r.branch.Remote, Create: r.create}
}

// openBranchPicker opens the picker on target and reads its branches in the
// background, so a large repository does not hold up the dashboard.
func (m dashboard) openBranchPicker(target navItem) (tea.Model, tea.Cmd) {
	if !target.hasGit {
		m.modal = noModal
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	agents := make([]string, 0)
	for _, tab := range runningTabs(target.tabs) {
		if name := agentDisplayNames[tab.Agent]; name != "" {
			agents = append(agents, name)
		}
	}
	m.modal = branchModal
	m.branchRequest++
	m.branches = branchPicker{target: target.workspace, agents: agents, loading: true, request: m.branchRequest}
	m.clearAnyError()
	path, request := target.workspace.Path, m.branchRequest
	return m, func() tea.Msg {
		branches, err := gitrepo.Branches(context.Background(), path)
		return branchListMsg{path: path, request: request, branches: branches, err: err}
	}
}

func (m dashboard) handleBranchList(message branchListMsg) (tea.Model, tea.Cmd) {
	if m.modal != branchModal || message.path != m.branches.target.Path || message.request != m.branches.request {
		return m, nil
	}
	m.branches.loading = false
	m.branches.branches = message.branches
	if message.err != nil {
		m.branches.err = message.err.Error()
	}
	m.branches.index, m.branches.offset = m.firstBranchChoice(), 0
	return m, nil
}

// firstBranchChoice is where the cursor starts in the list as filtered: the
// first branch that is not the current one, which is the branch most likely
// wanted next. The create row is never the start, so typing ahead of a slow
// read cannot leave Enter on a branch the user did not choose to create.
func (m dashboard) firstBranchChoice() int {
	rows := m.branchRows()
	for index, row := range rows {
		if !row.branch.Current && !row.create {
			return index
		}
	}
	for index, row := range rows {
		if !row.branch.Current {
			return index
		}
	}
	return 0
}

// branchRows is the list as filtered by the query, ignoring case, and the row
// that creates the query as a branch when no branch has that exact name.
func (m dashboard) branchRows() []branchRow {
	query := strings.TrimSpace(m.branches.query)
	rows := make([]branchRow, 0, len(m.branches.branches)+1)
	exact := false
	for _, branch := range m.branches.branches {
		if query != "" && !strings.Contains(strings.ToLower(branch.Name), strings.ToLower(query)) {
			continue
		}
		if branch.Name == query {
			exact = true
		}
		rows = append(rows, branchRow{branch: branch})
	}
	if query != "" && !exact {
		rows = append(rows, branchRow{branch: gitrepo.Branch{Name: query}, create: true})
	}
	return rows
}

func (m dashboard) handleBranchKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.branches.confirming != nil {
		switch message.String() {
		case "enter":
			request := *m.branches.confirming
			return m.startBranchSwitch(request)
		case "esc":
			m.branches.confirming = nil
		}
		return m, nil
	}
	rows := m.branchRows()
	// Until the list is on screen there is nothing to choose: Enter would run
	// a row the user cannot see.
	ready := !m.branches.loading && m.branches.err == ""
	switch message.String() {
	case "esc":
		if m.branches.query != "" {
			m.branches.query = ""
			m.branches.index, m.branches.offset = m.firstBranchChoice(), 0
			return m, nil
		}
		m.modal = noModal
	case "up":
		m.moveBranch(-1, len(rows))
	case "down":
		m.moveBranch(1, len(rows))
	case "enter":
		if ready && m.branches.index < len(rows) {
			return m.chooseBranch(rows[m.branches.index])
		}
	default:
		// Every other key types: branch names are letters, so the list moves
		// with the arrow keys alone. A key that types nothing leaves the
		// cursor where it is.
		query := editText(m.branches.query, message)
		if query != m.branches.query {
			m.branches.query = query
			m.branches.index, m.branches.offset = m.firstBranchChoice(), 0
		}
	}
	return m, nil
}

func (m *dashboard) moveBranch(delta, count int) {
	if count == 0 {
		return
	}
	m.branches.index = min(max(m.branches.index+delta, 0), count-1)
	capacity := branchCapacity(m.dimensions().bodyHeight)
	if m.branches.index < m.branches.offset {
		m.branches.offset = m.branches.index
	} else if m.branches.index >= m.branches.offset+capacity {
		m.branches.offset = m.branches.index - capacity + 1
	}
}

// chooseBranch switches to row, asking first when programs run in the
// workspace. The current branch is already checked out, so it does nothing.
func (m dashboard) chooseBranch(row branchRow) (tea.Model, tea.Cmd) {
	if row.branch.Current {
		return m, nil
	}
	request := row.request()
	if len(m.branches.agents) > 0 {
		m.branches.confirming = &request
		return m, nil
	}
	return m.startBranchSwitch(request)
}

// startBranchSwitch runs the switch and shows its result in the Git result
// view, where every Git command's output is read.
func (m dashboard) startBranchSwitch(request gitrepo.SwitchRequest) (tea.Model, tea.Cmd) {
	target := m.branches.target
	m.branches = branchPicker{}
	m.modal = gitActionsModal
	m.gitRun = gitActionRun{target: target, index: gitActionIndex(gitSwitchAction), action: gitSwitchAction, pending: true}
	ctx, cancel := context.WithCancel(context.Background())
	m.gitRun.cancel = cancel
	path := target.Path
	return m, func() tea.Msg {
		output, err := gitrepo.Switch(ctx, path, request)
		return gitActionMsg{path: path, action: gitSwitchAction, output: output, err: err}
	}
}

// branchHeaderRows are the target, the filter field, and the blank row above
// the list.
const branchHeaderRows = 3

func branchCapacity(height int) int {
	return max(modalCapacity(height)-branchHeaderRows, 1)
}

func (m dashboard) renderBranchModal(maximum, height int) []string {
	target := m.styles.modalStrong.Render(display.Text(m.branches.target.Name)) +
		m.styles.empty.Render("  "+display.Text(m.branches.target.Path))
	title := "Switch branch"
	if request := m.branches.confirming; request != nil {
		return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, maximum, title,
			target,
			"",
			m.styles.modalStrong.Render("Switch to "+display.Text(request.Branch)+"?"),
			m.styles.modalBody.Render(strings.Join(m.branches.agents, ", ")+" running here will see the files change."),
		))
	}
	field := m.renderTextField(m.branches.query, 40)
	if m.branches.query == "" {
		field = m.styles.inputField.Render(" ") + m.styles.empty.Render(" type to filter or to name a new branch")
	}
	lines := []string{target, field, ""}
	switch {
	case m.branches.loading:
		lines = append(lines, m.styles.empty.Render("Reading branches…"))
	case m.branches.err != "":
		lines = append(lines, m.styles.errorText.Render(display.Text(m.branches.err)))
	default:
		rows := m.branchRows()
		if len(rows) == 0 {
			lines = append(lines, m.styles.empty.Render("No branches"))
		}
		offset := min(max(m.branches.offset, 0), max(len(rows)-1, 0))
		end := min(offset+branchCapacity(height), len(rows))
		for index := offset; index < end; index++ {
			lines = append(lines, m.renderBranchRow(rows[index], index))
		}
	}
	return modalBoxFit(m.styles, minimumModalWidth, maximum, title, lines...)
}

func (m dashboard) renderBranchRow(row branchRow, index int) string {
	prefix := "  "
	style := m.styles.modalBody
	note := ""
	switch {
	case row.create:
		style = m.styles.navigationCurrent
		return m.branchRowStyle(index, style).Render(prefix + "+ Create branch " + display.Text(row.branch.Name))
	case row.branch.Current:
		note = "  current"
		style = m.styles.empty
	case row.branch.Remote:
		note = "  remote"
	}
	return m.branchRowStyle(index, style).Render(prefix+display.Text(row.branch.Name)) + m.styles.empty.Render(note)
}

func (m dashboard) branchRowStyle(index int, style lipgloss.Style) lipgloss.Style {
	if index == m.branches.index {
		return m.styles.navigationSelected
	}
	if m.hover.kind == hoverBranchRow && m.hover.index == index {
		return m.styles.hovered(style)
	}
	return style
}

// branchIndexAtContentRow is the list row drawn on a content row of the box.
func (m dashboard) branchIndexAtContentRow(row int) (int, bool) {
	if m.branches.loading || m.branches.err != "" || m.branches.confirming != nil || row < branchHeaderRows {
		return 0, false
	}
	index := m.branches.offset + row - branchHeaderRows
	if index >= len(m.branchRows()) {
		return 0, false
	}
	return index, true
}

func (m dashboard) handleBranchMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	row, inside := m.modalContentRowAt(message.Mouse())
	if !inside {
		return m, nil, false
	}
	if wheel, ok := message.(tea.MouseWheelMsg); ok {
		count := len(m.branchRows())
		switch wheel.Button {
		case tea.MouseWheelUp:
			m.moveBranch(-wheelLines, count)
		case tea.MouseWheelDown:
			m.moveBranch(wheelLines, count)
		}
		return m, nil, true
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return m, nil, false
	}
	index, ok := m.branchIndexAtContentRow(row)
	if !ok {
		return m, nil, true
	}
	m.branches.index = index
	updated, command := m.chooseBranch(m.branchRows()[index])
	return updated, command, true
}

// gitActionIndex is where action sits in the Git menu.
func gitActionIndex(action gitAction) int {
	for index, choice := range gitActionChoices {
		if choice == action {
			return index
		}
	}
	return 0
}
