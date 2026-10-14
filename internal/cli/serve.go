package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/internal/config"
	"github.com/agarwala-ayush/netpulse/internal/metrics"
	"github.com/agarwala-ayush/netpulse/internal/runner"
	"github.com/agarwala-ayush/netpulse/internal/server"
	"github.com/agarwala-ayush/netpulse/internal/store"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

func newServeCmd(g *globalOptions) *cobra.Command {
	var (
		tf     targetFlags
		listen string
	)
	cmd := &cobra.Command{
		Use:   "serve [host | url]...",
		Short: "Run checks continuously; serve Prometheus metrics and a web dashboard",
		Long: `Run every check on its interval forever and expose the results:

  /                web dashboard
  /metrics         Prometheus metrics
  /api/v1/status   JSON snapshot
  /healthz         liveness probe
  /readyz          readiness probe (ready once every check has run)

Send SIGHUP to reload the configuration file without restarting; an invalid
new config is rejected and the previous one keeps running. SIGINT/SIGTERM
trigger a graceful shutdown.

The listen address is taken from --listen, then $PORT (as set by platforms
like Render, Fly.io or Cloud Run), then server.listen in the config.`,
		Example: `  netpulse serve -c netpulse.yaml
  netpulse serve --listen :8080 github.com cloudflare.com
  kill -HUP $(pidof netpulse)   # hot-reload config`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, err := newLogger(os.Stderr, g.logLevel, g.logFormat)
			if err != nil {
				return usageError(err)
			}
			cfg, path, err := tf.load(args)
			if err != nil {
				return usageError(err)
			}

			addr := cfg.Server.Listen
			if port := os.Getenv("PORT"); port != "" {
				addr = ":" + port
			}
			if cmd.Flags().Changed("listen") {
				addr = listen
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			d := &daemon{
				logger:  logger,
				metrics: metrics.New(),
				store:   store.New(cfg.Server.History),
			}
			if err := d.start(ctx, cfg); err != nil {
				return usageError(err)
			}
			d.metrics.ConfigLoaded(true)
			logger.Info("netpulse starting", "version", version.Get().Version,
				"targets", len(cfg.Targets), "checks", cfg.CheckCount(), "config", path)

			go d.watchReload(ctx, path)

			srv := server.New(server.Options{
				Addr:     addr,
				Title:    cfg.Server.Title,
				Metrics:  d.metrics.Handler(),
				Snapshot: d.store.Snapshot,
				Ready:    d.ready,
				Logger:   logger,
			})
			err = srv.ListenAndServe(ctx)
			d.stop()
			if err != nil {
				return fmt.Errorf("http server: %w", err)
			}
			logger.Info("netpulse stopped")
			return nil
		},
	}
	tf.register(cmd, config.DefaultInterval)
	cmd.Flags().StringVarP(&listen, "listen", "l", config.DefaultListen, "address to serve HTTP on")
	return cmd
}

// daemon owns the running scheduler and can swap it out on reload.
type daemon struct {
	logger  *slog.Logger
	metrics *metrics.Metrics
	store   *store.Store

	mu     sync.Mutex
	sched  *runner.Scheduler
	cancel context.CancelFunc
	done   chan struct{}
}

// start builds jobs from cfg and (re)starts the scheduler. If building the
// jobs fails, the currently running scheduler is left untouched.
func (d *daemon) start(parent context.Context, cfg *config.Config) error {
	jobs, err := runner.BuildJobs(cfg)
	if err != nil {
		return err
	}
	d.stop()

	// Order matters: the old scheduler has fully stopped (no more writes to
	// the sinks) before we clear metrics and the store, so no stale result
	// can sneak in after the reset.
	d.metrics.Reset()
	d.metrics.SetJobs(len(jobs))
	d.store.Register(jobInfos(jobs))

	ctx, cancel := context.WithCancel(parent)
	sched := runner.NewScheduler(jobs, cfg.Concurrency, d.logger, d.metrics, d.store)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Run(ctx)
	}()

	d.mu.Lock()
	d.sched, d.cancel, d.done = sched, cancel, done
	d.mu.Unlock()
	return nil
}

// stop cancels the running scheduler and waits for it to exit.
func (d *daemon) stop() {
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.cancel, d.done = nil, nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (d *daemon) ready() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sched != nil && d.sched.Ready()
}

// watchReload reloads the configuration file on SIGHUP — the traditional
// Unix convention for daemons (nginx, Prometheus, sshd all do this), and
// what `systemctl reload netpulse` sends via ExecReload.
func (d *daemon) watchReload(ctx context.Context, path string) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
		}
		if path == "" {
			d.logger.Warn("SIGHUP received but netpulse was started without a config file; nothing to reload")
			continue
		}
		d.logger.Info("reloading configuration", "path", path)
		cfg, err := config.Load(path)
		if err == nil {
			err = d.start(ctx, cfg)
		}
		if err != nil {
			d.metrics.ConfigLoaded(false)
			d.logger.Error("config reload failed; keeping previous configuration",
				"error", err.Error())
			continue
		}
		d.metrics.ConfigLoaded(true)
		d.logger.Info("configuration reloaded", "targets", len(cfg.Targets), "checks", cfg.CheckCount())
	}
}
