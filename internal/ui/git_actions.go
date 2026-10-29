package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// gitAction is the Git command a choice in the actions modal runs.
type gitAction = gitrepo.Action

const (
	gitStatusAction = gitrepo.ActionStatus
	gitFetchAction  = gitrepo.ActionFetch
	gitPullAction   = gitrepo.ActionPull
	gitPushAction   = gitrepo.ActionPush
)

var gitActionChoices = [...]gitAction{
	gitStatusAction,
	gitFetchAction,
	gitPullAction,
	gitPushAction,
}

// gitActionDescription is what the actions modal says beside an action's name.
func gitActionDescription(action gitAction) string {
	switch action {
	case gitStatusAction:
		return "Show changed files"
	case gitFetchAction:
		return "Update remote refs"
	case gitPullAction:
		return "Fast-forward only"
	case gitPushAction:
		return "Push current branch"
	default:
		return ""
	}
}

// gitActionRun is the Git actions modal: the workspace it acts on, the action
// chosen, and the run and its result.
type gitActionRun struct {
	target model.Workspace
	// returnTo is the modal Enter goes back to after a result.
	returnTo modal
	index    int
	action   gitAction
	pending  bool
	complete bool
	output   string
	err      string
	offset   int
	// cancel stops a running command when the TUI closes.
	cancel func()
}

type gitActionMsg struct {
	path   string
	action gitAction
	output string
	err    error
}

func (m dashboard) openGitActions() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.gitRun.pending {
		return m, nil
	}
	target, ok := m.gitActionWorkspace()
	if !ok || !target.hasGit {
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	m.modal = gitActionsModal
	m.gitRun = gitActionRun{target: target.workspace, action: gitStatusAction}
	m.clearAnyError()
	return m, nil
}

func (m dashboard) gitActionWorkspace() (navItem, bool) {
	if m.focus == leftPane && !m.scrollback {
		return m.navigationItem()
	}
	return m.workspaceItem(m.selectedPath)
}

func (m dashboard) startGitAction() (tea.Model, tea.Cmd) {
	if m.gitRun.pending || m.gitRun.complete || m.gitRun.target.Path == "" {
		return m, nil
	}
	m.gitRun.action = gitActionChoices[m.gitRun.index]
	m.gitRun.pending = true
	m.gitRun.output = ""
	m.gitRun.err = ""
	m.gitRun.offset = 0
	path := m.gitRun.target.Path
	action := m.gitRun.action
	ctx, cancel := context.WithCancel(context.Background())
	m.gitRun.cancel = cancel
	return m, func() tea.Msg {
		output, err := gitrepo.Run(ctx, path, action)
		return gitActionMsg{path: path, action: action, output: output, err: err}
	}
}

func (m dashboard) handleGitActionResult(message gitActionMsg) (tea.Model, tea.Cmd) {
	if message.path != m.gitRun.target.Path || message.action != m.gitRun.action {
		return m, nil
	}
	m.gitRun.pending = false
	m.cancelGitAction()
	m.gitRun.complete = true
	m.gitRun.output = message.output
	if message.err != nil {
		m.gitRun.err = message.err.Error()
	}
	return m, m.readGitStatus(false, false)
}

func (m *dashboard) cancelGitAction() {
	if m.gitRun.cancel == nil {
		return
	}
	m.gitRun.cancel()
	m.gitRun.cancel = nil
}

func (m dashboard) resetGitActionResult() dashboard {
	m.gitRun.complete = false
	m.gitRun.output = ""
	m.gitRun.err = ""
	m.gitRun.offset = 0
	return m
}

func (m dashboard) gitActionResultLines() []string {
	lines := make([]string, 0)
	if m.gitRun.output != "" {
		for line := range strings.SplitSeq(m.gitRun.output, "\n") {
			lines = append(lines, display.Text(line))
		}
	}
	if m.gitRun.err != "" {
		lines = append(lines, "Error: "+display.Text(m.gitRun.err))
	}
	if len(lines) == 0 {
		return []string{"Completed successfully."}
	}
	return lines
}

func (m dashboard) maximumGitActionOffset(height int) int {
	capacity := gitActionResultCapacity(height)
	return max(len(m.gitActionResultLines())-capacity, 0)
}

func gitActionResultCapacity(height int) int {
	return max(modalCapacity(height)-gitActionHeaderRows, 1)
}

func (m dashboard) scrollGitAction(delta int) (tea.Model, tea.Cmd) {
	maximum := m.maximumGitActionOffset(m.dimensions().bodyHeight)
	m.gitRun.offset = min(max(m.gitRun.offset+delta, 0), maximum)
	return m, nil
}

func (m dashboard) handleGitActionsMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	row, inside := m.modalContentRowAt(message.Mouse())
	if !inside {
		return m, nil, false
	}
	if wheel, ok := message.(tea.MouseWheelMsg); ok && m.gitRun.complete {
		switch wheel.Button {
		case tea.MouseWheelUp:
			updated, command := m.scrollGitAction(-wheelLines)
			return updated, command, true
		case tea.MouseWheelDown:
			updated, command := m.scrollGitAction(wheelLines)
			return updated, command, true
		}
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || m.gitRun.pending || m.gitRun.complete {
		return m, nil, false
	}
	index := row - gitActionHeaderRows
	if index < 0 || index >= len(gitActionChoices) {
		return m, nil, true
	}
	m.gitRun.index = index
	updated, command := m.startGitAction()
	return updated, command, true
}

