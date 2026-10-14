package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/internal/output"
	"github.com/agarwala-ayush/netpulse/internal/runner"
)

func newCheckCmd(_ *globalOptions) *cobra.Command {
	var (
		tf          targetFlags
		format      string
		verbose     bool
		concurrency int
	)
	cmd := &cobra.Command{
		Use:   "check [host | url | host:port]...",
		Short: "Run every check once and print the results",
		Long: `Run every configured check once, concurrently, print the results and exit.

Exit codes follow the Nagios plugin convention, so the command slots into
cron jobs, CI pipelines and existing monitoring:

  0  all checks OK
  1  at least one WARNING (slow, or certificate expiring soon)
  2  at least one CRITICAL failure
  3  UNKNOWN: invalid usage or configuration`,
		Example: `  netpulse check github.com cloudflare.com
  netpulse check https://api.example.com/healthz --checks http,tls
  netpulse check -c netpulse.yaml -o json | jq '.results[] | select(.status != "ok")'
  netpulse check -c netpulse.yaml -o markdown >> "$GITHUB_STEP_SUMMARY"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmtType, err := output.ParseFormat(format)
			if err != nil {
				return usageError(err)
			}
			cfg, _, err := tf.load(args)
			if err != nil {
				return usageError(err)
			}
			if cmd.Flags().Changed("concurrency") {
				cfg.Concurrency = max(concurrency, 1)
			}
			jobs, err := runner.BuildJobs(cfg)
			if err != nil {
				return usageError(err)
			}

			// Ctrl-C cancels in-flight checks instead of killing the process,
			// so we still print whatever finished.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			interactive := fmtType == output.FormatTable && isatty.IsTerminal(os.Stderr.Fd())
			if interactive {
				fmt.Fprintf(os.Stderr, "%s %s across %s %s\n",
					output.Accent.Render("◉ netpulse"),
					pluralize(len(jobs), "check"), pluralize(len(cfg.Targets), "target"),
					output.Muted.Render(fmt.Sprintf("(concurrency %d)", cfg.Concurrency)))
			}

			start := time.Now()
			results := runner.RunOnce(ctx, jobs, cfg.Concurrency, nil)
			elapsed := time.Since(start)

			out := cmd.OutOrStdout()
			switch fmtType {
			case output.FormatJSON:
				err = output.JSON(out, results, elapsed)
			case output.FormatMarkdown:
				err = output.Markdown(out, results, elapsed)
			default:
				err = output.Table(out, results, elapsed, output.TableOptions{Verbose: verbose})
			}
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return &exitError{code: output.ExitUnknown, err: fmt.Errorf("interrupted: some checks were cancelled")}
			}
			if code := output.ExitCode(results); code != output.ExitOK {
				return &exitError{code: code}
			}
			return nil
		},
	}
	tf.register(cmd, 30*time.Second)
	fl := cmd.Flags()
	fl.StringVarP(&format, "output", "o", "table", "output format: table, json, markdown")
	fl.BoolVarP(&verbose, "verbose", "v", false, "show timing phases and details for every check")
	fl.IntVar(&concurrency, "concurrency", 0, "maximum checks in flight (overrides config)")
	_ = fl.MarkHidden("interval") // meaningless for a single run
	return cmd
}
