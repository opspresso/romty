package ui

import "charm.land/lipgloss/v2"

type uiStyles struct {
	paneTitle           lipgloss.Style
	paneTitleActive     lipgloss.Style
	navigationItem      lipgloss.Style
	navigationRoot      lipgloss.Style
	navigationCurrent   lipgloss.Style
	navigationSelected  lipgloss.Style
	agentClaude         lipgloss.Style
	agentCodex          lipgloss.Style
	gitBranch           lipgloss.Style
	gitStatus           lipgloss.Style
	gitConflict         lipgloss.Style
	diffAdded           lipgloss.Style
	diffRemoved         lipgloss.Style
	diffHunk            lipgloss.Style
	diffAddedLine       lipgloss.Style
	diffRemovedLine     lipgloss.Style
	syntaxKeyword       lipgloss.Style
	syntaxName          lipgloss.Style
	syntaxString        lipgloss.Style
	syntaxNumber        lipgloss.Style
	syntaxComment       lipgloss.Style
	syntaxOperator      lipgloss.Style
	tab                 lipgloss.Style
	tabSelected         lipgloss.Style
	tabRail             lipgloss.Style
	tabRailSelected     lipgloss.Style
	divider             lipgloss.Style
	dividerActive       lipgloss.Style
	shortcutKey         lipgloss.Style
	shortcutDescription lipgloss.Style
	promptLabel         lipgloss.Style
	promptText          lipgloss.Style
	errorLabel          lipgloss.Style
	errorText           lipgloss.Style
	noticeLabel         lipgloss.Style
	noticeText          lipgloss.Style
	modalBorder         lipgloss.Style
	modalTitle          lipgloss.Style
	modalBody           lipgloss.Style
	modalStrong         lipgloss.Style
	empty               lipgloss.Style
}

func newUIStyles(hasDarkBackground bool) *uiStyles {
	lightDark := lipgloss.LightDark(hasDarkBackground)
	text := lightDark(lipgloss.Color("#0F172A"), lipgloss.Color("#E2E8F0"))
	muted := lightDark(lipgloss.Color("#64748B"), lipgloss.Color("#8492A6"))
	border := lightDark(lipgloss.Color("#CBD5E1"), lipgloss.Color("#334155"))
	surface := lightDark(lipgloss.Color("#E2E8F0"), lipgloss.Color("#1E293B"))
	accent := lightDark(lipgloss.Color("#0F766E"), lipgloss.Color("#5EEAD4"))
	accentSurface := lightDark(lipgloss.Color("#CCFBF1"), lipgloss.Color("#134E4A"))
	accentText := lightDark(lipgloss.Color("#115E59"), lipgloss.Color("#F0FDFA"))
	errorColor := lightDark(lipgloss.Color("#BE123C"), lipgloss.Color("#FB7185"))
	errorText := lightDark(lipgloss.Color("#FFFFFF"), lipgloss.Color("#0F172A"))
	addedColor := lightDark(lipgloss.Color("#15803D"), lipgloss.Color("#86EFAC"))
	addedBackground := lightDark(lipgloss.Color("#DAFBE1"), lipgloss.Color("#12261E"))
	removedBackground := lightDark(lipgloss.Color("#FFEBE9"), lipgloss.Color("#301B1F"))

	return &uiStyles{
		paneTitle:           lipgloss.NewStyle().Foreground(muted).Bold(true),
		paneTitleActive:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		navigationItem:      lipgloss.NewStyle().Foreground(text),
		navigationRoot:      lipgloss.NewStyle().Foreground(text).Bold(true),
		navigationCurrent:   lipgloss.NewStyle().Foreground(accent).Bold(true),
		navigationSelected:  lipgloss.NewStyle().Foreground(accentText).Background(accentSurface).Bold(true),
		agentClaude:         lipgloss.NewStyle().Foreground(lipgloss.Color("#D97757")),
		agentCodex:          lipgloss.NewStyle().Foreground(lipgloss.Color("#3B82F6")),
		gitBranch:           lipgloss.NewStyle().Foreground(muted),
		gitStatus:           lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")),
		gitConflict:         lipgloss.NewStyle().Foreground(errorColor),
		diffAdded:           lipgloss.NewStyle().Foreground(addedColor),
		diffRemoved:         lipgloss.NewStyle().Foreground(errorColor),
		diffHunk:            lipgloss.NewStyle().Foreground(accent).Bold(true),
		diffAddedLine:       lipgloss.NewStyle().Foreground(addedColor).Background(addedBackground),
		diffRemovedLine:     lipgloss.NewStyle().Foreground(errorColor).Background(removedBackground),
		syntaxKeyword:       lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#7C3AED"), lipgloss.Color("#C084FC"))).Bold(true),
		syntaxName:          lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#0369A1"), lipgloss.Color("#7DD3FC"))),
		syntaxString:        lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#047857"), lipgloss.Color("#6EE7B7"))),
		syntaxNumber:        lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#B45309"), lipgloss.Color("#FBBF24"))),
		syntaxComment:       lipgloss.NewStyle().Foreground(muted).Italic(true),
		syntaxOperator:      lipgloss.NewStyle().Foreground(accent),
		tab:                 lipgloss.NewStyle().Foreground(muted).Background(surface),
		tabSelected:         lipgloss.NewStyle().Foreground(accentText).Background(accentSurface).Bold(true),
		tabRail:             lipgloss.NewStyle().Foreground(border),
		tabRailSelected:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		divider:             lipgloss.NewStyle().Foreground(border),
		dividerActive:       lipgloss.NewStyle().Foreground(accent).Bold(true),
		shortcutKey:         lipgloss.NewStyle().Foreground(accent).Background(surface).Bold(true),
		shortcutDescription: lipgloss.NewStyle().Foreground(muted),
		promptLabel:         lipgloss.NewStyle().Foreground(accentText).Background(accentSurface).Bold(true),
		promptText:          lipgloss.NewStyle().Foreground(text),
		errorLabel:          lipgloss.NewStyle().Foreground(errorText).Background(errorColor).Bold(true),
		errorText:           lipgloss.NewStyle().Foreground(errorColor),
		noticeLabel:         lipgloss.NewStyle().Foreground(text).Background(surface).Bold(true),
		noticeText:          lipgloss.NewStyle().Foreground(muted),
		modalBorder:         lipgloss.NewStyle().Foreground(accent),
		modalTitle:          lipgloss.NewStyle().Foreground(accent).Bold(true),
		modalBody:           lipgloss.NewStyle().Foreground(text),
		modalStrong:         lipgloss.NewStyle().Foreground(text).Bold(true),
		empty:               lipgloss.NewStyle().Foreground(muted),
	}
}
