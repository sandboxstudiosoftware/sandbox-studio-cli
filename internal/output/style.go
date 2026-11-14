// Package output provides tiny, dependency-free terminal styling and table
// rendering. Colour is emitted only when stdout is an interactive TTY and
// NO_COLOR is unset, so piped output (credential_process, `-o json`, `| jq`)
// stays plain and machine-readable.
package output

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// colourEnabled is resolved once based on the real stdout.
var colourEnabled = resolveColour(os.Stdout)

func resolveColour(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("SBS_FORCE_COLOR") != "" {
		return true
	}
	return term.IsTerminal(int(f.Fd()))
}

// SetColourForTest overrides colour detection; returns a restore func.
func SetColourForTest(on bool) func() {
	prev := colourEnabled
	colourEnabled = on
	return func() { colourEnabled = prev }
}

// ANSI SGR codes used by the helpers below.
const (
	reset     = "\x1b[0m"
	bold      = "\x1b[1m"
	faint     = "\x1b[2m"
	fgRed     = "\x1b[31m"
	fgGreen   = "\x1b[32m"
	fgYellow  = "\x1b[33m"
	fgBlue    = "\x1b[34m"
	fgMagenta = "\x1b[35m"
	fgCyan    = "\x1b[36m"
	fgGrey    = "\x1b[90m"
)

func wrap(code, s string) string {
	if !colourEnabled || s == "" {
		return s
	}
	return code + s + reset
}

// Styling helpers. Each is a no-op when colour is disabled.
func Bold(s string) string    { return wrap(bold, s) }
func Faint(s string) string   { return wrap(faint, s) }
func Red(s string) string     { return wrap(fgRed, s) }
func Green(s string) string   { return wrap(fgGreen, s) }
func Yellow(s string) string  { return wrap(fgYellow, s) }
func Blue(s string) string    { return wrap(fgBlue, s) }
func Magenta(s string) string { return wrap(fgMagenta, s) }
func Cyan(s string) string    { return wrap(fgCyan, s) }
func Grey(s string) string    { return wrap(fgGrey, s) }

// StatusColour maps a lease status to a sensible colour.
func StatusColour(status string) string {
	switch status {
	case "Active":
		return Green(status)
	case "PendingApproval", "ProcessingApproval", "PendingSetup", "Reserved":
		return Yellow(status)
	case "Suspended", "SetupFailed", "AccountQuarantined":
		return Red(status)
	case "Expired", "ManuallyTerminated", "ApprovalDenied", "Withdrawn", "BudgetExceeded",
		"Ejected", "ProcessingTermination":
		return Faint(status)
	default:
		return status
	}
}

// Dash renders an em dash for empty values (styled faint).
func Dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return Faint("—")
	}
	return s
}

// KeyValue prints an aligned "Label: value" line with the label styled.
func KeyValue(label, value string) string {
	return fmt.Sprintf("%s %s", Bold(label+":"), value)
}
