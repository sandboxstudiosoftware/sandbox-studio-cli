package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

func newLeaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lease",
		Short: "Request, review, manage and inspect leases",
	}
	cmd.AddCommand(
		newLeaseListCmd(),
		newLeaseGetCmd(),
		newLeaseRequestCmd(),
		newLeaseReviewCmd("approve"),
		newLeaseReviewCmd("deny"),
		newLeaseActionCmd("withdraw", "Withdraw your pending lease request",
			"Cancels a lease request that has not yet been approved or denied.",
			(*api.Client).WithdrawLease, false),
		newLeaseActionCmd("suspend", "Suspend an active lease",
			"Temporarily revokes access to the lease's sandbox account.",
			(*api.Client).SuspendLease, false),
		newLeaseActionCmd("resume", "Resume a suspended lease",
			"Restores access to a suspended lease.",
			(*api.Client).ResumeLease, false),
		newLeaseActionCmd("terminate", "Terminate a lease (irreversible)",
			"Ends the lease immediately. The account is wiped and returned to the pool.",
			(*api.Client).TerminateLease, true),
		newLeaseShareCmd(true),
		newLeaseShareCmd(false),
		newLeaseTeamCmd(),
		newLeaseUpdateCmd(),
		newLeaseLogsCmd(),
		newLeaseSecretsCmd(),
		newLeaseWaitCmd(),
		newLeaseBrowseCmd(),
	)
	return cmd
}

// withClient resolves the active context, authenticates and runs fn.
func withClient(fn func(ctx context.Context, c *api.Client) error) error {
	_, a, err := resolve()
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := a.Client(ctx)
	if err != nil {
		return err
	}
	return fn(ctx, client)
}

