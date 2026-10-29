package ui

import (
	"fmt"
	"testing"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

func BenchmarkSplitDiffViewport(b *testing.B) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 30
	value.fileView = fileViewState{
		active: true, split: true, target: model.Workspace{Path: "/workspace"},
		files: []gitrepo.ChangedFile{{Path: "large.txt"}}, request: 1,
	}
	lines := make([]string, 0, 30000)
	for range 10000 {
		lines = append(lines, "@@ -1 +1 @@", "-old content", "+new content")
	}
	updated, _ := value.handleFileContent(fileContentMsg{
		path: "/workspace", filePath: "large.txt", request: 1, lines: lines,
	})
	value = updated.(dashboard)
	value.fileView.diffOffset = 10000
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		value.renderFileViewContent(88, 28)
	}
}

// BenchmarkDashboardRender draws a large tree, 5 roots of 40 workspaces with
// two agents each, half of them waiting, the way every frame draws it.
func BenchmarkDashboardRender(b *testing.B) {
	var roots []model.RootView
	for rootIndex := range 5 {
		root := model.Root{ID: fmt.Sprintf("r%d", rootIndex), Name: fmt.Sprintf("root%d", rootIndex), Path: fmt.Sprintf("/r%d", rootIndex)}
		view := model.RootView{Root: root}
		for index := range 40 {
			id := fmt.Sprintf("%s-w%d", root.ID, index)
			view.Directories = append(view.Directories, model.WorkspaceView{
				Workspace: model.Workspace{ID: id, RootID: root.ID, Name: id, Path: root.Path + "/" + id},
				Tabs: []model.Tab{
					{ID: id + "a", WorkspaceID: id, Name: "1", Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking},
					{ID: id + "b", WorkspaceID: id, Name: "2", Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingInput},
				},
			})
		}
		roots = append(roots, view)
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: roots})
	value.width, value.height = 160, 50
	value.focus = leftPane
	value.setNavigation(3)
	b.ReportAllocs()
	for b.Loop() {
		value.render()
		_ = value.windowTitle()
	}
}
