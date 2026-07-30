package cmd

import (
	"os"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/initialize"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/spf13/cobra"
)

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise this directory as an rdk repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			store, err := repofs.New(wd)
			if err != nil {
				return err
			}
			if err := initialize.Run(store, wd); err != nil {
				return err
			}
			a.log.Result(diag.Diagnostic{
				Code:    diag.CodeInitComplete,
				Summary: "rdk init: ready — edit rdk/config.yaml, then run 'rdk apply'",
			})
			return nil
		},
	}
}
