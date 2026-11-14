package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandboxstudio/sbs-cli/internal/api"
)

type fakeBackend struct {
	leases       []api.Lease
	approvals    []api.Lease
	templates    []api.PublicTemplate
	calls        []string
	failOn       string
	lastOpts     api.ListLeasesOptions
	logsNotFound bool
}

func (f *fakeBackend) rec(s string) error {
	f.calls = append(f.calls, s)
	if f.failOn == s {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeBackend) ListLeasesWith(_ context.Context, o api.ListLeasesOptions) ([]api.Lease, error) {
	f.lastOpts = o
	return f.leases, f.rec("list")
}
func (f *fakeBackend) ListApprovals(context.Context, []string) ([]api.Lease, error) {
	return f.approvals, f.rec("approvals")
}
func (f *fakeBackend) ListPublicTemplates(context.Context) ([]api.PublicTemplate, error) {
	return f.templates, f.rec("templates")
}
func (f *fakeBackend) RequestLease(_ context.Context, r api.NewLeaseRequest) (*api.Lease, error) {
	return &api.Lease{LeaseID: "NEW"}, f.rec("request:" + r.TemplateID)
}
func (f *fakeBackend) ReviewLease(_ context.Context, id, a string) error {
	return f.rec(a + ":" + id)
}
func (f *fakeBackend) TerminateLease(_ context.Context, id string) error {
	return f.rec("terminate:" + id)
}
func (f *fakeBackend) SuspendLease(_ context.Context, id string) error { return f.rec("suspend:" + id) }
func (f *fakeBackend) ResumeLease(_ context.Context, id string) error  { return f.rec("resume:" + id) }
func (f *fakeBackend) WithdrawLease(_ context.Context, id string) error {
	return f.rec("withdraw:" + id)
}
func (f *fakeBackend) LeaseLogs(_ context.Context, id string) (string, error) {
	if f.logsNotFound {
		return "", &api.APIError{StatusCode: 404, Message: "api.errors.LEASE_LOGS_NOT_FOUND"}
	}
	return "line one\nline two\n", f.rec("logs:" + id)
}

func newModel(f *fakeBackend) *Model {
	m := New(context.Background(), f)
	m.Init()
	m.Settle()
	return m
}

func keys(m *Model, ks ...Key) {
	for _, k := range ks {
		m.Handle(k)
		m.Settle()
	}
}

func view(m *Model) string { return strings.Join(m.View(), "\n") }

func (f *fakeBackend) called(s string) bool {
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

var (
	up    = Key{Code: KeyUp}
	down  = Key{Code: KeyDown}
	enter = Key{Code: KeyEnter}
	esc   = Key{Code: KeyEsc}
)

func twoLeases() *fakeBackend {
	return &fakeBackend{leases: []api.Lease{
		{LeaseID: "aaaaaaaa-1", Status: "Active", AWSAccountID: "111122223333"},
		{LeaseID: "bbbbbbbb-2", Status: "Suspended"},
	}}
}

func TestParseKey(t *testing.T) {
	cases := map[string]Key{
		"\x1b[A": {Code: KeyUp}, "\x1b[B": {Code: KeyDown}, "\x1bOB": {Code: KeyDown},
		"\x1b[C": {Code: KeyRight}, "\x1b[D": {Code: KeyLeft}, "\x1b": {Code: KeyEsc},
		"\r": {Code: KeyEnter}, "\t": {Code: KeyTab}, "\x03": {Code: KeyCtrlC},
		"\x1b[5~": {Code: KeyPgUp}, "\x1b[6~": {Code: KeyPgDn}, "\x1b[H": {Code: KeyHome},
		"q": {Code: KeyRune, Rune: 'q'}, "é": {Code: KeyRune, Rune: 'é'},
	}
	for in, want := range cases {
		if got := ParseKey([]byte(in)); got != want {
			t.Errorf("ParseKey(%q) = %+v, want %+v", in, got, want)
		}
	}
	if got := ParseKey([]byte("\x1b[99X")); got.Code != KeyNone {
		t.Errorf("unknown sequence should be ignored, got %+v", got)
	}
}

func TestNavigationAndView(t *testing.T) {
	m := newModel(twoLeases())
	v := view(m)
	if !strings.Contains(v, "aaaaaaaa") || !strings.Contains(v, "bbbbbbbb") {
		t.Fatalf("leases not rendered:\n%s", v)
	}
	keys(m, down, down, down) // clamps at the last row
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.cursor)
	}
	keys(m, up, up)
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
}

