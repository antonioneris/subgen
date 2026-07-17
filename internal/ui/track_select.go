package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/antonioneris/subgen/internal/media"
)

type trackSelectModel struct {
	path     string
	tracks   []media.SubtitleTrack
	cursor   int
	selected int
	canceled bool
}

func (m trackSelectModel) Init() tea.Cmd { return nil }

func (m trackSelectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.tracks)-1 {
			m.cursor++
		}
	case "enter":
		m.selected = m.tracks[m.cursor].Index
		return m, tea.Quit
	case "esc", "q", "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	}
	return m, nil
}

func (m trackSelectModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4B5FD"))
	selected := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7DD3FC"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8"))
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", title.Render("Escolha uma única faixa de legenda"))
	fmt.Fprintf(&b, "%s\n\n", muted.Render(filepath.Base(m.path)))
	for i, track := range m.tracks {
		cursor := "  "
		style := muted
		if i == m.cursor {
			cursor, style = "› ", selected
		}
		language := track.Language
		if language == "" {
			language = "idioma desconhecido"
		}
		details := fmt.Sprintf("faixa %d · %s · %s", track.Index, language, track.Codec)
		if track.Title != "" {
			details += " · " + track.Title
		}
		fmt.Fprintf(&b, "%s%s\n", cursor, style.Render(details))
	}
	fmt.Fprintf(&b, "\n%s", muted.Render("↑/↓ selecionar  •  Enter confirmar  •  Esc cancelar"))
	return b.String()
}

func SelectTrack(ctx context.Context, input io.Reader, output io.Writer, path string, tracks []media.SubtitleTrack) (int, error) {
	inFile, inOK := input.(*os.File)
	outFile, outOK := output.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(inFile.Fd())) || !term.IsTerminal(int(outFile.Fd())) {
		return 0, fmt.Errorf("%s possui várias faixas; execute em um terminal interativo ou use --track", path)
	}
	program := tea.NewProgram(
		trackSelectModel{path: path, tracks: tracks, selected: -1},
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output),
	)
	final, err := program.Run()
	if err != nil {
		return 0, err
	}
	model, ok := final.(trackSelectModel)
	if !ok || model.canceled || model.selected < 0 {
		return 0, fmt.Errorf("seleção de faixa cancelada")
	}
	return model.selected, nil
}
