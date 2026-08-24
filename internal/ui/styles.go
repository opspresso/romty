package ui

import "charm.land/lipgloss/v2"

type uiStyles struct {
	paneTitle           lipgloss.Style
	paneTitleActive     lipgloss.Style
	navigationItem      lipgloss.Style
	navigationRoot      lipgloss.Style
	navigationCurrent   lipgloss.Style
	navigationSelected  lipgloss.Style
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

	return &uiStyles{
		paneTitle:           lipgloss.NewStyle().Foreground(muted).Bold(true),
		paneTitleActive:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		navigationItem:      lipgloss.NewStyle().Foreground(text),
		navigationRoot:      lipgloss.NewStyle().Foreground(text).Bold(true),
		navigationCurrent:   lipgloss.NewStyle().Foreground(accent).Bold(true),
		navigationSelected:  lipgloss.NewStyle().Foreground(accentText).Background(accentSurface).Bold(true),
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
		modalBorder:         lipgloss.NewStyle().Foreground(accent),
		modalTitle:          lipgloss.NewStyle().Foreground(accent).Bold(true),
		modalBody:           lipgloss.NewStyle().Foreground(text),
		modalStrong:         lipgloss.NewStyle().Foreground(text).Bold(true),
		empty:               lipgloss.NewStyle().Foreground(muted),
	}
}
