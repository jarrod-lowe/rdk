package cmd

import (
	"fmt"
	"os"

	"github.com/jarrod-lowe/rdk/internal/initialize"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise this directory as an rdk repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := initialize.Run(wd); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "rdk init: ready — edit rdk/config.yaml, then run 'rdk apply'")
			return nil
		},
	}
}
