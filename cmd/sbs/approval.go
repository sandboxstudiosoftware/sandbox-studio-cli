package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/output"
)

func newApprovalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approval",
		Short: "List and act on lease requests awaiting approval",
	}
	cmd.AddCommand(newApprovalListCmd(), newLeaseReviewCmd("approve"), newLeaseReviewCmd("deny"))
	return cmd
}

func newApprovalListCmd() *cobra.Command {
	var (
		statuses []string
		all      bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List lease requests you can review (pending by default)",
		Long: "Lists lease requests for templates you approve. Pending requests are shown\n" +
			"by default; use --status pending,approved,denied,withdrawn or --all for more.",
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := statuses
			if all {
				filter = []string{"pending", "approved", "denied", "withdrawn"}
			} else if len(filter) == 0 {
				filter = []string{"pending"}
			}
			for i := range filter {
				filter[i] = strings.ToLower(strings.TrimSpace(filter[i]))
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				leases, err := c.ListApprovals(ctx, filter)
				if err != nil {
					return err
				}
				return renderLeases(leases, "No approvals found.")
			})
		},
	}
	cmd.Flags().StringSliceVar(&statuses, "status", nil,
		"pending, approved, denied or withdrawn (repeatable or comma-separated)")
	cmd.Flags().BoolVar(&all, "all", false, "list approvals of every status")
	return cmd
}

func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Browse the templates you can request a lease from",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List templates available for lease requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c *api.Client) error {
				templates, err := c.ListPublicTemplates(ctx)
				if err != nil {
					return err
				}
				if flagOutput == "json" {
					if templates == nil {
						templates = []api.PublicTemplate{}
					}
					return printJSON(templates)
				}
				if len(templates) == 0 {
					fmt.Fprintln(os.Stderr, output.Faint("No templates available."))
					return nil
				}
				tbl := output.NewTable("TEMPLATE ID", "NAME", "BUDGET", "MAX HOURS", "APPROVAL")
				for _, t := range templates {
					budget, hours, approval := "-", "-", "no"
					if t.MaxBudget > 0 {
						budget = fmt.Sprintf("$%.0f", t.MaxBudget)
					}
					if t.MaxDurationInHours > 0 {
						hours = fmt.Sprintf("%.0f", t.MaxDurationInHours)
					}
					if t.RequiresApproval {
						approval = "required"
					}
					tbl.Row(output.Cyan(t.TemplateID), t.Name, budget, hours, approval)
				}
				tbl.Render(os.Stdout)
				return nil
			})
		},
	})
	return cmd
}
