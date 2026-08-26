package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const gitActionTimeout = 2 * time.Minute

type gitAction int

const (
	gitStatusAction gitAction = iota
	gitFetchAction
	gitPullAction
	gitPushAction
)

var gitActionChoices = [...]gitAction{
	gitStatusAction,
	gitFetchAction,
	gitPullAction,
	gitPushAction,
}

func (a gitAction) label() string {
	switch a {
	case gitStatusAction:
		return "Status"
	case gitFetchAction:
		return "Fetch"
	case gitPullAction:
		return "Pull"
	case gitPushAction:
		return "Push"
	default:
		return "Git"
	}
}

func (a gitAction) description() string {
	switch a {
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

func (a gitAction) arguments() ([]string, error) {
	switch a {
	case gitStatusAction:
		return []string{"status", "--short", "--branch"}, nil
	case gitFetchAction:
		return []string{"fetch"}, nil
	case gitPullAction:
		return []string{"pull", "--ff-only"}, nil
	case gitPushAction:
		return []string{"push"}, nil
	default:
		return nil, fmt.Errorf("unknown Git action %d", a)
	}
}

type gitActionMsg struct {
	path   string
	action gitAction
	output string
	err    error
}

func executeGitAction(path string, action gitAction) (string, error) {
	return executeGitActionContext(context.Background(), path, action)
}

func executeGitActionContext(parent context.Context, path string, action gitAction) (string, error) {
	arguments, err := action.arguments()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, gitActionTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", path}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	output, err := command.CombinedOutput()
	value := strings.TrimSpace(string(output))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return value, fmt.Errorf("%s timed out after %s", action.label(), gitActionTimeout)
	}
	return value, err
}

func (m dashboard) openGitActions() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.gitActionPending {
		return m, nil
	}
	target, ok := m.gitActionWorkspace()
	if !ok || !target.hasGit {
		m.setError(gitError, "selected workspace is not a Git repository")
		return m, nil
	}
	m.modal = gitActionsModal
	m.gitActionTarget = target.workspace
	m.gitActionIndex = 0
	m.gitAction = gitStatusAction
	m.gitActionPending = false
	m.gitActionComplete = false
	m.gitActionOutput = ""
	m.gitActionError = ""
	m.gitActionOffset = 0
	m.clearAnyError()
	return m, nil
}

func (m dashboard) gitActionWorkspace() (navItem, bool) {
	if m.focus == leftPane && !m.scrollback {
		return m.navigationItem()
	}
	for _, item := range m.navigationItems() {
		if item.workspace.Path == m.selectedPath {
			return item, true
		}
	}
	return navItem{}, false
}

func (m dashboard) startGitAction() (tea.Model, tea.Cmd) {
	if m.gitActionPending || m.gitActionComplete || m.gitActionTarget.Path == "" {
		return m, nil
	}
	m.gitAction = gitActionChoices[m.gitActionIndex]
	m.gitActionPending = true
	m.gitActionOutput = ""
	m.gitActionError = ""
	m.gitActionOffset = 0
	path := m.gitActionTarget.Path
	action := m.gitAction
	ctx, cancel := context.WithCancel(context.Background())
	m.gitActionCancel = cancel
	return m, func() tea.Msg {
		output, err := executeGitActionContext(ctx, path, action)
		return gitActionMsg{path: path, action: action, output: output, err: err}
	}
}

func (m dashboard) handleGitActionResult(message gitActionMsg) (tea.Model, tea.Cmd) {
	if message.path != m.gitActionTarget.Path || message.action != m.gitAction {
		return m, nil
	}
	m.gitActionPending = false
	m.cancelGitAction()
	m.gitActionComplete = true
	m.gitActionOutput = message.output
	if message.err != nil {
		m.gitActionError = message.err.Error()
	}
	return m, m.readGitStatus(false, false)
}

func (m *dashboard) cancelGitAction() {
	if m.gitActionCancel == nil {
		return
	}
	m.gitActionCancel()
	m.gitActionCancel = nil
}

func (m dashboard) resetGitActionResult() dashboard {
	m.gitActionComplete = false
	m.gitActionOutput = ""
	m.gitActionError = ""
	m.gitActionOffset = 0
	return m
}

func (m dashboard) gitActionResultLines() []string {
	lines := make([]string, 0)
	if m.gitActionOutput != "" {
		for _, line := range strings.Split(m.gitActionOutput, "\n") {
			lines = append(lines, displayText(line))
		}
	}
	if m.gitActionError != "" {
		lines = append(lines, "Error: "+displayText(m.gitActionError))
	}
	if len(lines) == 0 {
		return []string{"Completed successfully."}
	}
	return lines
}

func (m dashboard) maximumGitActionOffset(height int) int {
	capacity := max(modalCapacity(height)-2, 1)
	return max(len(m.gitActionResultLines())-capacity, 0)
}

func (m dashboard) scrollGitAction(delta int) (tea.Model, tea.Cmd) {
	maximum := m.maximumGitActionOffset(m.dimensions().bodyHeight)
	m.gitActionOffset = min(max(m.gitActionOffset+delta, 0), maximum)
	return m, nil
}

func (m dashboard) renderGitActionsModal(width, height int) []string {
	target := m.styles.modalStrong.Render(displayText(m.gitActionTarget.Name)) +
		m.styles.empty.Render("  "+displayText(m.gitActionTarget.Path))
	if m.gitActionPending {
		return modalBox(m.styles, width, "Git · "+m.gitAction.label(),
			target,
			"",
			m.styles.modalBody.Render("Running…"),
			"",
		)
	}
	if !m.gitActionComplete {
		lines := []string{target, ""}
		for index, action := range gitActionChoices {
			prefix := "  "
			style := m.styles.modalBody
			if index == m.gitActionIndex {
				prefix = "› "
				style = m.styles.modalStrong
			}
			label := pad(action.label(), 8)
			lines = append(lines, style.Render(prefix+label+action.description()))
		}
		return modalBox(m.styles, width, "Git actions", lines...)
	}

	result := m.gitActionResultLines()
	capacity := max(modalCapacity(height)-2, 1)
	offset := min(max(m.gitActionOffset, 0), max(len(result)-capacity, 0))
	end := min(offset+capacity, len(result))
	title := "Git · " + m.gitAction.label()
	if len(result) > capacity {
		title += fmt.Sprintf(" %d-%d/%d", offset+1, end, len(result))
	}
	lines := []string{target, ""}
	for _, line := range result[offset:end] {
		style := m.styles.modalBody
		if strings.HasPrefix(line, "Error: ") {
			style = m.styles.errorText
		}
		lines = append(lines, style.Render(line))
	}
	return modalBox(m.styles, width, title, lines...)
}
