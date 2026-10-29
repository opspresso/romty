package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

// branchDashboard opens the workspace at a real repository with branches main
// and feature, and the cursor on it.
func branchDashboard(t *testing.T, tabs []model.Tab) (dashboard, string) {
	t.Helper()
	repository := t.TempDir()
	testutil.RunGit(t, "init", "--initial-branch=main", repository)
	testutil.WriteFile(t, repository, "a.txt", "a\n")
	testutil.RunGit(t, "-C", repository, "add", ".")
	testutil.RunGit(t, "-C", repository, "commit", "-m", "initial")
	testutil.RunGit(t, "-C", repository, "branch", "feature")
	workspace := model.Workspace{ID: "workspace-1", RootID: projectsRoot.ID, Name: "alpha", Path: repository}
	value := newDashboard(&fakeBackend{}, projectsSnapshot([]model.WorkspaceView{{Workspace: workspace, Tabs: tabs}}))
	value.width, value.height = 110, 24
	value.gitStates = map[string]gitrepo.State{repository: {Branch: "main"}}
	value.focus = leftPane
	value.setNavigation(1)
	return value, repository
}

// openPicker presses g then b, and loads the branches the picker asked for.
func openPicker(t *testing.T, value *dashboard) {
	t.Helper()
	send(value, key('g', "g"))
	command := send(value, key('b', "b"))
	if value.modal != branchModal || command == nil {
		t.Fatalf("g b = (modal %v, command %v), want the branch picker", value.modal, command)
	}
	deliver(value, command)
}

// deliver runs command and hands its Git answers back to the dashboard, the
// way the program would.
func deliver(value *dashboard, command tea.Cmd) {
	for _, message := range commandMessages(command) {
		switch message.(type) {
		case branchListMsg, gitActionMsg:
			send(value, message)
		}
	}
}

func currentBranch(t *testing.T, repository string) string {
	t.Helper()
	branches, err := gitrepo.Branches(context.Background(), repository)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	for _, branch := range branches {
		if branch.Current {
			return branch.Name
		}
	}
	return ""
}

func TestBranchPickerSwitchesAndShowsTheResult(t *testing.T) {
	value, repository := branchDashboard(t, nil)
	openPicker(t, &value)
	screen := ansi.Strip(value.render())
	if !strings.Contains(screen, "Switch branch") || !strings.Contains(screen, "main  current") || !strings.Contains(screen, "feature") {
		t.Fatalf("picker lacks the branches:\n%s", screen)
	}

	// Typing filters; Enter switches to the row under the cursor.
	typeText(&value, "feat")
	command := send(&value, key(tea.KeyEnter, ""))
	if value.modal != gitActionsModal || !value.gitRun.pending || command == nil {
		t.Fatalf("Enter = (modal %v, pending %v), want the switch running", value.modal, value.gitRun.pending)
	}
	deliver(&value, command)
	if current := currentBranch(t, repository); current != "feature" {
		t.Fatalf("current branch = %q, want feature", current)
	}
	if !value.gitRun.complete || value.gitRun.action != gitSwitchAction || value.gitRun.err != "" {
		t.Fatalf("result = %+v, want a completed switch", value.gitRun)
	}
}

// A name that matches no branch is offered as a new branch.
func TestBranchPickerCreatesATypedBranch(t *testing.T) {
	value, repository := branchDashboard(t, nil)
	openPicker(t, &value)
	typeText(&value, "topic/new")
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "+ Create branch topic/new") {
		t.Fatalf("picker does not offer to create the branch:\n%s", screen)
	}
	command := send(&value, key(tea.KeyEnter, ""))
	deliver(&value, command)
	if current := currentBranch(t, repository); current != "topic/new" {
		t.Fatalf("current branch = %q, want topic/new", current)
	}
}

// Programs running in the workspace share its directory, so the switch asks
// first and Esc steps back without switching.
func TestBranchPickerAsksWhileAnAgentRuns(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true, Agent: model.AgentClaude}}
	value, repository := branchDashboard(t, tabs)
	openPicker(t, &value)
	typeText(&value, "feature")
	if command := send(&value, key(tea.KeyEnter, "")); command != nil {
		t.Fatal("the switch ran without asking while an agent runs")
	}
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "Claude Code running here will see the files change") {
		t.Fatalf("confirmation does not name the agent:\n%s", screen)
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != branchModal || value.branches.confirming != nil || currentBranch(t, repository) != "main" {
		t.Fatal("Esc did not step back without switching")
	}
	send(&value, key(tea.KeyEnter, ""))
	command := send(&value, key(tea.KeyEnter, ""))
	deliver(&value, command)
	if current := currentBranch(t, repository); current != "feature" {
		t.Fatalf("current branch = %q after confirming, want feature", current)
	}
}

// Esc clears the filter before it closes the picker, and Enter on the current
// branch does nothing.
func TestBranchPickerEscClearsThenCloses(t *testing.T) {
	value, _ := branchDashboard(t, nil)
	openPicker(t, &value)
	// The cursor starts on the branch after the current one.
	if value.branches.index != 1 {
		t.Fatalf("cursor starts at %d, want the first branch that is not current", value.branches.index)
	}
	send(&value, key(tea.KeyUp, ""))
	if command := send(&value, key(tea.KeyEnter, "")); command != nil || value.modal != branchModal {
		t.Fatal("Enter on the current branch did something")
	}
	typeText(&value, "x")
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != branchModal || value.branches.query != "" {
		t.Fatal("first Esc did not clear the filter")
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal {
		t.Fatal("second Esc did not close the picker")
	}
}

// Typing ahead of a slow branch read must not leave Enter on the create row:
// before the list arrives Enter does nothing, and after it the cursor starts
// on the matching branch.
func TestBranchPickerTypingAheadOfTheListNeverCreates(t *testing.T) {
	value, repository := branchDashboard(t, nil)
	send(&value, key('g', "g"))
	command := send(&value, key('b', "b"))
	typeText(&value, "feat")
	if again := send(&value, key(tea.KeyEnter, "")); again != nil {
		t.Fatal("Enter ran before the list was on screen")
	}
	deliver(&value, command)
	rows := value.branchRows()
	if rows[value.branches.index].create || rows[value.branches.index].branch.Name != "feature" {
		t.Fatalf("cursor = %+v, want feature", rows[value.branches.index])
	}
	deliver(&value, send(&value, key(tea.KeyEnter, "")))
	if current := currentBranch(t, repository); current != "feature" {
		t.Fatalf("current branch = %q, want feature", current)
	}
}

// The answer to a read made for a picker since closed and reopened is
// dropped.
func TestBranchPickerDropsAStaleRead(t *testing.T) {
	value, _ := branchDashboard(t, nil)
	send(&value, key('g', "g"))
	stale := send(&value, key('b', "b"))
	send(&value, key(tea.KeyEscape, ""))
	send(&value, key('g', "g"))
	send(&value, key('b', "b"))
	deliver(&value, stale)
	if !value.branches.loading {
		t.Fatal("a stale read was applied to the reopened picker")
	}
}
