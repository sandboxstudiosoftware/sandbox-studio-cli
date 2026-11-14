package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

// Backend is the slice of the API client the UI needs. *api.Client satisfies it;
// tests supply a fake.
type Backend interface {
	ListLeasesWith(ctx context.Context, opts api.ListLeasesOptions) ([]api.Lease, error)
	ListApprovals(ctx context.Context, statuses []string) ([]api.Lease, error)
	ListPublicTemplates(ctx context.Context) ([]api.PublicTemplate, error)
	RequestLease(ctx context.Context, req api.NewLeaseRequest) (*api.Lease, error)
	ReviewLease(ctx context.Context, leaseID, action string) error
	TerminateLease(ctx context.Context, leaseID string) error
	SuspendLease(ctx context.Context, leaseID string) error
	ResumeLease(ctx context.Context, leaseID string) error
	WithdrawLease(ctx context.Context, leaseID string) error
	LeaseLogs(ctx context.Context, leaseID string) (string, error)
}

type screen int

const (
	scrLeases screen = iota
	scrMenu
	scrTemplates
	scrConfirm
	scrText
)

// filters the lease view can cycle through with "f".
var filters = []struct {
	label    string
	statuses []string // nil == every status
}{
	{"Active", []string{"Active"}},
	{"Pending", []string{"PendingApproval", "PendingSetup"}},
	{"Suspended", []string{"Suspended"}},
	{"All", nil},
}

type menuItem struct {
	label string
	run   func(m *Model)
}

// Model is the whole UI state. It is not safe for concurrent use.
type Model struct {
	be  Backend
	ctx context.Context

	// Size reports the terminal size; set by the host loop, defaulted for tests.
	Size func() (w, h int)

	scr       screen
	prev      screen // screen to return to from scrText/scrConfirm
	approvals bool   // leases view shows the approvals queue
	filterIdx int
	leases    []api.Lease
	cursor    int

	menu       []menuItem
	menuCursor int

	templates []api.PublicTemplate
	tplCursor int

	confirmMsg string
	confirmRun func(m *Model)

	textTitle string
	text      []string
	textOff   int

	// PickMode makes enter on an Active lease choose it (and quit) instead of
	// opening the action menu. Used by `sbs assume` and `sbs open` with no lease
	// argument. PickVerb names the choice in the help line (default "assume role").
	PickMode bool
	PickVerb string
	assume   *api.Lease

	// OpenConsole, when set, adds "Open AWS console" to an Active lease's menu.
	// The host fetches a fresh console sign-in link for the lease and opens it in
	// the browser, returning the link and whether a browser was launched. When no
	// browser could be launched, the UI exits and ExitNotice carries the link so
	// the host can print it on the restored terminal.
	OpenConsole func(ctx context.Context, leaseID string) (url string, opened bool, err error)
	exitNotice  []string

	status    string
	statusErr bool

	pending func()
	quit    bool
}

// New creates a model. Call Init, then Settle, to load the first page.
func New(ctx context.Context, be Backend) *Model {
	return &Model{ctx: ctx, be: be, Size: func() (int, int) { return 100, 30 }}
}

// Assumed returns the lease the user chose to assume a role in, if any. The host
// loop checks it after the UI exits.
func (m *Model) Assumed() (api.Lease, bool) {
	if m.assume == nil {
		return api.Lease{}, false
	}
	return *m.assume, true
}

// ExitNotice returns lines the host should print after the UI exits (e.g. a
// console link that could not be opened in a browser).
func (m *Model) ExitNotice() []string { return m.exitNotice }

// openConsole runs the host's OpenConsole for a lease and reports the outcome.
func (m *Model) openConsole(id string) {
	m.scr = scrLeases
	m.later("Opening the AWS console…", func() {
		url, opened, err := m.OpenConsole(m.ctx, id)
		switch {
		case err != nil:
			m.setStatus("Could not open the console: "+err.Error(), true)
		case !opened:
			m.exitNotice = []string{
				"Could not open a browser. Open this link within about 15 minutes to sign in to lease " + id + ":",
				url,
			}
			m.quit = true
		default:
			m.setStatus("Opened the AWS console for "+short(id)+" in your browser (the sign-in link is valid for ~15 minutes).", false)
		}
	})
}

// chooseAssume records l as the lease to assume and leaves the UI.
func (m *Model) chooseAssume(l api.Lease) {
	m.assume = &l
	m.quit = true
}

// Quit reports whether the user asked to leave.
func (m *Model) Quit() bool { return m.quit }

