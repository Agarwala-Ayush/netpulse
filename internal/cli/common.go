package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/config"
	"github.com/agarwala-ayush/netpulse/internal/runner"
	"github.com/agarwala-ayush/netpulse/internal/store"
)

// targetFlags are shared by check, serve and watch: either a config file or
// hosts on the command line.
type targetFlags struct {
	configPath string
	checks     []string
	dnsServer  string
	interval   time.Duration
	timeout    time.Duration
}

func (f *targetFlags) register(cmd *cobra.Command, defaultInterval time.Duration) {
	fl := cmd.Flags()
	fl.StringVarP(&f.configPath, "config", "c", "", "path to config file (default: ./netpulse.yaml if present)")
	fl.StringSliceVar(&f.checks, "checks", nil, "checks to run for hosts given as arguments: dns,http,tls,tcp (default all)")
	fl.StringVar(&f.dnsServer, "dns-server", "", "resolver ip:port for DNS checks of argument hosts (default: system)")
	fl.DurationVar(&f.interval, "interval", defaultInterval, "check interval for hosts given as arguments")
	fl.DurationVarP(&f.timeout, "timeout", "t", 0, "per-check timeout (overrides config)")
}

// configSearchPath is where we look when neither --config nor hosts are given.
var configSearchPath = []string{"netpulse.yaml", "netpulse.yml", "/etc/netpulse/netpulse.yaml"}

// load resolves the configuration: an explicit --config, else hosts from
// the command line, else a config file found in the search path.
func (f *targetFlags) load(args []string) (*config.Config, string, error) {
	for _, c := range f.checks {
		if _, err := checks.ParseKind(c); err != nil {
			return nil, "", err
		}
	}

	var (
		cfg  *config.Config
		path = f.configPath
		err  error
	)
	switch {
	case path != "" && len(args) > 0:
		return nil, "", errors.New("pass either --config or hosts as arguments, not both")
	case path != "":
		cfg, err = config.Load(path)
	case len(args) > 0:
		cfg, err = config.FromHosts(args, config.QuickOptions{
			Checks:    f.checks,
			DNSServer: f.dnsServer,
			Interval:  config.Duration(f.interval),
		})
	default:
		for _, candidate := range configSearchPath {
			if _, statErr := os.Stat(candidate); statErr == nil {
				path = candidate
				break
			}
		}
		if path == "" {
			return nil, "", errors.New("no targets: pass hosts as arguments (e.g. `netpulse check github.com`), " +
				"use --config, or create ./netpulse.yaml with `netpulse init`")
		}
		cfg, err = config.Load(path)
	}
	if err != nil {
		return nil, "", err
	}

	if f.timeout > 0 {
		for i := range cfg.Targets {
			cfg.Targets[i].Timeout = config.Duration(f.timeout)
		}
		if err := cfg.Validate(); err != nil {
			return nil, "", err
		}
	}
	return cfg, path, nil
}

func jobInfos(jobs []runner.Job) []store.JobInfo {
	out := make([]store.JobInfo, len(jobs))
	for i, j := range jobs {
		out[i] = store.JobInfo{Target: j.Target, Group: j.Group, Host: j.Host, Kind: j.Checker.Kind()}
	}
	return out
}

func pluralize(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
