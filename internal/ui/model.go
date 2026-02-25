package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	incusclient "github.com/lxc/incus/v6/client"

	"myringa/internal/incus"
)

// Messages
type tickMsg time.Time
type connectDoneMsg struct {
	conn incusclient.InstanceServer
	err  error
}
type fetchDoneMsg struct {
	rows      []incus.InstanceRow
	snapshots map[string]incus.CPUSnapshot
	err       error
}

// Model is the BubbleTea model for the TUI.
type Model struct {
	conn         incusclient.InstanceServer
	rows         []incus.InstanceRow
	cpuSnapshots map[string]incus.CPUSnapshot
	lastUpdated  time.Time
	table        table.Model
	spinner      spinner.Model
	loading      bool
	err          error
	width  int
	height int
}

// NewModel creates the initial model.
func NewModel() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot

	t := table.New(
		table.WithFocused(true),
	)
	ts := table.DefaultStyles()
	ts.Header = HeaderStyle
	ts.Selected = SelectedStyle
	t.SetStyles(ts)

	return Model{
		cpuSnapshots: make(map[string]incus.CPUSnapshot),
		table:        t,
		spinner:      s,
		loading:      true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, connectCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetColumns(m.tableColumns())
		m.table.SetHeight(m.height - headerHeight(m.err != nil))
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case connectDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			return m, tickCmd()
		}
		m.conn = msg.conn
		m.err = nil
		prev := copySnapshots(m.cpuSnapshots)
		return m, fetchCmd(m.conn, prev)

	case fetchDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.conn = nil
			return m, tickCmd()
		}
		m.rows = msg.rows
		m.cpuSnapshots = msg.snapshots
		m.lastUpdated = time.Now()
		m.loading = false
		m.err = nil
		m.table.SetRows(m.toTableRows())
		return m, tickCmd()

	case tickMsg:
		if m.conn == nil {
			return m, connectCmd()
		}
		prev := copySnapshots(m.cpuSnapshots)
		return m, fetchCmd(m.conn, prev)

	default:
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
}

func (m Model) View() string {
	var s string

	// Title bar
	title := TitleStyle.Render("myringa")
	var meta string
	if len(m.rows) > 0 {
		meta += DimStyle.Render(fmt.Sprintf("  %d instances", len(m.rows)))
	}
	if !m.lastUpdated.IsZero() {
		meta += DimStyle.Render(fmt.Sprintf("  %s", m.lastUpdated.Format("15:04:05")))
	}
	s += title + meta + "\n"

	// Error banner
	if m.err != nil {
		s += ErrorStyle.Render(fmt.Sprintf("  error: %v", m.err)) + "\n"
	}

	// Main content
	if m.loading && len(m.rows) == 0 {
		s += m.spinner.View() + " Connecting to Incus...\n"
	} else {
		s += m.table.View() + "\n"
	}

	// Footer
	s += HelpStyle.Render("  j/k scroll  q quit")

	return s
}

// Commands

func connectCmd() tea.Cmd {
	return func() tea.Msg {
		conn, err := incus.Connect()
		return connectDoneMsg{conn: conn, err: err}
	}
}

func fetchCmd(conn incusclient.InstanceServer, prev map[string]incus.CPUSnapshot) tea.Cmd {
	return func() tea.Msg {
		rows, snaps, err := incus.FetchInstances(conn, prev)
		return fetchDoneMsg{rows: rows, snapshots: snaps, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Helpers

// headerHeight returns the number of lines used by title + error + footer.
func headerHeight(hasErr bool) int {
	h := 3 // title + table header (built-in) + footer
	if hasErr {
		h++
	}
	return h
}

func copySnapshots(m map[string]incus.CPUSnapshot) map[string]incus.CPUSnapshot {
	c := make(map[string]incus.CPUSnapshot, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (m Model) tableColumns() []table.Column {
	const (
		nameW = 32
		cpuW  = 8
		memW  = 12
		diskW = 8
	)
	ipWidth := m.width - (nameW + cpuW + memW + diskW)
	if ipWidth < 16 {
		ipWidth = 16
	}

	return []table.Column{
		{Title: "NAME", Width: nameW},
		{Title: "CPU", Width: cpuW},
		{Title: "MEM", Width: memW},
		{Title: "DISK", Width: diskW},
		{Title: "IPv4", Width: ipWidth},
	}
}

func (m Model) toTableRows() []table.Row {
	trs := make([]table.Row, len(m.rows))
	for i, r := range m.rows {
		name := fmt.Sprintf("%s %s  %s", statusRune(r.Status), r.Name, r.Type)
		row := []string{
			name,
			r.CPU,
			r.Memory,
			r.Disk,
			r.IPv4,
		}
		trs[i] = row
	}
	return trs
}

// statusRune returns an uncolored dot character for status.
func statusRune(status string) string {
	switch status {
	case "Running":
		return "●"
	case "Stopped":
		return "○"
	case "Frozen":
		return "◎"
	default:
		return "·"
	}
}
