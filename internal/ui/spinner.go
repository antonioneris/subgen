package ui

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type taskResult struct{ err error }
type progressMsg struct{ current, received int }
type progressClosedMsg struct{}

type taskModel struct {
	spinner  spinner.Model
	bar      progress.Model
	title    string
	total    int
	action   func(func(int, int)) error
	progress chan progressMsg
	current  int
	received int
	err      error
	done     bool
}

func (m taskModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, waitProgress(m.progress), func() tea.Msg {
		err := m.action(func(current, received int) {
			select {
			case m.progress <- progressMsg{current: current, received: received}:
			default:
			}
		})
		close(m.progress)
		return taskResult{err: err}
	})
}

func waitProgress(progress <-chan progressMsg) tea.Cmd {
	return func() tea.Msg {
		received, ok := <-progress
		if !ok {
			return progressClosedMsg{}
		}
		return received
	}
}

func (m taskModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case taskResult:
		m.err, m.done = msg.err, true
		return m, tea.Quit
	case progressMsg:
		m.current, m.received = msg.current, msg.received
		return m, waitProgress(m.progress)
	case progressClosedMsg:
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.bar.Width = min(max(msg.Width-4, 20), 72)
		return m, nil
	}
	return m, nil
}

func (m taskModel) View() string {
	if m.done {
		return ""
	}
	if m.total > 0 {
		percent := min(float64(m.current)/float64(m.total), 1)
		detail := fmt.Sprintf("%d/%d legendas", m.current, m.total)
		if m.received > 0 {
			detail += fmt.Sprintf(" · %.1f kB recebidos", float64(m.received)/1000)
		} else {
			detail += " · aguardando o primeiro trecho"
		}
		return fmt.Sprintf("  %s %s\n  %s\n  %s", m.spinner.View(), m.title, m.bar.ViewAs(percent), detail)
	}
	return fmt.Sprintf("  %s %s", m.spinner.View(), m.title)
}

// RunTask animates only on a real terminal. Redirected output remains clean
// and deterministic for logs, scripts and tests.
func RunTask(ctx context.Context, out io.Writer, title string, total int, action func(report func(current, received int)) error) error {
	file, interactive := out.(*os.File)
	if !interactive || !term.IsTerminal(int(file.Fd())) {
		fmt.Fprintf(out, "  ◌ %s\n", title)
		return action(func(int, int) {})
	}
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC"))
	bar := progress.New(progress.WithDefaultGradient())
	bar.Width = 48
	bar.ShowPercentage = true
	program := tea.NewProgram(
		taskModel{spinner: spin, bar: bar, title: title, total: total, action: action, progress: make(chan progressMsg, 1)},
		tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(out),
	)
	final, err := program.Run()
	if err != nil {
		return err
	}
	if model, ok := final.(taskModel); ok {
		return model.err
	}
	return nil
}
