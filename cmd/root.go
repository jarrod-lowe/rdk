// Package cmd wires the rdk CLI.
package cmd

import (
	"os"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/logger"
	"github.com/spf13/cobra"
)

// app holds what every subcommand shares. The logger lives here rather than in
// a package variable so that a test can build a root command with its own
// streams and get its own logger.
type app struct {
	log *logger.Logger

	logFormat string
	logLevel  string
	color     string
}

// NewRootCmd builds the root command with all subcommands attached.
func NewRootCmd() *cobra.Command { return (&app{}).rootCmd() }

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "rdk",
		Short:        "Repository Development Kit — manages the common machinery of a service repo",
		SilenceUsage: true,
		// The logger is the only thing that writes to stderr, so cobra must not
		// print errors itself.
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			opts, err := logger.Resolve(a.logFormat, a.logLevel, a.color, os.LookupEnv)
			if err != nil {
				return err
			}
			opts.Stdout = cmd.OutOrStdout()
			opts.Stderr = cmd.ErrOrStderr()
			a.log = logger.New(opts)
			return kind.Validate()
		},
	}
	f := root.PersistentFlags()
	f.StringVar(&a.logFormat, "log-format", "", "output format: text or jsonl (default text)")
	f.StringVar(&a.logLevel, "log-level", "", "lowest level to report: debug, info, warn, error (default info)")
	f.StringVar(&a.color, "color", "", "colour in text output: auto, always, never (default auto)")

	root.AddCommand(a.versionCmd())
	root.AddCommand(a.initCmd())
	root.AddCommand(a.applyCmd())
	return root
}

// Execute runs the CLI and returns the process exit status: 1 when the user's
// definitions are at fault, 2 when rdk is.
func Execute() int {
	a := &app{}
	err := a.rootCmd().Execute()
	if err == nil {
		return 0
	}
	log := a.log
	if log == nil {
		// The failure happened before flags were parsed, so report it plainly.
		log = logger.New(logger.Options{})
	}
	log.Fail(err)
	return diag.ExitCode(err)
}
