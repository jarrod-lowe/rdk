package cmd

import (
	"fmt"
	"os"

	"github.com/jarrod-lowe/rdk/internal/apply"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/jarrod-lowe/rdk/internal/version"
	"github.com/spf13/cobra"
)

func newApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Regenerate all rdk-managed files from the definitions in rdk/",
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			store, err := repofs.New(wd)
			if err != nil {
				return err
			}
			res, err := apply.Run(store, version.Version)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.Summary())
			return nil
		},
	}
}
