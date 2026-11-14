package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Global flags shared by all commands.
var (
	flagContext string
	flagURL     string
	flagOutput  string
	flagDebug   bool
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "sbs",
		Short:         "Sandbox Studio command-line client",
		Long:          "sbs is a cross-platform client for Sandbox Studio: log in to sandbox accounts (including as an AWS credential_process provider) and drive the Sandbox Studio API.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			switch flagOutput {
			case "", "table", "json":
				return nil
			}
			return fmt.Errorf("invalid --output %q: use table or json", flagOutput)
		},
	}

	p := root.PersistentFlags()
	p.StringVar(&flagContext, "context", "", "named context to use (defaults to current)")
	p.StringVar(&flagURL, "url", "", "override the Sandbox Studio URL")
	p.StringVarP(&flagOutput, "output", "o", "", "output format: table|json")
	p.BoolVar(&flagDebug, "debug", false, "enable debug logging to stderr")

	root.AddCommand(
		newConfigureCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newCredsCmd(),
		newAssumeCmd(),
		newOpenCmd(),
		newWhoamiCmd(),
		newLeaseCmd(),
		newApprovalCmd(),
		newTemplateCmd(),
		newVersionCmd(),
	)
	return root
}
