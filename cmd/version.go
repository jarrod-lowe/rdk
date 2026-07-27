package cmd

import (
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the rdk version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "rdk version %s\n", version.Version)
			return nil
		},
	}
}