func TestSuspendFlowNeedsConfirmation(t *testing.T) {
	f := twoLeases()
	m := newModel(f)
	keys(m, enter) // open menu on the Active lease
	if !strings.Contains(view(m), "Suspend lease") {
		t.Fatalf("menu missing Suspend:\n%s", view(m))
	}
	keys(m, down, down, down, enter) // assume, details, logs, suspend
	if f.called("suspend:aaaaaaaa-1") {
		t.Fatal("suspended before confirming")
	}
	keys(m, R('n')) // cancel
	if f.called("suspend:aaaaaaaa-1") {
		t.Fatal("cancel still suspended")
	}
	keys(m, enter, down, down, down, enter, R('y'))
	if !f.called("suspend:aaaaaaaa-1") {
		t.Fatalf("suspend not called: %v", f.calls)
	}
	if !strings.Contains(m.status, "done") {
		t.Errorf("status = %q, want a done message", m.status)
	}
}

func TestTerminateConfirmedOnly(t *testing.T) {
	f := twoLeases()
	m := newModel(f)
	keys(m, enter, down, down, down, down, enter) // Active menu: assume, details, logs, suspend, terminate
	if !strings.Contains(view(m), "TERMINATE") {
		t.Fatalf("no terminate confirmation:\n%s", view(m))
	}
	keys(m, enter) // enter is not 'y'
	if f.called("terminate:aaaaaaaa-1") {
		t.Fatal("terminated without y")
	}
	keys(m, enter, down, down, down, down, enter, R('y'))
	if !f.called("terminate:aaaaaaaa-1") {
		t.Fatalf("terminate not called: %v", f.calls)
	}
}

func TestResumeSuspendedLease(t *testing.T) {
	f := twoLeases()
	m := newModel(f)
	keys(m, down, enter, down, down, enter) // Suspended menu: details, logs, resume
	if !f.called("resume:bbbbbbbb-2") {
		t.Fatalf("resume not called: %v", f.calls)
	}
}

func TestApprovalsTabApprove(t *testing.T) {
	f := twoLeases()
	f.approvals = []api.Lease{{LeaseID: "pppppppp-3", Status: "PendingApproval"}}
	m := newModel(f)
	keys(m, Key{Code: KeyTab})
	if !strings.Contains(view(m), "pppppppp") || !strings.Contains(view(m), "Approvals") {
		t.Fatalf("approvals view missing:\n%s", view(m))
	}
	keys(m, enter, down, enter) // details, approve
	if !f.called("approve:pppppppp-3") {
		t.Fatalf("approve not called: %v", f.calls)
	}
}

func TestNewLeaseFromTemplate(t *testing.T) {
	f := twoLeases()
	f.templates = []api.PublicTemplate{{TemplateID: "T1", Name: "Dev", RequiresApproval: true}}
	m := newModel(f)
	keys(m, R('n'))
	if !strings.Contains(view(m), "Dev") {
		t.Fatalf("template picker missing:\n%s", view(m))
	}
	keys(m, enter)
	if !strings.Contains(view(m), "needs approval") {
		t.Errorf("confirmation should mention approval:\n%s", view(m))
	}
	keys(m, R('y'))
	if !f.called("request:T1") {
		t.Fatalf("request not sent: %v", f.calls)
	}
}

