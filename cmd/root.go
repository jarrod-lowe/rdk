// Package cmd wires the rdk CLI.
package cmd

import "github.com/spf13/cobra"

// NewRootCmd builds the root command with all subcommands attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "rdk",
		Short:         "Repository Development Kit — manages the common machinery of a service repo",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newApplyCmd())
	return root
}