// Init schedules the initial load.
func (m *Model) Init() { m.reload("Loading…") }

// HasPending reports whether there is deferred (blocking) work to run.
func (m *Model) HasPending() bool { return m.pending != nil }

// RunPending runs the deferred work, if any. The host renders once before calling
// this so the "Working…" status is visible while the API call is in flight.
func (m *Model) RunPending() {
	if f := m.pending; f != nil {
		m.pending = nil
		f()
	}
}

// Settle runs deferred work until none is left (used by tests).
func (m *Model) Settle() {
	for m.pending != nil {
		m.RunPending()
	}
}

func (m *Model) setStatus(msg string, isErr bool) { m.status, m.statusErr = msg, isErr }

// defer schedules fn to run after the next render, showing label meanwhile.
func (m *Model) later(label string, fn func()) {
	m.setStatus(label, false)
	m.pending = fn
}

func (m *Model) reload(label string) {
	m.later(label, func() {
		var (
			leases []api.Lease
			err    error
		)
		if m.approvals {
			leases, err = m.be.ListApprovals(m.ctx, []string{"pending"})
		} else {
			leases, err = m.be.ListLeasesWith(m.ctx, api.ListLeasesOptions{Statuses: filters[m.filterIdx].statuses})
		}
		if err != nil {
			m.setStatus("Load failed: "+err.Error(), true)
			return
		}
		m.leases = leases
		if m.cursor >= len(leases) {
			m.cursor = len(leases) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		if m.status == label {
			m.setStatus("", false)
		}
	})
}

// act runs a lease API call, then returns to the list with the outcome shown.
func (m *Model) act(label string, fn func() error) {
	m.scr = scrLeases
	m.later(label, func() {
		if err := fn(); err != nil {
			m.setStatus("Failed: "+err.Error(), true)
			return
		}
		done := strings.TrimSuffix(label, "…") + " — done"
		m.reload("Refreshing…")
		m.pending = chain(m.pending, func() { m.setStatus(done, false) })
	})
}

func chain(first, second func()) func() {
	return func() {
		first()
		second()
	}
}

func (m *Model) selected() (api.Lease, bool) {
	if m.cursor < 0 || m.cursor >= len(m.leases) {
		return api.Lease{}, false
	}
	return m.leases[m.cursor], true
}

// Handle processes one key press.
func (m *Model) Handle(k Key) {
	if k.Code == KeyCtrlC {
		m.quit = true
		return
	}
	switch m.scr {
	case scrLeases:
		m.handleLeases(k)
	case scrMenu:
		m.handleMenu(k)
	case scrTemplates:
		m.handleTemplates(k)
	case scrConfirm:
		m.handleConfirm(k)
	case scrText:
		m.handleText(k)
	}
}

// move applies a navigation key to a cursor over n items.
func move(k Key, cur, n, page int) int {
	switch k.Code {
	case KeyUp:
		cur--
	case KeyDown:
		cur++
	case KeyPgUp:
		cur -= page
	case KeyPgDn:
		cur += page
	case KeyHome:
		cur = 0
	case KeyEnd:
		cur = n - 1
	case KeyRune:
		switch k.Rune {
		case 'k':
			cur--
		case 'j':
			cur++
		case 'g':
			cur = 0
		case 'G':
			cur = n - 1
		}
	}
	if cur >= n {
		cur = n - 1
	}
	if cur < 0 {
		cur = 0
	}
	return cur
}

func (m *Model) page() int {
	_, h := m.Size()
	if p := h - 8; p > 3 {
		return p
	}
	return 3
}

func (m *Model) handleLeases(k Key) {
	m.cursor = move(k, m.cursor, len(m.leases), m.page())
	switch {
	case k.Code == KeyEnter || k.Code == KeyRight || (k.Code == KeyRune && k.Rune == 'l'):
		if l, ok := m.selected(); ok {
			if m.PickMode && !m.approvals {
				if l.Status != "Active" {
					m.setStatus("Only Active leases can be chosen (this one is "+l.Status+").", true)
					return
				}
				m.chooseAssume(l)
				return
			}
			m.openMenu(l)
		}
	case k.Code == KeyTab:
		m.approvals = !m.approvals
		m.cursor = 0
		m.reload("Loading…")
	case k.Code == KeyRune && k.Rune == 'f' && !m.approvals:
		m.filterIdx = (m.filterIdx + 1) % len(filters)
		m.cursor = 0
		m.reload("Loading…")
	case k.Code == KeyRune && k.Rune == 'r':
		m.reload("Refreshing…")
	case k.Code == KeyRune && k.Rune == 'n':
		m.openTemplates()
	case k.Code == KeyEsc || (k.Code == KeyRune && k.Rune == 'q'):
		m.quit = true
	}
}

func (m *Model) openTemplates() {
	m.scr = scrTemplates
	m.tplCursor = 0
	m.later("Loading templates…", func() {
		t, err := m.be.ListPublicTemplates(m.ctx)
		if err != nil {
			m.scr = scrLeases
			m.setStatus("Could not load templates: "+err.Error(), true)
			return
		}
		m.templates = t
		m.setStatus("", false)
	})
}

func (m *Model) handleTemplates(k Key) {
	m.tplCursor = move(k, m.tplCursor, len(m.templates), m.page())
	switch {
	case k.Code == KeyEnter || k.Code == KeyRight:
		if m.tplCursor < len(m.templates) {
			t := m.templates[m.tplCursor]
			note := ""
			if t.RequiresApproval {
				note = " It needs approval first."
			}
			m.askConfirm(fmt.Sprintf("Request a lease from %q?%s", t.Name, note), scrTemplates, func(m *Model) {
				m.act("Requesting lease…", func() error {
					_, err := m.be.RequestLease(m.ctx, api.NewLeaseRequest{TemplateID: t.TemplateID})
					return err
				})
			})
		}
	case k.Code == KeyEsc || k.Code == KeyLeft || (k.Code == KeyRune && k.Rune == 'q'):
		m.scr = scrLeases
	}
}

func (m *Model) askConfirm(msg string, back screen, run func(m *Model)) {
	m.prev, m.scr = back, scrConfirm
	m.confirmMsg, m.confirmRun = msg, run
}

func (m *Model) handleConfirm(k Key) {
	if k.Code == KeyRune && (k.Rune == 'y' || k.Rune == 'Y') {
		run := m.confirmRun
		m.confirmRun = nil
		if run != nil {
			run(m)
		}
		return
	}
	m.scr = m.prev // anything else cancels
	m.setStatus("Cancelled.", false)
}

func (m *Model) openMenu(l api.Lease) {
	id := l.LeaseID
	items := []menuItem{{"Show details", func(m *Model) { m.showDetails(l) }}}
	switch l.Status {
	case "Active":
		front := []menuItem{{"Assume role (use AWS credentials in this account)", func(m *Model) { m.chooseAssume(l) }}}
		if m.OpenConsole != nil {
			front = append(front, menuItem{"Open AWS console (sign in to this account in your browser)", func(m *Model) { m.openConsole(id) }})
		}
		items = append(front, items...)
		items = append(items,
			menuItem{"Show setup logs", func(m *Model) { m.showLogs(id) }},
			menuItem{"Suspend lease", func(m *Model) {
				m.askConfirm("Suspend lease "+short(id)+"? Access is revoked until resumed.", scrLeases, func(m *Model) {
					m.act("Suspending…", func() error { return m.be.SuspendLease(m.ctx, id) })
				})
			}},
			menuItem{"Terminate lease", func(m *Model) { m.confirmTerminate(l) }})
	case "Suspended":
		items = append(items,
			menuItem{"Show setup logs", func(m *Model) { m.showLogs(id) }},
			menuItem{"Resume lease", func(m *Model) {
				m.act("Resuming…", func() error { return m.be.ResumeLease(m.ctx, id) })
			}},
			menuItem{"Terminate lease", func(m *Model) { m.confirmTerminate(l) }})
	case "PendingApproval":
		items = append(items,
			menuItem{"Approve request", func(m *Model) {
				m.act("Approving…", func() error { return m.be.ReviewLease(m.ctx, id, "approve") })
			}},
			menuItem{"Deny request", func(m *Model) {
				m.askConfirm("Deny the request for lease "+short(id)+"?", scrLeases, func(m *Model) {
					m.act("Denying…", func() error { return m.be.ReviewLease(m.ctx, id, "deny") })
				})
			}},
			menuItem{"Withdraw my request", func(m *Model) {
				m.act("Withdrawing…", func() error { return m.be.WithdrawLease(m.ctx, id) })
			}})
	default:
		items = append(items, menuItem{"Show setup logs", func(m *Model) { m.showLogs(id) }})
	}
	items = append(items, menuItem{"Back", func(m *Model) { m.scr = scrLeases }})
	m.menu, m.menuCursor, m.scr = items, 0, scrMenu
}

func (m *Model) confirmTerminate(l api.Lease) {
	m.askConfirm(fmt.Sprintf("TERMINATE lease %s (%s, owner %s)? The account is wiped. This cannot be undone.",
		short(l.LeaseID), dash(l.TemplateLabel()), dash(l.OwnerLabel())), scrLeases, func(m *Model) {
		m.act("Terminating…", func() error { return m.be.TerminateLease(m.ctx, l.LeaseID) })
	})
}

func (m *Model) handleMenu(k Key) {
	m.menuCursor = move(k, m.menuCursor, len(m.menu), 99)
	switch {
	case k.Code == KeyEnter || k.Code == KeyRight:
		if m.menuCursor < len(m.menu) {
			m.menu[m.menuCursor].run(m)
		}
	case k.Code == KeyEsc || k.Code == KeyLeft || (k.Code == KeyRune && k.Rune == 'q'):
		m.scr = scrLeases
	}
}

func (m *Model) showDetails(l api.Lease) {
	lines := []string{
		"Lease ID:  " + l.LeaseID,
		"Status:    " + l.Status,
		"Account:   " + dash(l.AWSAccountID),
		"Template:  " + dash(l.TemplateLabel()),
		"Owner:     " + dash(l.OwnerLabel()),
		fmt.Sprintf("Budget:    $%.2f spent of $%.2f", l.TotalCost, l.MaxBudget),
	}
	m.openText("Lease details", lines, scrLeases)
}

func (m *Model) showLogs(id string) {
	m.scr = scrText
	m.openText("Setup logs", []string{"Loading…"}, scrLeases)
	m.later("Loading logs…", func() {
		logs, err := m.be.LeaseLogs(m.ctx, id)
		var ae *api.APIError
		if errors.As(err, &ae) && ae.StatusCode == 404 {
			m.text = []string{"No setup logs for this lease."}
			m.setStatus("", false)
			return
		}
		if err != nil {
			m.text = []string{"Could not load logs: " + err.Error()}
			m.setStatus("", false)
			return
		}
		m.text = strings.Split(strings.TrimRight(logs, "\n"), "\n")
		if logs == "" {
			m.text = []string{"(no logs)"}
		}
		m.setStatus("", false)
	})
}

func (m *Model) openText(title string, lines []string, back screen) {
	m.textTitle, m.text, m.textOff, m.prev, m.scr = title, lines, 0, back, scrText
}

func (m *Model) handleText(k Key) {
	m.textOff = move(k, m.textOff, len(m.text), m.page())
	if k.Code == KeyEsc || k.Code == KeyLeft || (k.Code == KeyRune && (k.Rune == 'q' || k.Rune == 'h')) {
		m.scr = m.prev
	}
}

// ---- rendering ----------------------------------------------------------------

const (
	reverse = "\x1b[7m"
	reset   = "\x1b[0m"
)

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// fit truncates s to w runes (adding an ellipsis) and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := utf8.RuneCountInString(s)
	if n > w {
		r := []rune(s)
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-n)
}

