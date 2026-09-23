package ui

import (
	"testing"

	"github.com/opspresso/romty/internal/model"
)

func BenchmarkSplitDiffViewport(b *testing.B) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 30
	value.gitDiff = gitDiffView{
		active: true, split: true, target: model.Workspace{Path: "/workspace"},
		files: []gitChangedFile{{Path: "large.txt"}}, request: 1,
	}
	lines := make([]string, 0, 30000)
	for range 10000 {
		lines = append(lines, "@@ -1 +1 @@", "-old content", "+new content")
	}
	updated, _ := value.handleGitFileDiff(gitFileDiffMsg{
		path: "/workspace", filePath: "large.txt", request: 1, lines: lines,
	})
	value = updated.(dashboard)
	value.gitDiff.diffOffset = 10000
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		value.renderGitFileDiff(88, 28)
	}
}
