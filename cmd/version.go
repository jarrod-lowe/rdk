package cmd

import (
	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/version"
	"github.com/spf13/cobra"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the rdk version",
		// None of rdk's commands takes an argument, and cobra ignores extras
		// unless asked. Without this, `rdk init /some/path` silently
		// initialises the working directory instead (rule 6).
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.log.Result(diag.Diagnostic{
				Code:    diag.CodeVersion,
				Summary: "rdk version " + version.Version,
				Attrs:   []diag.Attr{diag.Str("version", version.Version)},
			})
			return nil
		},
	}
}
