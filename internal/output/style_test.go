package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoColourWhenDisabled(t *testing.T) {
	restore := SetColourForTest(false)
	defer restore()

	if got := Green("Active"); got != "Active" {
		t.Errorf("expected no ANSI when colour disabled, got %q", got)
	}
	if got := StatusColour("Active"); strings.Contains(got, "\x1b") {
		t.Errorf("StatusColour leaked ANSI when disabled: %q", got)
	}
}

func TestColourWhenEnabled(t *testing.T) {
	restore := SetColourForTest(true)
	defer restore()

	if got := Green("x"); !strings.HasPrefix(got, "\x1b[32m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("expected wrapped ANSI, got %q", got)
	}
}

func TestDashOnEmpty(t *testing.T) {
	restore := SetColourForTest(false)
	defer restore()
	if got := Dash("  "); got != "—" {
		t.Errorf("Dash(empty) = %q, want em dash", got)
	}
	if got := Dash("v"); got != "v" {
		t.Errorf("Dash(value) = %q, want v", got)
	}
}

// Alignment must be based on VISIBLE width, ignoring ANSI codes.
func TestTableAlignmentIgnoresAnsi(t *testing.T) {
	restore := SetColourForTest(true)
	defer restore()

	tbl := NewTable("A", "B")
	tbl.Row(Green("xx"), "1") // coloured cell, visible width 2
	tbl.Row("yyyy", "2")      // plain, visible width 4
	var buf bytes.Buffer
	tbl.Render(&buf)

	// Strip ANSI and check the second column lines up at the same rune offset.
	plain := ansiRE.ReplaceAllString(buf.String(), "")
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), plain)
	}
	// Column B should start after the widest A cell ("yyyy" = 4) + 2 gap = col 6.
	for _, ln := range lines {
		idx := strings.IndexAny(ln, "12B")
		// header "B" and the values "1"/"2" must all begin at the same offset
		if idx != 6 {
			t.Errorf("column B misaligned in %q (offset %d, want 6)", ln, idx)
		}
	}
}
