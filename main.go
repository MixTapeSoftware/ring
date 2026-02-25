package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"myringa/internal/ui"
)

func main() {
	p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
