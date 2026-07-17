package ui

import (
	"io"

	"github.com/charmbracelet/lipgloss"
)

type Styles struct {
	Title   lipgloss.Style
	Accent  lipgloss.Style
	Muted   lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
}

func New(out io.Writer) Styles {
	renderer := lipgloss.NewRenderer(out)
	return Styles{
		Title:   renderer.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5B21B6", Dark: "#C4B5FD"}),
		Accent:  renderer.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"}),
		Muted:   renderer.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}),
		Success: renderer.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}),
		Warning: renderer.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}),
		Error:   renderer.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}),
	}
}
