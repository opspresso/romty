package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func TestDashboardHighlightsCodeDiffsInBothLayouts(t *testing.T) {
	lines := normalizeGitDiffLines(strings.Join([]string{
		"diff --git a/main.go b/main.go",
		"--- a/main.go",
		"+++ b/main.go",
		"@@ -1,4 +1,4 @@",
		" package main",
		`-var message = "old"`,
		`+var message = "new"`,
		"-/* old",
		"-continued */",
		"+/* new",
		"+continued */",
	}, "\n"))
	syntax, highlighted := highlightGitDiffSyntax("main.go", lines)
	if !highlighted {
		t.Fatal("Go diff was not recognised as source code")
	}

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{
		active:            true,
		files:             []gitChangedFile{{Path: "main.go", WorkTreeStatus: 'M'}},
		diffLines:         lines,
		diffSyntax:        syntax,
		syntaxHighlighted: true,
	}
	inline := strings.Join(value.renderGitFileDiff(100, 20), "\n")
	for _, line := range strings.Split(inline, "\n") {
		plain := ansi.Strip(line)
		if (strings.HasPrefix(plain, `-var message`) || strings.HasPrefix(plain, `+var message`)) && lipgloss.Width(line) != 100 {
			t.Fatalf("changed inline row width = %d, want full 100-column background: %q", lipgloss.Width(line), plain)
		}
	}
	for _, split := range []bool{false, true} {
		value.gitDiff.split = split
		rendered := strings.Join(value.renderGitFileDiff(100, 20), "\n")
		for _, fragment := range []string{
			value.styles.syntaxKeyword.Render("package"),
			value.styles.syntaxString.Background(value.styles.diffAddedLine.GetBackground()).Render(`"new"`),
			value.styles.syntaxComment.Background(value.styles.diffAddedLine.GetBackground()).Render("continued */"),
		} {
			if !strings.Contains(rendered, fragment) {
				t.Fatalf("split=%t highlighted diff does not contain %q:\n%s", split, fragment, ansi.Strip(rendered))
			}
		}
		plain := ansi.Strip(rendered)
		for _, fragment := range []string{`-var message = "old"`, `+var message = "new"`, "-continued */", "+continued */"} {
			if !strings.Contains(plain, fragment) {
				t.Fatalf("split=%t highlighted diff lost %q:\n%s", split, fragment, plain)
			}
		}
	}
}

func TestDashboardUsesChangedLineBackgroundForPlainTextDiffs(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	for _, test := range []struct {
		line  string
		style lipgloss.Style
	}{
		{line: "+new", style: value.styles.diffAddedLine},
		{line: "-old", style: value.styles.diffRemovedLine},
	} {
		want := test.style.Render(pad(test.line, 20))
		if got := value.renderGitDiffLine(test.line, 20); got != want {
			t.Fatalf("changed line %q = %q, want full background %q", test.line, got, want)
		}
	}
}

func TestGitDiffSyntaxFallsBackForUnknownFiles(t *testing.T) {
	lines := []string{"@@ -1 +1 @@", "-old", "+new"}
	syntax, highlighted := highlightGitDiffSyntax("notes.romty-unknown", lines)
	if highlighted || syntax != nil {
		t.Fatalf("unknown file syntax = (%#v, %t), want plain text fallback", syntax, highlighted)
	}
}

func TestGitDiffSyntaxFallsBackForLargeDiffs(t *testing.T) {
	for _, lines := range [][]string{
		make([]string, maximumHighlightedDiffLines+1),
		{"@@ -1 +1 @@", "+" + strings.Repeat("x", maximumHighlightedDiffBytes)},
	} {
		syntax, highlighted := highlightGitDiffSyntax("main.go", lines)
		if highlighted || syntax != nil {
			t.Fatalf("large diff syntax = (%#v, %t), want plain text fallback", syntax, highlighted)
		}
	}
}

type panickingGitLexer struct {
	chroma.Lexer
}

func (panickingGitLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	return func() chroma.Token { panic("malformed source") }, nil
}

func TestGitDiffSyntaxFallsBackWhenALexerPanics(t *testing.T) {
	lines := []string{"@@ -1 +1 @@", "-old", "+new"}
	syntax, highlighted := highlightGitDiffSyntaxWithLexer(panickingGitLexer{}, lines)
	if highlighted || syntax != nil {
		t.Fatalf("panicking lexer syntax = (%#v, %t), want plain text fallback", syntax, highlighted)
	}
}
