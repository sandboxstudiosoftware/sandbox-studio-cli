// Package tui is a small keyboard-driven terminal UI for browsing leases and
// acting on them. It is deliberately dependency-free: the model is a plain state
// machine (Handle a key, then View), and only the thin loop in cmd/sbs touches
// the real terminal. That keeps the whole thing unit-testable.
package tui

import "unicode/utf8"

// KeyCode identifies a non-printable key (or KeyRune for a printable one).
type KeyCode int

const (
	KeyNone KeyCode = iota
	KeyRune
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyEnter
	KeyEsc
	KeyTab
	KeyBackspace
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyCtrlC
)

// Key is one decoded key press.
type Key struct {
	Code KeyCode
	Rune rune // set when Code == KeyRune
}

// R is a convenience constructor for a printable key (used by tests).
func R(r rune) Key { return Key{Code: KeyRune, Rune: r} }

// ParseKey decodes the bytes returned by a single raw-mode terminal read. Only the
// first key is decoded; terminals deliver one key per read in practice.
func ParseKey(b []byte) Key {
	if len(b) == 0 {
		return Key{}
	}
	if b[0] == 0x1b {
		if len(b) == 1 {
			return Key{Code: KeyEsc}
		}
		// CSI ("ESC [") and SS3 ("ESC O") sequences.
		if len(b) >= 3 && (b[1] == '[' || b[1] == 'O') {
			switch b[2] {
			case 'A':
				return Key{Code: KeyUp}
			case 'B':
				return Key{Code: KeyDown}
			case 'C':
				return Key{Code: KeyRight}
			case 'D':
				return Key{Code: KeyLeft}
			case 'H':
				return Key{Code: KeyHome}
			case 'F':
				return Key{Code: KeyEnd}
			case '1', '7':
				if len(b) >= 4 && b[3] == '~' {
					return Key{Code: KeyHome}
				}
			case '4', '8':
				if len(b) >= 4 && b[3] == '~' {
					return Key{Code: KeyEnd}
				}
			case '5':
				if len(b) >= 4 && b[3] == '~' {
					return Key{Code: KeyPgUp}
				}
			case '6':
				if len(b) >= 4 && b[3] == '~' {
					return Key{Code: KeyPgDn}
				}
			}
		}
		return Key{} // unknown escape sequence: ignore
	}
	switch b[0] {
	case '\r', '\n':
		return Key{Code: KeyEnter}
	case '\t':
		return Key{Code: KeyTab}
	case 0x03:
		return Key{Code: KeyCtrlC}
	case 0x7f, 0x08:
		return Key{Code: KeyBackspace}
	}
	r, _ := utf8.DecodeRune(b)
	if r == utf8.RuneError || r < 0x20 {
		return Key{}
	}
	return Key{Code: KeyRune, Rune: r}
}