func TestFilterCycleAndErrors(t *testing.T) {
	f := twoLeases()
	m := newModel(f)
	if got := f.lastOpts.Statuses; len(got) != 1 || got[0] != "Active" {
		t.Fatalf("default filter = %v", got)
	}
	keys(m, R('f'), R('f'), R('f'))
	if f.lastOpts.Statuses != nil {
		t.Errorf("All filter should send no statuses, got %v", f.lastOpts.Statuses)
	}
	f.failOn = "suspend:aaaaaaaa-1"
	keys(m, R('f'), enter, down, down, down, enter, R('y'))
	if !m.statusErr || !strings.Contains(m.status, "boom") {
		t.Errorf("expected an error status, got %q (err=%v)", m.status, m.statusErr)
	}
}

func TestLogsAndQuit(t *testing.T) {
	m := newModel(twoLeases())
	keys(m, enter, down, down, enter) // assume, details, logs
	if !strings.Contains(view(m), "line two") {
		t.Fatalf("logs not shown:\n%s", view(m))
	}
	keys(m, esc)
	keys(m, R('q'))
	if !m.Quit() {
		t.Error("q should quit")
	}
}

func TestViewFitsTerminal(t *testing.T) {
	m := newModel(twoLeases())
	m.Size = func() (int, int) { return 60, 12 }
	if n := len(m.View()); n > 12 {
		t.Errorf("view has %d lines, want <= 12", n)
	}
}

func TestLogsNotFoundShowsFriendlyText(t *testing.T) {
	f := twoLeases()
	f.logsNotFound = true
	m := newModel(f)
	keys(m, enter, down, down, enter)
	v := view(m)
	if !strings.Contains(v, "No setup logs for this lease") || m.statusErr {
		t.Errorf("expected a friendly message, statusErr=%v:\n%s", m.statusErr, v)
	}
}

func TestMenuAssumeRoleOnActiveLease(t *testing.T) {
	f := &fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}}
	m := newModel(f)
	keys(m, Key{Code: KeyEnter}) // open menu
	if !strings.Contains(view(m), "Assume role") {
		t.Fatalf("menu lacks Assume role:\n%s", view(m))
	}
	keys(m, Key{Code: KeyEnter}) // first item
	l, ok := m.Assumed()
	if !ok || l.LeaseID != "L1" || !m.Quit() {
		t.Fatalf("Assumed = %v %v, quit=%v", l, ok, m.Quit())
	}
}

func TestMenuNoAssumeRoleOnSuspendedLease(t *testing.T) {
	f := &fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Suspended"}}}
	m := newModel(f)
	keys(m, Key{Code: KeyEnter})
	if strings.Contains(view(m), "Assume role") {
		t.Fatalf("suspended lease should not offer Assume role:\n%s", view(m))
	}
}

func TestPickModeEnterChoosesActiveLease(t *testing.T) {
	f := &fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}}
	m := New(context.Background(), f)
	m.PickMode = true
	m.Init()
	m.Settle()
	keys(m, Key{Code: KeyEnter})
	if l, ok := m.Assumed(); !ok || l.LeaseID != "L1" || !m.Quit() {
		t.Fatalf("pick failed: %v %v quit=%v", l, ok, m.Quit())
	}
}

func TestPickModeRejectsNonActiveLease(t *testing.T) {
	f := &fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Suspended"}}}
	m := New(context.Background(), f)
	m.PickMode = true
	m.Init()
	m.Settle()
	keys(m, Key{Code: KeyEnter})
	if _, ok := m.Assumed(); ok || m.Quit() {
		t.Fatal("non-active lease must not be picked")
	}
	if !strings.Contains(view(m), "Only Active leases") {
		t.Errorf("expected explanation:\n%s", view(m))
	}
}

// consoleOpener is a fake host OpenConsole that records the lease it was asked for.
type consoleOpener struct {
	got    []string
	opened bool
	err    error
}

func (c *consoleOpener) fn(_ context.Context, id string) (string, bool, error) {
	c.got = append(c.got, id)
	return "https://signin.aws.amazon.com/federation?Action=login&SigninToken=t", c.opened, c.err
}

