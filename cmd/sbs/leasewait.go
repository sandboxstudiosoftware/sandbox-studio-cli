package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

// failureStatuses are end states a lease will not leave on its own, so waiting for
// any other status would never succeed.
var failureStatuses = map[string]bool{
	"ApprovalDenied":     true,
	"Withdrawn":          true,
	"SetupFailed":        true,
	"Expired":            true,
	"BudgetExceeded":     true,
	"ManuallyTerminated": true,
	"AccountQuarantined": true,
	"Ejected":            true,
}

// waitForLease polls the lease until its status equals target (case-insensitive).
// It fails fast if the lease reaches a different terminal status, and gives up
// after timeout. Progress is reported on stderr whenever the status changes.
func waitForLease(ctx context.Context, c *api.Client, id, target string, interval, timeout time.Duration) (*api.Lease, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	last := ""
	for {
		lease, err := c.GetLease(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("timed out after %s waiting for lease %s to be %s (last status: %s)",
					timeout, id, target, output.Dash(last))
			}
			return nil, err
		}
		if lease.Status != last {
			fmt.Fprintf(os.Stderr, "%s %s\n", output.Faint(time.Now().Format("15:04:05")), output.StatusColour(lease.Status))
			last = lease.Status
		}
		if strings.EqualFold(lease.Status, target) {
			return lease, nil
		}
		if failureStatuses[lease.Status] {
			return lease, fmt.Errorf("lease %s ended as %s without reaching %s", id, lease.Status, target)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out after %s waiting for lease %s to be %s (last status: %s)",
				timeout, id, target, last)
		case <-time.After(interval):
		}
	}
}

func newLeaseWaitCmd() *cobra.Command {
	var (
		target   string
		timeout  time.Duration
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <lease-id>",
		Short: "Wait until a lease reaches a status (Active by default)",
		Long: "Polls a lease until it reaches the target status, then exits 0. Exits non-zero\n" +
			"if the lease ends in a different final state (denied, setup failed, expired...)\n" +
			"or the timeout passes. Useful right after `lease request` or `lease approve`.",
		Example: "  sbs lease wait 3f2a... --timeout 20m\n" +
			"  sbs lease request --template \"Dev Sandbox\" --wait",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 || timeout <= 0 {
				return fmt.Errorf("--interval and --timeout must be greater than 0")
			}
			return withClient(func(ctx context.Context, c *api.Client) error {
				lease, err := waitForLease(ctx, c, args[0], target, interval, timeout)
				if err != nil {
					return err
				}
				if flagOutput == "json" {
					return printJSON(lease)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&target, "for", "Active", "status to wait for")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "give up after this long")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "polling interval")
	return cmd
}