// confirm asks a yes/no question on stderr and reads the answer from stdin.
// Anything but y/yes (including EOF on a non-interactive stdin) is a "no".
func confirm(msg string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", msg)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// actionResult is one lease's outcome for a state-changing command, printed as JSON
// in `-o json` mode.
type actionResult struct {
	LeaseID string `json:"leaseId"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// actionReport collects per-lease outcomes. In table mode it prints a status line
// per lease on stderr; in JSON mode it prints one array on stdout at the end.
type actionReport struct {
	action  string
	results []actionResult
	failed  int
}

func (r *actionReport) ok(id, done string) {
	r.results = append(r.results, actionResult{LeaseID: id, Action: r.action, OK: true})
	if flagOutput != "json" {
		fmt.Fprintf(os.Stderr, "%s %s: %s\n", output.Green("✓"), id, done)
	}
}

func (r *actionReport) fail(id string, err error) {
	r.failed++
	r.results = append(r.results, actionResult{LeaseID: id, Action: r.action, Error: err.Error()})
	if flagOutput != "json" {
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", output.Red("✗"), id, err)
	}
}

// finish prints the JSON array (JSON mode) and returns an error if anything failed
// so the exit status is non-zero either way.
func (r *actionReport) finish() error {
	if flagOutput == "json" {
		if err := printJSON(r.results); err != nil {
			return err
		}
	}
	if r.failed > 0 {
		return fmt.Errorf("%d of %d failed", r.failed, len(r.results))
	}
	return nil
}

func renderLeases(leases []api.Lease, emptyMsg string) error {
	if flagOutput == "json" {
		if leases == nil {
			leases = []api.Lease{}
		}
		return printJSON(leases)
	}
	if len(leases) == 0 {
		fmt.Fprintln(os.Stderr, output.Faint(emptyMsg))
		return nil
	}
	tbl := output.NewTable("LEASE ID", "STATUS", "ACCOUNT", "TEMPLATE", "OWNER")
	for _, l := range leases {
		tbl.Row(
			output.Cyan(l.LeaseID),
			output.StatusColour(l.Status),
			output.Dash(l.AWSAccountID),
			output.Dash(l.TemplateLabel()),
			output.Dash(l.OwnerLabel()),
		)
	}
	tbl.Render(os.Stdout)
	fmt.Fprintln(os.Stderr, output.Faint(fmt.Sprintf("%d lease(s).", len(leases))))
	return nil
}

func newLeaseListCmd() *cobra.Command {
	var (
		statuses    []string
		all         bool
		userID      string
		since       string
		interactive bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List leases (Active only by default)",
		Long: "Lists your leases. By default only Active leases are shown.\n\n" +
			"Use --status to filter by one or more statuses (repeat or comma-separate),\n" +
			"e.g. --status Active --status Expired  or  --status Active,Suspended.\n" +
			"Use --all to list every status. --user (managers/admins) lists another\n" +
			"user's leases; --since limits to leases started on/after a date.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if interactive {
				return runBrowse()
			}
			// Default to Active; --all clears the filter; --status overrides.
			filter := statuses
			if all {
				filter = nil
			} else if len(filter) == 0 {
				filter = []string{"Active"}
			}
			start, err := parseSince(since)
			if err != nil {
				return err
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				leases, err := c.ListLeasesWith(ctx, api.ListLeasesOptions{
					Statuses: filter, UserID: userID, StartDate: start,
				})
				if err != nil {
					return err
				}
				return renderLeases(leases, "No leases found.")
			})
		},
	}
	// StringSlice supports both repeated flags and comma-separated values.
	cmd.Flags().StringSliceVar(&statuses, "status", nil,
		"filter by status (repeatable or comma-separated), e.g. Active,Expired")
	cmd.Flags().BoolVar(&all, "all", false, "list leases of every status")
	cmd.Flags().StringVar(&userID, "user", "", "list this user's leases (managers/admins)")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "open the interactive keyboard browser")
	cmd.Flags().StringVar(&since, "since", "", "only leases started since a date (YYYY-MM-DD or RFC 3339)")
	return cmd
}

// parseSince converts a YYYY-MM-DD or RFC 3339 value to the RFC 3339 form the API
// expects. An empty value yields an empty string.
func parseSince(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("invalid date %q: use YYYY-MM-DD or RFC 3339", s)
}

func newLeaseGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <lease-id>",
		Short: "Show a single lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				lease, err := c.GetLease(ctx, args[0])
				if err != nil {
					return err
				}
				return printJSON(lease)
			})
		},
	}
}

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// resolveTemplateID accepts a template id (UUID) or an exact, case-insensitive
// template name and returns the id plus the template when it was looked up.
func resolveTemplateID(ctx context.Context, c *api.Client, ref string) (string, *api.PublicTemplate, error) {
	if uuidRE.MatchString(ref) {
		return ref, nil, nil
	}
	templates, err := c.ListPublicTemplates(ctx)
	if err != nil {
		return "", nil, err
	}
	var matches []api.PublicTemplate
	for _, t := range templates {
		if strings.EqualFold(t.Name, ref) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("no template named %q (see `sbs template list`)", ref)
	case 1:
		return matches[0].TemplateID, &matches[0], nil
	default:
		return "", nil, fmt.Errorf("%d templates are named %q; use the template id instead", len(matches), ref)
	}
}

func newLeaseRequestCmd() *cobra.Command {
	var (
		template  string
		comments  string
		forUser   string
		shareWith []string
		wait      bool
	)
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Request a new lease from a template",
		Long: "Requests a lease from an account template, given by id or exact name\n" +
			"(see `sbs template list`). Depending on the template the lease is\n" +
			"provisioned right away or waits in PendingApproval for an approver.",
		Example: "  sbs lease request --template \"Dev Sandbox\" --comments \"Workshop prep\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(template) == "" {
				return fmt.Errorf("--template is required")
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				id, _, err := resolveTemplateID(ctx, c, strings.TrimSpace(template))
				if err != nil {
					return err
				}
				lease, err := c.RequestLease(ctx, api.NewLeaseRequest{
					TemplateID:       id,
					Comments:         comments,
					UsersToShareWith: shareWith,
					LeaseForUserID:   forUser,
				})
				if err != nil {
					return err
				}
				if flagOutput == "json" {
					return printJSON(lease)
				}
				fmt.Fprintf(os.Stderr, "Lease requested: %s (%s)\n", output.Cyan(lease.LeaseID), output.StatusColour(lease.Status))
				if lease.Status == "PendingApproval" {
					fmt.Fprintln(os.Stderr, output.Faint("Waiting for approval. Check with: sbs lease get "+lease.LeaseID))
				}
				fmt.Println(lease.LeaseID)
				if wait {
					if _, err := waitForLease(ctx, c, lease.LeaseID, "Active", 5*time.Second, 10*time.Minute); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&template, "template", "", "template id or exact name (required)")
	cmd.Flags().StringVar(&comments, "comments", "", "comments for the approver")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait (up to 10m) until the lease is Active")
	cmd.Flags().StringVar(&forUser, "for-user", "", "request on behalf of this user id")
	cmd.Flags().StringSliceVar(&shareWith, "share-with", nil, "user ids to share the lease with (repeatable or comma-separated)")
	return cmd
}

func newLeaseReviewCmd(action string) *cobra.Command {
	verb := map[string]string{"approve": "Approve", "deny": "Deny"}[action]
	return &cobra.Command{
		Use:   action + " <lease-id>...",
		Short: verb + " pending lease requests",
		Long: verb + " one or more pending lease requests. Only available to designated\n" +
			"approvers of the template. List what is waiting with `sbs approval list`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				rep := &actionReport{action: action}
				for _, id := range args {
					if err := c.ReviewLease(ctx, id, action); err != nil {
						rep.fail(id, err)
						continue
					}
					rep.ok(id, map[string]string{"approve": "approved", "deny": "denied"}[action])
				}
				return rep.finish()
			})
		},
	}
}

// newLeaseActionCmd builds a simple "POST /leases/{id}/<action>" command. When
// destructive is true it shows the lease and asks for confirmation unless --yes.
func newLeaseActionCmd(
	name, short, long string,
	call func(*api.Client, context.Context, string) error,
	destructive bool,
) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   name + " <lease-id>...",
		Short: short,
		Long:  long,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				if destructive && !yes {
					for _, id := range args {
						desc := id
						if l, err := c.GetLease(ctx, id); err == nil {
							desc = fmt.Sprintf("%s (%s, owner %s, account %s)",
								id, l.TemplateLabel(), l.OwnerLabel(), l.AWSAccountID)
						}
						if !confirm(fmt.Sprintf("%s lease %s? This cannot be undone.", capitalise(name), desc)) {
							return fmt.Errorf("aborted")
						}
					}
				}
				rep := &actionReport{action: name}
				for _, id := range args {
					if err := call(c, ctx, id); err != nil {
						rep.fail(id, err)
						continue
					}
					rep.ok(id, name+" requested")
				}
				return rep.finish()
			})
		},
	}
	if destructive {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	}
	return cmd
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func newLeaseShareCmd(share bool) *cobra.Command {
	use, short := "unshare <lease-id>", "Revoke other users' access to a lease"
	if share {
		use, short = "share <lease-id>", "Share a lease with other users"
	}
	var users []string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(users) == 0 {
				return fmt.Errorf("at least one --user is required")
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				var err error
				if share {
					err = c.ShareLease(ctx, args[0], users)
				} else {
					err = c.UnshareLease(ctx, args[0], users)
				}
				if err != nil {
					return err
				}
				verb := map[bool]string{true: "share", false: "unshare"}[share]
				if flagOutput == "json" {
					return printJSON(map[string]any{"leaseId": args[0], "action": verb, "userIds": users, "ok": true})
				}
				fmt.Fprintf(os.Stderr, "%s %s: %sd\n", output.Green("✓"), args[0], verb)
				return nil
			})
		},
	}
	cmd.Flags().StringSliceVar(&users, "user", nil, "user id (repeatable or comma-separated)")
	return cmd
}

func newLeaseTeamCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "team <lease-id>",
		Short: "List the users a lease is shared with",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				members, err := c.LeaseTeamMembers(ctx, args[0])
				if err != nil {
					return err
				}
				if flagOutput == "json" {
					if members == nil {
						members = []api.TeamMember{}
					}
					return printJSON(members)
				}
				if len(members) == 0 {
					fmt.Fprintln(os.Stderr, output.Faint("Not shared with anyone."))
					return nil
				}
				tbl := output.NewTable("USER", "USER ID", "SHARED ON", "SHARED BY")
				for _, m := range members {
					tbl.Row(m.Label(), m.TeamMemberUserID, output.Dash(m.SharedOn), output.Dash(m.SharedByUserID))
				}
				tbl.Render(os.Stdout)
				return nil
			})
		},
	}
}

func newLeaseUpdateCmd() *cobra.Command {
	var (
		maxBudget   float64
		expires     string
		maxDuration float64
		noBudget    bool
		noExpiry    bool
	)
	cmd := &cobra.Command{
		Use:   "update <lease-id>",
		Short: "Change a lease's budget or expiry (managers/admins)",
		Long: "Updates budget and expiry settings on a lease. Only the flags you pass are\n" +
			"changed. Use --no-max-budget / --no-expiry to remove a limit.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			patch := map[string]any{}
			switch {
			case noBudget:
				patch["maxBudget"] = nil
			case f.Changed("max-budget"):
				if maxBudget <= 0 {
					return fmt.Errorf("--max-budget must be greater than 0")
				}
				patch["maxBudget"] = maxBudget
			}
			switch {
			case noExpiry:
				patch["expirationDate"] = nil
			case f.Changed("expires"):
				t, err := time.Parse(time.RFC3339, expires)
				if err != nil {
					if d, derr := time.Parse("2006-01-02", expires); derr == nil {
						t = d
					} else {
						return fmt.Errorf("invalid --expires %q: use YYYY-MM-DD or RFC 3339", expires)
					}
				}
				patch["expirationDate"] = t.UTC().Format(time.RFC3339)
			}
			if f.Changed("max-duration-hours") {
				if maxDuration <= 0 {
					return fmt.Errorf("--max-duration-hours must be greater than 0")
				}
				patch["maxDurationInHours"] = maxDuration
			}
			if len(patch) == 0 {
				return fmt.Errorf("nothing to update: pass --max-budget, --expires or --max-duration-hours")
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				lease, err := c.UpdateLease(ctx, args[0], patch)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "%s %s: updated\n", output.Green("✓"), args[0])
				if flagOutput == "json" {
					return printJSON(lease)
				}
				return nil
			})
		},
	}
	cmd.Flags().Float64Var(&maxBudget, "max-budget", 0, "new maximum budget in dollars")
	cmd.Flags().BoolVar(&noBudget, "no-max-budget", false, "remove the budget limit")
	cmd.Flags().StringVar(&expires, "expires", "", "new expiry (YYYY-MM-DD or RFC 3339)")
	cmd.Flags().BoolVar(&noExpiry, "no-expiry", false, "remove the expiry date")
	cmd.Flags().Float64Var(&maxDuration, "max-duration-hours", 0, "new maximum duration in hours")
	return cmd
}

func newLeaseLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <lease-id>",
		Short: "Show the account setup logs for a lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				logs, err := c.LeaseLogs(ctx, args[0])
				if err != nil {
					// A lease without a setup script has no logs; that is not a failure.
					var ae *api.APIError
					if errors.As(err, &ae) && ae.StatusCode == 404 && strings.Contains(ae.Message, "LEASE_LOGS_NOT_FOUND") {
						if flagOutput == "json" {
							return printJSON(map[string]string{"leaseId": args[0], "logs": ""})
						}
						fmt.Fprintln(os.Stderr, output.Faint("No setup logs for this lease."))
						return nil
					}
					return err
				}
				if flagOutput == "json" {
					return printJSON(map[string]string{"leaseId": args[0], "logs": logs})
				}
				fmt.Print(logs)
				if !strings.HasSuffix(logs, "\n") {
					fmt.Println()
				}
				return nil
			})
		},
	}
}

func newLeaseSecretsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "secrets <lease-id>",
		Short: "Show the build-output secrets (credentials) for a lease",
		Long: "Prints the decrypted outputs produced by the template's launch script.\n" +
			"These can be sensitive; they are written to stdout only.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				secrets, err := c.LeaseSecrets(ctx, args[0])
				if err != nil {
					return err
				}
				if flagOutput == "json" {
					if secrets == nil {
						secrets = map[string]string{}
					}
					return printJSON(secrets)
				}
				if len(secrets) == 0 {
					fmt.Fprintln(os.Stderr, output.Faint("No secrets for this lease."))
					return nil
				}
				tbl := output.NewTable("NAME", "VALUE")
				for k, v := range secrets {
					tbl.Row(k, v)
				}
				tbl.Render(os.Stdout)
				return nil
			})
		},
	}
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