// window returns the first visible index so cur stays in a view of size rows.
func window(cur, n, rows int) int {
	if rows >= n || cur < rows/2 {
		return 0
	}
	start := cur - rows/2
	if start > n-rows {
		start = n - rows
	}
	return start
}

// View renders the current screen as lines (no trailing newlines, no wrapping).
func (m *Model) View() []string {
	w, h := m.Size()
	var body []string
	var help string
	switch m.scr {
	case scrLeases:
		body, help = m.viewLeases(w, h)
	case scrMenu:
		body, help = m.viewMenu(w, h), "↑/↓ choose • enter select • esc back"
	case scrTemplates:
		body, help = m.viewTemplates(w, h), "↑/↓ choose • enter request lease • esc back"
	case scrConfirm:
		body = []string{"", output.Bold("  " + m.confirmMsg), "", "  Press y to confirm, any other key to cancel."}
		help = "y confirm • any other key cancels"
	case scrText:
		body, help = m.viewText(w, h), "↑/↓ scroll • esc back"
	}

	out := make([]string, 0, h)
	out = append(out, m.header())
	out = append(out, "")
	out = append(out, body...)
	// Pad so the footer sits on the bottom rows.
	for len(out) < h-2 {
		out = append(out, "")
	}
	if len(out) > h-2 && h > 3 {
		out = out[:h-2]
	}
	st := m.status
	if r := []rune(st); w > 4 && len(r) > w-1 {
		st = string(r[:w-2]) + "…"
	}
	if m.statusErr {
		st = output.Red(st)
	} else if st != "" {
		st = output.Yellow(st)
	}
	out = append(out, st, output.Faint(help))
	return out
}

