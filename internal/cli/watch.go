package cli

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/runner"
	"github.com/agarwala-ayush/netpulse/internal/tui"
)

func newWatchCmd(_ *globalOptions) *cobra.Command {
	var tf targetFlags
	cmd := &cobra.Command{
		Use:   "watch [host | url]...",
		Short: "Live terminal dashboard that re-runs checks continuously",
		Example: `  netpulse watch github.com cloudflare.com google.com
  netpulse watch -c netpulse.yaml
  netpulse watch --interval 5s 1.1.1.1:53 8.8.8.8:53 --checks tcp`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := tf.load(args)
			if err != nil {
				return usageError(err)
			}
			jobs, err := runner.BuildJobs(cfg)
			if err != nil {
				return usageError(err)
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// The scheduler runs on many goroutines; Bubble Tea's model must
			// only be touched by its own event loop. A channel bridges the
			// two worlds: sinks send, the TUI receives results as messages.
			results := make(chan checks.Result, 256)
			sink := runner.SinkFunc(func(r checks.Result) {
				select {
				case results <- r:
				case <-ctx.Done():
				}
			})
			// No logger: log lines would corrupt the full-screen display.
			sched := runner.NewScheduler(jobs, cfg.Concurrency, nil, sink)
			go sched.Run(ctx)

			model := tui.New(cfg.Server.Title+" watch", jobs, results, sched.TriggerAll)
			_, err = tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
			cancel()
			if err != nil && ctx.Err() == nil {
				return err
			}
			return nil
		},
	}
	tf.register(cmd, 10*time.Second)
	return cmd
}
