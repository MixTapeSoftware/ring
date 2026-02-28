package ui

import (
	"testing"

	"ring/internal/incus"
)

func TestStatusRune(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"Running", "●"},
		{"Stopped", "○"},
		{"Frozen", "◎"},
		{"Unknown", "·"},
		{"", "·"},
	}
	for _, tt := range tests {
		got := statusRune(tt.status)
		if got != tt.want {
			t.Errorf("statusRune(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestToTableRows(t *testing.T) {
	rows := []incus.InstanceRow{
		{Name: "web-1", Status: "Running", Type: "CT", CPU: "12.3%", Memory: "256M/1G", Disk: "2G", IPv4: "10.0.0.1"},
		{Name: "db-1", Status: "Stopped", Type: "VM", CPU: "—", Memory: "—", Disk: "—", IPv4: "—"},
	}
	result := toTableRows(rows)

	if len(result) != 2 {
		t.Fatalf("got %d rows, want 2", len(result))
	}

	// First row: running container
	if result[0][0] != "● web-1  CT" {
		t.Errorf("row[0] name = %q, want %q", result[0][0], "● web-1  CT")
	}
	if result[0][1] != "12.3%" {
		t.Errorf("row[0] CPU = %q, want %q", result[0][1], "12.3%")
	}

	// Second row: stopped VM
	if result[1][0] != "○ db-1  VM" {
		t.Errorf("row[1] name = %q, want %q", result[1][0], "○ db-1  VM")
	}
	if result[1][4] != "—" {
		t.Errorf("row[1] IPv4 = %q, want %q", result[1][4], "—")
	}
}

func TestBuildExecCmd(t *testing.T) {
	cmd := BuildExecCmd("my-instance", "alice")
	args := cmd.Args
	want := []string{"incus", "exec", "my-instance", "--", "su", "-", "alice", "-s", "/bin/zsh"}
	if len(args) != len(want) {
		t.Fatalf("got args %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestSelectedName_Empty(t *testing.T) {
	m := NewModel("testuser")
	if name := m.selectedName(); name != "" {
		t.Errorf("selectedName() on empty model = %q, want empty", name)
	}
}

func TestRestoreCursor(t *testing.T) {
	m := NewModel("testuser")
	m.width = 120
	m.table.SetColumns(m.tableColumns())
	m.table.SetHeight(10)
	m.rows = []incus.InstanceRow{
		{Name: "alpha"},
		{Name: "bravo"},
		{Name: "charlie"},
	}
	m.table.SetRows(toTableRows(m.rows))

	// Restore to "bravo" → should be at index 1
	m.restoreCursor("bravo")
	if m.table.Cursor() != 1 {
		t.Errorf("cursor = %d, want 1", m.table.Cursor())
	}

	// Restore to non-existent → cursor stays
	m.restoreCursor("zulu")
	if m.table.Cursor() != 1 {
		t.Errorf("cursor after missing name = %d, want 1", m.table.Cursor())
	}

	// Restore with empty string → no-op
	m.restoreCursor("")
	if m.table.Cursor() != 1 {
		t.Errorf("cursor after empty = %d, want 1", m.table.Cursor())
	}
}

func TestFindRow(t *testing.T) {
	m := NewModel("testuser")
	m.rows = []incus.InstanceRow{
		{Name: "alpha", Status: "Running"},
		{Name: "bravo", Status: "Stopped"},
	}

	row, ok := m.findRow("bravo")
	if !ok {
		t.Fatal("findRow(bravo) returned false")
	}
	if row.Status != "Stopped" {
		t.Errorf("findRow(bravo).Status = %q, want Stopped", row.Status)
	}

	_, ok = m.findRow("charlie")
	if ok {
		t.Error("findRow(charlie) should return false")
	}
}
