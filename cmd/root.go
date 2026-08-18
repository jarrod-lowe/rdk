// Package cmd wires the rdk CLI.
package cmd

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/logger"
	"github.com/jarrod-lowe/rdk/internal/repofs"
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

	// storeMu guards store, which is set by whichever subcommand's RunE opens
	// one (apply, so far) and read by the signal handler below. The two run on
	// different goroutines — RunE on the one root.Execute() called from, the
	// handler on its own — so the pointer needs a lock even though only one
	// write ever happens.
	storeMu sync.Mutex
	store   repofs.Store
}

// setStore records the Store a RunE opened, giving the signal handler a route
// to it. A command that never takes a lock (version, init) can call this too
// with no ill effect: the handler now calls PrepareShutdown, not a bare
// ReleaseLock (see handleSignals), but PrepareShutdown is just as harmless
// here — its flag-write only forecloses acquireLock calls that have not
// started yet, and a command that never acquires a transaction lock has none
// pending; its own trailing ReleaseLock call is, as before, a no-op when
// nothing is held.
func (a *app) setStore(s repofs.Store) {
	a.storeMu.Lock()
	a.store = s
	a.storeMu.Unlock()
}

func (a *app) getStore() repofs.Store {
	a.storeMu.Lock()
	defer a.storeMu.Unlock()
	return a.store
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
			// Resolve returns whatever it did manage to resolve even on
			// rejection (see its doc comment), so a.log is built from that
			// partial result unconditionally: a valid --log-format=jsonl next
			// to a bad --log-level must still render the rejection as JSONL,
			// not fall back to logger.New's zero-value default of
			// FormatText.
			opts, err := logger.Resolve(a.logFormat, a.logLevel, a.color, os.LookupEnv)
			opts.Stdout = cmd.OutOrStdout()
			opts.Stderr = cmd.ErrOrStderr()
			a.log = logger.New(opts)
			if err != nil {
				return err
			}
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
	root.AddCommand(a.lockCmd())
	root.AddCommand(a.unlockCmd())
	return root
}

// Execute runs the CLI and returns the process exit status: 1 when the user's
// input is at fault, 2 when rdk is.
func Execute() int { return execute(os.Args[1:], nil, nil) }

// execute is Execute with the command line and streams injected, so the
// exit-code mapping is testable without spawning a process.
func execute(args []string, stdout, stderr io.Writer) int {
	a := &app{}
	root := a.rootCmd()
	root.SetArgs(args)
	if stdout != nil {
		root.SetOut(stdout)
	}
	if stderr != nil {
		root.SetErr(stderr)
	}

	stop := a.handleSignals()
	defer stop()

	err := root.Execute()
	if err == nil {
		// RunE returning nil only proves the logic that produced a command's
		// output ran without a Go error — not that the output reached the
		// reader. Result sits on slog.Logger.LogAttrs, whose API is void, so
		// a handler's write failure (a full disk, a broken pipe on a
		// redirected stdout) has nowhere to surface except Delivered, checked
		// here rather than by every RunE: that is what makes a command whose
		// output is the deliverable — rdk lock above all — unable to exit 0
		// having taken its action but never shown the caller the result.
		//
		// a.log is nil here for exactly one case: cobra's own --help/-h
		// handling short-circuits before PersistentPreRunE ever builds it, so
		// no Result was ever at risk of not being delivered — there is
		// nothing for Delivered to have latched.
		if a.log == nil {
			return 0
		}
		if werr := a.log.Delivered(); werr != nil {
			err = diag.Wrap(werr, diag.Diagnostic{
				Code:    diag.CodeOutputFailed,
				Summary: "rdk could not deliver its output",
				Hint:    "check the destination (redirect target, disk space); the command already ran, so for one with a side effect (rdk lock, for instance) re-running reports what it already did and still names the id you need",
			})
		} else {
			return 0
		}
	}
	err = a.renderFailure(err, stdout, stderr)
	return diag.ExitCode(err)
}

