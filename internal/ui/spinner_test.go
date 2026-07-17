package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
)

func TestTaskViewShowsProgressBarBelowTitle(t *testing.T) {
	bar := progress.New(progress.WithDefaultGradient())
	bar.Width = 30
	model := taskModel{
		spinner: spinner.New(), bar: bar, title: "traduzindo episódio",
		total: 80, current: 20, received: 2048,
	}
	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected title, progress and detail lines, got %q", view)
	}
	if !strings.Contains(lines[0], "traduzindo episódio") || !strings.Contains(lines[1], "25%") || !strings.Contains(lines[2], "20/80 legendas") {
		t.Fatalf("view = %q", view)
	}
}
