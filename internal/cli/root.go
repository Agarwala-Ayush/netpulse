// Package cli wires netpulse's commands together using Cobra.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/internal/output"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

type globalOptions struct {
	logLevel  string
	logFormat string
	noColor   bool
}

// exitError carries a specific process exit code up to Execute.
type exitError struct {
	code int
	err  error // nil means "exit quietly with code"
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// usageError wraps configuration/usage problems as exit code 3 (UNKNOWN).
func usageError(err error) error { return &exitError{code: output.ExitUnknown, err: err} }

// Execute runs the CLI with the process arguments and returns the exit code.
func Execute() int { return run(os.Args[1:], os.Stdout, os.Stderr) }

// run is Execute with injectable arguments and writers, for tests.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			printError(stderr, ee.err)
		}
		return ee.code
	}
	printError(stderr, err)
	return output.ExitUnknown
}

func printError(w io.Writer, err error) {
	style := lipgloss.NewStyle().Foreground(output.ColorFail).Bold(true)
	msg := err.Error()
	// errors.Join produces one error per line; indent them under the header.
	if strings.Contains(msg, "\n") {
		msg = "\n  • " + strings.ReplaceAll(msg, "\n", "\n  • ")
	}
	fmt.Fprintln(w, style.Render("error:")+" "+msg)
}

func newRootCmd() *cobra.Command {
	opts := &globalOptions{}
	cmd := &cobra.Command{
		Use:   "netpulse",
		Short: "Concurrent network health checker with Prometheus metrics",
		Long: output.Accent.Render("NetPulse") + ` runs DNS, HTTP, TLS-certificate and TCP-latency checks across
many hosts concurrently. Use it as a one-shot CLI, a live terminal dashboard,
or a long-running Prometheus exporter with a built-in web dashboard.`,
		Example: `  # Check a few hosts right now (no config needed)
  netpulse check github.com cloudflare.com https://example.org/health

  # Live terminal dashboard
  netpulse watch -c netpulse.yaml

  # Prometheus exporter + web dashboard on :9469
  netpulse serve -c netpulse.yaml

  # Generate an annotated config file
  netpulse init`,
		Version:       version.Get().Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if opts.noColor {
				lipgloss.SetColorProfile(termenv.Ascii)
			}
			return nil
		},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")

	pf := cmd.PersistentFlags()
	pf.StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	pf.StringVar(&opts.logFormat, "log-format", "text", "log format: text or json")
	pf.BoolVar(&opts.noColor, "no-color", false, "disable coloured output (also honours $NO_COLOR)")

	cmd.AddCommand(
		newCheckCmd(opts),
		newServeCmd(opts),
		newWatchCmd(opts),
		newInitCmd(),
		newValidateCmd(),
		newVersionCmd(),
	)
	return cmd
}

// newLogger builds a structured slog logger writing to w.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", level)
	}
	hopts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, hopts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, hopts)), nil
	default:
		return nil, fmt.Errorf("invalid --log-format %q (want text or json)", format)
	}
}
