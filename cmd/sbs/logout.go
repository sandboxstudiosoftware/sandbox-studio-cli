package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the API session and cached lease credentials",
		Long: "Removes the API session and any cached lease credentials for the current\n" +
			"deployment from the OS keyring, so the next command requires `sbs login` again.\n" +
			"Stored API client credentials are kept unless you pass --all.\n\n" +
			"Note: this only removes local copies. Session tokens are short-lived and expire\n" +
			"on their own; they cannot be revoked server-side.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, a, err := resolve()
			if err != nil {
				return err
			}
			a.Logout()
			credsCache(a).Clear()
			if all {
				a.ForgetClientCredentials()
			}
			if flagOutput == "json" {
				return printJSON(map[string]any{"url": ctx.URL, "loggedOut": true, "apiClientRemoved": all})
			}
			if all {
				fmt.Fprintf(os.Stderr, "Logged out of %s and removed the stored API client.\n", ctx.URL)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Logged out of %s.\n", ctx.URL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also remove the stored API client id and secret")
	return cmd
}