func (m *Model) header() string {
	title := "Leases"
	if m.approvals {
		title = "Approvals (pending)"
	} else {
		title += " — " + filters[m.filterIdx].label
	}
	return output.Bold("Sandbox Studio  ") + title
}

func (m *Model) viewLeases(w, h int) ([]string, string) {
	help := "↑/↓ move • enter actions • n new lease • tab leases/approvals • f filter • r refresh • q quit"
	if m.approvals {
		help = "↑/↓ move • enter actions • n new lease • tab leases/approvals • r refresh • q quit"
	}
	if m.PickMode {
		verb := m.PickVerb
		if verb == "" {
			verb = "assume role"
		}
		help = "↑/↓ move • enter " + verb + " • f filter • r refresh • q quit"
	}
	if len(m.leases) == 0 {
		if m.pending != nil {
			return nil, help
		}
		return []string{"  " + output.Faint("Nothing here. Press n to request a lease, f to change the filter.")}, help
	}
	wide := w >= 90
	hdr := "  " + fit("LEASE", 8) + "  " + fit("STATUS", 16) + "  "
	if wide {
		hdr += fit("ACCOUNT", 12) + "  "
	}
	hdr += fit("TEMPLATE", 24) + "  OWNER"
	rows := h - 6
	if rows < 3 {
		rows = 3
	}
	lines := []string{output.Bold(fit(hdr, w))}
	start := window(m.cursor, len(m.leases), rows)
	for i := start; i < len(m.leases) && i < start+rows; i++ {
		l := m.leases[i]
		sel := i == m.cursor
		marker := "  "
		if sel {
			marker = "▶ "
		}
		fixed := 2 + 8 + 2 + 16 + 2 + 24 + 2
		if wide {
			fixed += 12 + 2
		}
		owner := fit(dash(l.OwnerLabel()), w-fixed)
		if sel {
			line := marker + fit(short(l.LeaseID), 8) + "  " + fit(l.Status, 16) + "  "
			if wide {
				line += fit(dash(l.AWSAccountID), 12) + "  "
			}
			line += fit(dash(l.TemplateLabel()), 24) + "  " + owner
			lines = append(lines, reverse+fit(line, w)+reset)
			continue
		}
		pad := strings.Repeat(" ", 16-utf8.RuneCountInString(l.Status))
		line := marker + fit(short(l.LeaseID), 8) + "  " + output.StatusColour(l.Status) + pad + "  "
		if wide {
			line += fit(dash(l.AWSAccountID), 12) + "  "
		}
		line += fit(dash(l.TemplateLabel()), 24) + "  " + owner
		lines = append(lines, line)
	}
	if len(m.leases) > rows {
		lines = append(lines, output.Faint(fmt.Sprintf("  %d/%d", m.cursor+1, len(m.leases))))
	}
	return lines, help
}

