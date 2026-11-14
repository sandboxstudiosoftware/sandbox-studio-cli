package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

func newOpenCmd() *cobra.Command {
	var (
		role      string
		printOnly bool
	)
	cmd := &cobra.Command{
		Use:     "open [lease-id]",
		Aliases: []string{"console"},
		Short:   "Open the AWS console for a lease, signed in to its account",
		Long: "Signs you in to a lease's AWS account in your default browser.\n\n" +
			"With no lease id, an interactive list of your Active leases opens; pick one\n" +
			"with enter (the same picker as `sbs assume`).\n\n" +
			"Sandbox Studio returns a one-time console sign-in link for the lease, and sbs\n" +
			"opens it right away. The link is only valid for about 15 minutes; once you are\n" +
			"signed in, the console session lasts the lease template's session duration\n" +
			"(up to 12h, or at most 1h with --role ADMIN). Use --print to print the link\n" +
			"instead of opening it, e.g. to paste it into another browser profile.\n\n" +
			"Requires a session from `sbs login`.",
		Example: "  sbs open\n" +
			"  sbs open 3f2a9c1e-...\n" +
			"  sbs open 3f2a9c1e-... --role ADMIN\n" +
			"  sbs open 3f2a9c1e-... --print",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := normalizeRole(role)
			if err != nil {
				return err
			}
			_, a, err := resolve()
			if err != nil {
				return err
			}
			ctx := context.Background()

			var leaseID string
			if len(args) == 1 {
				leaseID = args[0]
			} else {
				client, err := a.Client(ctx)
				if err != nil {
					return err
				}
				picked, err := pickLease(ctx, client, "open console")
				if err != nil {
					return err
				}
				if picked == nil {
					fmt.Fprintln(os.Stderr, output.Faint("No lease selected."))
					return &exitCodeError{code: 1}
				}
				leaseID = picked.LeaseID
			}

			url, err := leaseConsoleURL(ctx, a, leaseID, r)
			if err != nil {
				return err
			}
			if printOnly {
				if flagOutput == "json" {
					return printJSON(map[string]any{"leaseId": leaseID, "url": url, "opened": false})
				}
				fmt.Fprintln(os.Stderr, "Console sign-in link for lease "+leaseID+" (valid for about 15 minutes):")
				fmt.Println(url)
				return nil
			}
			return launchBrowser(url)
		},
	}
	cmd.Flags().StringVar(&role, "role", "USER", roleFlagHelp)
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the sign-in link instead of opening a browser")
	return cmd
}