// handleSignals installs a SIGINT/SIGTERM handler for the duration of a run
// and returns a func that tears it down. SIGINT and SIGTERM don't run
// deferred functions, so without this, Ctrl-C during an apply strands
// .rdk/apply.lock and every apply after it needs --break-lock — turning the
// rare recovery path into the routine one. The handler is confined to
// foreclosing and releasing the transaction lock this process holds (a's
// Store already refuses to release anyone else's, and — since the storage
// split — cannot reach a held lock left by `rdk lock` at all, because that
// lives in a different file this releases from) and exiting; it must not
// grow into general cleanup, which is what a signal handler duplicating
// defer's job would become.
//
// It calls PrepareShutdown, not a bare ReleaseLock, because releasing only
// what is already held cannot see a race a step further back. If the signal
// arrives before acquireLock has taken the store's lock, a plain ReleaseLock
// correctly finds nothing held and returns — it is not wrong about the state
// at that instant — but a concurrent acquireLock that had not yet started
// when it ran can still go on to finish and publish .rdk/apply.lock
// afterward, with no second release ever running before os.Exit ends the
// process a few lines below. That leaves a lock stranded by an ordinary
// Ctrl-C, exactly what goal 5 of
// docs/superpowers/specs/2026-08-02-apply-lock-design.md forbids — and a
// reader who sees only "release the lock" here would have every reason to
// simplify this back to ReleaseLock, since a release-only handler looks
// sufficient right up until that timing lines up. PrepareShutdown closes it
// by also foreclosing acquisitions that have not started yet: it records,
// under the same mutex acquireLock's owned path checks immediately before
// publishing a lock, that no further lock may be created, then releases
// whatever this Store already holds. Because both run under that one mutex,
// the outcome is always one of two — refused before anything was written, or
// acquired and then found and released — never a stranded third. See
// PrepareShutdown's and acquireLock's own doc comments in
// internal/repofs/store.go for the full mechanism.
//
// The channel is buffered by one and read at most once: os/signal never
// blocks sending to it, so a second signal arriving while the first is still
// being handled is simply dropped rather than deadlocking anything, and
// os.Exit ends the process before a third could matter.
func (a *app) handleSignals() func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})

	go func() {
		select {
		case sig := <-sigCh:
			a.prepareStoreForShutdown()
			// Shell convention: 128 + signal number, so a script can tell an
			// interruption (130/143) from a definition error (1) or an rdk
			// bug (2). os.Exit bypasses execute's normal return because a
			// signal is not a value root.Execute() ever produces.
			os.Exit(128 + int(sig.(syscall.Signal)))
		case <-done:
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

// prepareStoreForShutdown is handleSignals' per-signal action on whatever
// Store setStore last recorded, split out from the goroutine above purely so
// it has a name a test can call directly: the goroutine's own os.Exit a line
// below this call always follows it in production, which would take the test
// binary down with it if a test tried to reach this through the signal
// channel instead. PrepareShutdown, not ReleaseLock — see handleSignals' own
// doc comment for why merely releasing what is already held leaves open a
// race this closes. Best-effort: a run this deep into interruption has
// nowhere left to report a failure to, and exiting promptly matters more
// than that report.
func (a *app) prepareStoreForShutdown() {
	if store := a.getStore(); store != nil {
		_ = store.PrepareShutdown()
	}
}

// renderFailure logs err through a's own logger and returns it unchanged, so
// the caller can still classify it (e.g. via diag.ExitCode). Split out of
// execute so a test driving root.Execute() directly — to inspect the raw
// error alongside the rendered text — can reach the same rendering without
// duplicating it.
func (a *app) renderFailure(err error, stdout, stderr io.Writer) error {
	log := a.log
	if log == nil {
		// PersistentPreRunE sets a.log unconditionally, from whatever
		// logger.Resolve managed to resolve, even when Resolve itself
		// rejects a value — so a nil a.log here means PersistentPreRunE
		// never ran: cobra rejected the command line before it, on an
		// unknown flag, command, or argument. That is the user's input
		// being wrong, never rdk's fault, so it must not exit 2 as an rdk
		// bug.
		var d *diag.Error
		if !errors.As(err, &d) {
			err = diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeInvalidFlag,
				Summary: "invalid command line",
				Hint:    "run 'rdk --help' for the accepted commands and flags",
			})
		}
		// The injected writers matter most on exactly this path: a test that
		// cannot see the diagnostic cannot assert on it.
		log = logger.New(logger.Options{Stdout: stdout, Stderr: stderr})
	}
	log.Fail(err)
	return err
}
