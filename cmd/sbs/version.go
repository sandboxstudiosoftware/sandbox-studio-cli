package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagOutput == "json" {
				return printJSON(map[string]string{"version": versionString()})
			}
			fmt.Println(versionString())
			return nil
		},
	}
}