func (m *Model) viewMenu(w, h int) []string {
	l, _ := m.selected()
	lines := []string{output.Bold(fmt.Sprintf("  Lease %s  %s  %s", short(l.LeaseID), output.StatusColour(l.Status), dash(l.TemplateLabel()))), ""}
	for i, it := range m.menu {
		if i == m.menuCursor {
			lines = append(lines, reverse+fit("  ▶ "+it.label, 40)+reset)
		} else {
			lines = append(lines, "    "+it.label)
		}
	}
	return lines
}

func (m *Model) viewTemplates(w, h int) []string {
	if len(m.templates) == 0 {
		if m.pending != nil {
			return nil
		}
		return []string{"  " + output.Faint("No templates available.")}
	}
	lines := []string{output.Bold("  Request a lease from…"), ""}
	rows := h - 8
	if rows < 3 {
		rows = 3
	}
	start := window(m.tplCursor, len(m.templates), rows)
	for i := start; i < len(m.templates) && i < start+rows; i++ {
		t := m.templates[i]
		extra := ""
		if t.RequiresApproval {
			extra = "  (approval required)"
		}
		label := fit(t.Name+extra, w-6)
		if i == m.tplCursor {
			lines = append(lines, reverse+"  ▶ "+label+reset)
		} else {
			lines = append(lines, "    "+label)
		}
	}
	return lines
}

func (m *Model) viewText(w, h int) []string {
	lines := []string{output.Bold("  " + m.textTitle), ""}
	rows := h - 6
	if rows < 3 {
		rows = 3
	}
	end := m.textOff + rows
	if end > len(m.text) {
		end = len(m.text)
	}
	for _, l := range m.text[m.textOff:end] {
		lines = append(lines, "  "+fit(l, w-4))
	}
	return lines
}
