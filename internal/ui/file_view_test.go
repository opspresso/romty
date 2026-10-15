package ui

import (
	"strings"
	"testing"
)

func TestHighlightWorkspaceFileSyntax(t *testing.T) {
	lines := normalizeWorkspaceFileLines("package main\n\n\tfunc main() {}\n")
	syntax, highlighted := highlightWorkspaceFileSyntax("main.go", lines)
	if !highlighted || len(syntax) != len(lines) || !syntax[0].newHighlighted ||
		gitSyntaxText(syntax[0].new) != "package main" {
		t.Fatalf("workspace file syntax = (%#v, %t)", syntax, highlighted)
	}
	for _, line := range lines {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("normalized workspace file contains a tab: %q", line)
		}
	}
}