// gitActionHeaderRows are the target name and the blank line the modal draws
// above its list, which is what separates a content row from an action index.
const gitActionHeaderRows = 2

func (m dashboard) renderGitActionsModal(maximum, height int) []string {
	target := m.styles.modalStrong.Render(display.Text(m.gitRun.target.Name)) +
		m.styles.empty.Render("  "+display.Text(m.gitRun.target.Path))
	if m.gitRun.pending {
		return modalBoxFit(m.styles, minimumModalWidth, maximum, "Git · "+m.gitRun.action.String(),
			target,
			"",
			m.styles.modalBody.Render("Running…"),
			"",
		)
	}
	if !m.gitRun.complete {
		// The same highlighted bar the root picker draws, so the cursor on a
		// list that can push stands out as clearly as any other.
		labels := make([]string, len(gitActionChoices))
		contentWidth := lipgloss.Width(target)
		for index, action := range gitActionChoices {
			labels[index] = pad(action.String(), gitActionLabelWidth) + gitActionDescription(action)
			contentWidth = max(contentWidth, lipgloss.Width(labels[index])+2)
		}
		contentWidth = min(contentWidth, max(maximum-6, 0))
		lines := []string{target, ""}
		for index, label := range labels {
			prefix := "  "
			style := m.styles.modalBody
			if index == m.gitRun.index {
				prefix = "▌ "
				style = m.styles.navigationSelected
			} else if m.hover.kind == hoverGitAction && m.hover.index == index {
				style = m.styles.interactiveHover
			}
			lines = append(lines, style.Render(pad(truncate(prefix+label, contentWidth), contentWidth)))
		}
		return modalBoxFit(m.styles, minimumModalWidth, maximum, "Git actions", lines...)
	}

	result := m.gitActionResultLines()
	capacity := gitActionResultCapacity(height)
	offset := min(max(m.gitRun.offset, 0), max(len(result)-capacity, 0))
	end := min(offset+capacity, len(result))
	title := "Git · " + m.gitRun.action.String()
	if len(result) > capacity {
		title += fmt.Sprintf(" %d-%d/%d", offset+1, end, len(result))
	}
	lines := []string{target, ""}
	for index, line := range result[offset:end] {
		hovered := m.hover.kind == hoverGitResult && m.hover.index == index+gitActionHeaderRows
		lines = append(lines, m.renderGitOutputLine(line, hovered))
	}
	return modalBoxFit(m.styles, minimumModalWidth, maximum, title, lines...)
}

// gitActionLabelWidth is the column the action descriptions line up in.
const gitActionLabelWidth = 8

// renderGitOutputLine colours what git printed: a status line's two letters,
// the branch header, and a diffstat's run of + and -, so the result can be
// scanned rather than read.
//
// Hovering tints the row without taking its colours away, the way the file
// view marks the file it has open.
func (m dashboard) renderGitOutputLine(line string, hovered bool) string {
	paint := func(style lipgloss.Style, value string) string {
		if hovered {
			style = style.Background(m.styles.interactiveHover.GetBackground())
		}
		return style.Render(value)
	}
	switch {
	case strings.HasPrefix(line, "Error: "):
		return paint(m.styles.errorText, line)
	case strings.HasPrefix(line, "## "):
		// The branch header `git status --branch` prints, with its divergence
		// in brackets: `## main...origin/main [ahead 1]`.
		branch, divergence, found := strings.Cut(line, " [")
		rendered := paint(m.styles.gitBranch, branch)
		if found {
			rendered += paint(m.styles.gitStatus, " ["+divergence)
		}
		return rendered
	case isGitStatusLine(line):
		return paint(m.gitStatusStyle(line[0], line[1]), line[:2]) + paint(m.styles.modalBody, line[2:])
	}
	if at := gitDiffstatMarks(line); at >= 0 {
		return paint(m.styles.modalBody, line[:at]) + m.paintDiffstatMarks(line[at:], hovered)
	}
	return paint(m.styles.modalBody, line)
}

// isGitStatusLine reports whether a line is one porcelain short-format record:
// two status letters and a space before the path.
func isGitStatusLine(line string) bool {
	if len(line) < 4 || line[2] != ' ' {
		return false
	}
	return isGitStatusCode(line[0]) && isGitStatusCode(line[1]) && line[:2] != "  "
}

func isGitStatusCode(value byte) bool {
	return strings.IndexByte(" MADRCU?!T", value) >= 0
}

// gitDiffstatMarks reports where a diffstat line's run of + and - begins, or
// -1 when the line is not one. The shape is `path | 12 ++++----`, which is
// what pull and push print for every file they moved.
func gitDiffstatMarks(line string) int {
	bar := strings.LastIndex(line, "|")
	if bar < 0 {
		return -1
	}
	at := -1
	for index := bar + 1; index < len(line); index++ {
		switch line[index] {
		case '+', '-':
			if at < 0 {
				at = index
			}
		case ' ', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			if at >= 0 {
				return -1
			}
		default:
			return -1
		}
	}
	return at
}

func (m dashboard) paintDiffstatMarks(marks string, hovered bool) string {
	var result strings.Builder
	for _, mark := range marks {
		style := m.styles.diffRemoved
		if mark == '+' {
			style = m.styles.diffAdded
		}
		if hovered {
			style = style.Background(m.styles.interactiveHover.GetBackground())
		}
		result.WriteString(style.Render(string(mark)))
	}
	return result.String()
}
