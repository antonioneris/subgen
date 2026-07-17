package ui

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"golang.org/x/term"
)

type InfoRow struct {
	Status, Media, Source, Cues, Tokens, Calls, Cost string
}

func RenderInfoTable(out io.Writer, rows []InfoRow) {
	data := make([][]string, 0, len(rows))
	for _, row := range rows {
		data = append(data, []string{row.Status, row.Media, row.Source, row.Cues, row.Tokens, row.Calls, row.Cost})
	}
	width := 140
	if file, ok := out.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		if terminalWidth, _, err := term.GetSize(int(file.Fd())); err == nil {
			width = max(terminalWidth, 88)
		}
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5B21B6", Dark: "#C4B5FD"}).Padding(0, 1)
	cell := lipgloss.NewStyle().Padding(0, 1)
	muted := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#475569", Dark: "#CBD5E1"}).Padding(0, 1)
	status := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"}).Padding(0, 1)
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#94A3B8", Dark: "#475569"})).
		Headers("Status", "Mídia", "Fonte", "Falas", "Tokens E/S", "Chamadas", "Estimativa").
		Rows(data...).
		Width(width).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return header
			}
			if col == 0 {
				return status
			}
			if col >= 3 {
				return muted
			}
			return cell
		})
	fmt.Fprintln(out, t.String())
}
