package output

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ansiRE matches ANSI SGR escape sequences so we can measure a cell's VISIBLE
// width (tabwriter would otherwise count the escape bytes and misalign columns).
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int {
	return len([]rune(ansiRE.ReplaceAllString(s, "")))
}

// Table is a minimal column-aligned renderer that is ANSI-aware. Headers are
// rendered bold; rows may contain styled cells.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable creates a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// Row appends a row. Extra/short cells are tolerated.
func (t *Table) Row(cells ...string) {
	t.rows = append(t.rows, cells)
}

// Render writes the aligned table to w with two-space column gaps.
func (t *Table) Render(w io.Writer) {
	cols := len(t.headers)
	widths := make([]int, cols)
	for i, h := range t.headers {
		widths[i] = visibleWidth(h)
	}
	for _, r := range t.rows {
		for i := 0; i < cols && i < len(r); i++ {
			if vw := visibleWidth(r[i]); vw > widths[i] {
				widths[i] = vw
			}
		}
	}

	writeRow := func(cells []string, styleHeader bool) {
		var b strings.Builder
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if styleHeader {
				cell = Bold(cell)
			}
			b.WriteString(cell)
			// Pad to the column's visible width (last column isn't padded).
			if i < cols-1 {
				pad := widths[i] - visibleWidth(cell)
				if pad < 0 {
					pad = 0
				}
				b.WriteString(strings.Repeat(" ", pad+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}

	writeRow(t.headers, true)
	for _, r := range t.rows {
		writeRow(r, false)
	}
}