func consoleModel(f *fakeBackend, c *consoleOpener) *Model {
	m := New(context.Background(), f)
	m.OpenConsole = c.fn
	m.Init()
	m.Settle()
	return m
}

func TestMenuOpenConsoleOnlyWhenHostSupportsIt(t *testing.T) {
	active := []api.Lease{{LeaseID: "L1", Status: "Active"}}
	m := newModel(&fakeBackend{leases: active})
	keys(m, Key{Code: KeyEnter})
	if strings.Contains(view(m), "Open AWS console") {
		t.Fatalf("no OpenConsole callback, so no menu item:\n%s", view(m))
	}

	m = consoleModel(&fakeBackend{leases: active}, &consoleOpener{opened: true})
	keys(m, Key{Code: KeyEnter})
	v := view(m)
	if !strings.Contains(v, "Open AWS console") || strings.Index(v, "Assume role") > strings.Index(v, "Open AWS console") {
		t.Fatalf("menu should offer Open AWS console right after Assume role:\n%s", v)
	}

	m = consoleModel(&fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Suspended"}}}, &consoleOpener{})
	keys(m, Key{Code: KeyEnter})
	if strings.Contains(view(m), "Open AWS console") {
		t.Fatalf("a suspended lease should not offer the console:\n%s", view(m))
	}
}

func TestMenuOpenConsoleOpensAndStaysInUI(t *testing.T) {
	c := &consoleOpener{opened: true}
	m := consoleModel(&fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}}, c)
	keys(m, Key{Code: KeyEnter}, Key{Code: KeyDown}, Key{Code: KeyEnter}) // menu, 2nd item
	if len(c.got) != 1 || c.got[0] != "L1" {
		t.Fatalf("OpenConsole calls = %v, want [L1]", c.got)
	}
	if m.Quit() || m.statusErr || !strings.Contains(view(m), "Opened the AWS console") {
		t.Errorf("quit=%v statusErr=%v:\n%s", m.Quit(), m.statusErr, view(m))
	}
	if _, ok := m.Assumed(); ok {
		t.Error("opening the console must not assume the role")
	}
}

func TestMenuOpenConsoleError(t *testing.T) {
	c := &consoleOpener{err: errors.New("lease L1 is not active")}
	m := consoleModel(&fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}}, c)
	keys(m, Key{Code: KeyEnter}, Key{Code: KeyDown}, Key{Code: KeyEnter})
	if m.Quit() || !m.statusErr || !strings.Contains(view(m), "lease L1 is not active") {
		t.Errorf("quit=%v statusErr=%v:\n%s", m.Quit(), m.statusErr, view(m))
	}
}

// Without a browser, the UI exits and hands the link to the host to print.
func TestMenuOpenConsoleWithoutBrowserExitsWithLink(t *testing.T) {
	c := &consoleOpener{opened: false}
	m := consoleModel(&fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}}, c)
	keys(m, Key{Code: KeyEnter}, Key{Code: KeyDown}, Key{Code: KeyEnter})
	notice := strings.Join(m.ExitNotice(), "\n")
	if !m.Quit() || !strings.Contains(notice, "https://signin.aws.amazon.com/federation") || !strings.Contains(notice, "15 minutes") {
		t.Errorf("quit=%v notice=%q", m.Quit(), notice)
	}
}

func TestPickVerbInHelp(t *testing.T) {
	m := New(context.Background(), &fakeBackend{leases: []api.Lease{{LeaseID: "L1", Status: "Active"}}})
	m.PickMode = true
	m.Init()
	m.Settle()
	if !strings.Contains(view(m), "enter assume role") {
		t.Errorf("default pick verb missing:\n%s", view(m))
	}
	m.PickVerb = "open console"
	if !strings.Contains(view(m), "enter open console") {
		t.Errorf("custom pick verb missing:\n%s", view(m))
	}
}
