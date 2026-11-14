package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/tui"
)

func newLeaseBrowseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "browse",
		Aliases: []string{"ui"},
		Short:   "Browse leases with the keyboard and act on them",
		Long: "Opens an interactive full-screen view of your leases. Move with the arrow\n" +
			"keys (or j/k), press enter for the actions available on a lease (details,\n" +
			"logs, assume role, open AWS console, suspend, resume, terminate, approve, deny,\n" +
			"withdraw), n to request a\n" +
			"lease from a template, tab to switch to the approvals queue, f to change the\n" +
			"status filter, r to refresh and q to quit. Destructive actions ask first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowse()
		},
	}
}

// runBrowse runs the interactive UI against the active deployment. If the user
// chooses "Assume role" on a lease, it then starts a subshell with that lease's
// credentials.
func runBrowse() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("interactive mode needs a terminal; use `sbs lease list` in scripts")
	}
	_, a, err := resolve()
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := a.Client(ctx)
	if err != nil {
		return err
	}
	openConsole := func(ctx context.Context, leaseID string) (string, bool, error) {
		url, err := leaseConsoleURL(ctx, a, leaseID, api.AccessRoleUser)
		if err != nil {
			return "", false, err
		}
		return url, openBrowser(url) == nil, nil
	}
	picked, err := browseLoop(ctx, client, os.Stdout, browseOptions{openConsole: openConsole})
	if err != nil || picked == nil {
		return err
	}
	// The UI has restored the terminal by now.
	// Check access (and prime the credentials cache) before starting the shell.
	if _, _, err := assumeLeaseEnv(ctx, a, picked.LeaseID, api.AccessRoleUser); err != nil {
		return err
	}
	env, err := refreshingEnv(picked.LeaseID, api.AccessRoleUser)
	if err != nil {
		return err
	}
	return runSubshell(env, picked.LeaseID, "; credentials refresh automatically")
}

// pickLease shows the lease list and returns the Active lease the user chose, or
// nil if they quit. verb names the choice in the help line (e.g. "assume role").
// It draws on stderr so stdout stays free for `--export`.
func pickLease(ctx context.Context, be tui.Backend, verb string) (*api.Lease, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return nil, fmt.Errorf("no lease specified and not at a terminal: pass a lease id (see `sbs lease list`)")
	}
	return browseLoop(ctx, be, os.Stderr, browseOptions{pick: true, pickVerb: verb})
}

// browseOptions configures browseLoop.
type browseOptions struct {
	pick        bool   // enter chooses an Active lease instead of opening the menu
	pickVerb    string // help-line label for the pick action
	openConsole func(ctx context.Context, leaseID string) (url string, opened bool, err error)
}

// browseLoop runs the full-screen UI on the terminal behind w. In pick mode,
// enter chooses an Active lease. It returns the lease the user chose to assume a
// role in, or nil if they just quit.
func browseLoop(ctx context.Context, be tui.Backend, w *os.File, opts browseOptions) (*api.Lease, error) {
	in, out := int(os.Stdin.Fd()), int(w.Fd())
	old, err := term.MakeRaw(in)
	if err != nil {
		return nil, fmt.Errorf("entering raw terminal mode: %w", err)
	}
	m := tui.New(ctx, be)
	m.PickMode = opts.pick
	m.PickVerb = opts.pickVerb
	m.OpenConsole = opts.openConsole

	// Alternate screen + hidden cursor; always restored, even on error or panic.
	fmt.Fprint(w, "\x1b[?1049h\x1b[?25l")
	defer func() {
		fmt.Fprint(w, "\x1b[?25h\x1b[?1049l")
		_ = term.Restore(in, old)
		// Only once the normal screen is back, so the text stays visible.
		for _, line := range m.ExitNotice() {
			fmt.Fprintln(w, line)
		}
	}()
	m.Size = func() (int, int) {
		w, h, err := term.GetSize(out)
		if err != nil || w <= 0 || h <= 0 {
			return 80, 24
		}
		return w, h
	}

	render := func() {
		var b strings.Builder
		b.WriteString("\x1b[H")
		for _, l := range m.View() {
			b.WriteString(l)
			b.WriteString("\x1b[K\r\n")
		}
		b.WriteString("\x1b[J")
		_, _ = w.WriteString(b.String())
	}
	// settle renders before each blocking API call so "Working…" is visible.
	settle := func() {
		for m.HasPending() {
			render()
			m.RunPending()
		}
		render()
	}

	m.Init()
	settle()

	buf := make([]byte, 32)
	for !m.Quit() {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil, nil // stdin closed
		}
		m.Handle(tui.ParseKey(buf[:n]))
		settle()
	}
	if l, ok := m.Assumed(); ok {
		return &l, nil
	}
	return nil, nil
}
