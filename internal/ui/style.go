package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/yindia/p9s/internal/model"
)

// Palette: a "signal board" look. Amber brand, teal structure, and
// enforcement modes carry meaning (red blocks, amber warns, blue observes).
// Every color is adaptive so light terminals stay legible.
var (
	colorBrand   = lipgloss.AdaptiveColor{Light: "#A15C00", Dark: "#F2A541"}
	colorFrame   = lipgloss.AdaptiveColor{Light: "#1F7A73", Dark: "#3FB8AF"}
	colorText    = lipgloss.AdaptiveColor{Light: "#1B1F27", Dark: "#E6E9EF"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#5C6370", Dark: "#7C8595"}
	colorEnforce = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#F25F5C"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#8A5A00", Dark: "#F2C14E"}
	colorAudit   = lipgloss.AdaptiveColor{Light: "#2457A6", Dark: "#7AA7E0"}
	colorOK      = lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#7BC67E"}
	colorSelBg   = lipgloss.AdaptiveColor{Light: "#D6E4F0", Dark: "#24354D"}
	colorSelFg   = lipgloss.AdaptiveColor{Light: "#0B1220", Dark: "#FFFFFF"}
)

type styles struct {
	brand, ctx, engine, muted, ro, rw lipgloss.Style
	title, titleCount, rule           lipgloss.Style
	colHeader, cell, selected         lipgloss.Style
	notice, errorText, flashErr       lipgloss.Style
	flashInfo, prompt, keyHint        lipgloss.Style
	sectionTitle                      lipgloss.Style
	modes                             map[model.EnforcementMode]lipgloss.Style
	count, zero, yes, no              lipgloss.Style
	severity                          map[model.Severity]lipgloss.Style
}

func newStyles() styles {
	base := lipgloss.NewStyle().Foreground(colorText)
	return styles{
		brand:        lipgloss.NewStyle().Foreground(colorBrand).Bold(true),
		ctx:          lipgloss.NewStyle().Foreground(colorText).Bold(true),
		engine:       lipgloss.NewStyle().Foreground(colorFrame),
		muted:        lipgloss.NewStyle().Foreground(colorMuted),
		ro:           lipgloss.NewStyle().Foreground(colorOK).Bold(true),
		rw:           lipgloss.NewStyle().Foreground(colorEnforce).Bold(true),
		title:        lipgloss.NewStyle().Foreground(colorBrand).Bold(true),
		titleCount:   lipgloss.NewStyle().Foreground(colorText),
		rule:         lipgloss.NewStyle().Foreground(colorFrame),
		colHeader:    lipgloss.NewStyle().Foreground(colorFrame).Bold(true),
		cell:         base,
		selected:     lipgloss.NewStyle().Foreground(colorSelFg).Background(colorSelBg).Bold(true),
		notice:       lipgloss.NewStyle().Foreground(colorWarn),
		errorText:    lipgloss.NewStyle().Foreground(colorEnforce),
		flashErr:     lipgloss.NewStyle().Foreground(colorEnforce).Bold(true),
		flashInfo:    lipgloss.NewStyle().Foreground(colorOK),
		prompt:       lipgloss.NewStyle().Foreground(colorBrand).Bold(true),
		keyHint:      lipgloss.NewStyle().Foreground(colorFrame).Bold(true),
		sectionTitle: lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Underline(true),
		modes: map[model.EnforcementMode]lipgloss.Style{
			model.ModeEnforce: lipgloss.NewStyle().Foreground(colorEnforce).Bold(true),
			model.ModeWarn:    lipgloss.NewStyle().Foreground(colorWarn),
			model.ModeAudit:   lipgloss.NewStyle().Foreground(colorAudit),
			model.ModeUnknown: lipgloss.NewStyle().Foreground(colorMuted),
		},
		count: lipgloss.NewStyle().Foreground(colorEnforce).Bold(true),
		zero:  lipgloss.NewStyle().Foreground(colorMuted),
		yes:   lipgloss.NewStyle().Foreground(colorOK),
		no:    lipgloss.NewStyle().Foreground(colorEnforce),
		severity: map[model.Severity]lipgloss.Style{
			model.SeverityError:   lipgloss.NewStyle().Foreground(colorEnforce).Bold(true),
			model.SeverityWarning: lipgloss.NewStyle().Foreground(colorWarn),
			model.SeverityInfo:    lipgloss.NewStyle().Foreground(colorAudit),
		},
	}
}
