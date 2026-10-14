package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/agarwala-ayush/netpulse/configs"
	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/config"
	"github.com/agarwala-ayush/netpulse/internal/output"
	"github.com/agarwala-ayush/netpulse/internal/runner"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Write an annotated example config file",
		Long:  "Write an annotated example configuration to path (default ./netpulse.yaml). Use \"-\" for stdout.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "netpulse.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			if path == "-" {
				_, err := cmd.OutOrStdout().Write(configs.Example)
				return err
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if force {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			f, err := os.OpenFile(path, flags, 0o644) //nolint:gosec // config is not secret; operator chooses path
			if errors.Is(err, fs.ErrExist) {
				return usageError(fmt.Errorf("%s already exists (use --force to overwrite)", path))
			}
			if err != nil {
				return err
			}
			if _, err := f.Write(configs.Example); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s wrote %s\n  next: netpulse validate -c %s && netpulse check -c %s\n",
				output.StatusStyle(checks.StatusOK).Render("✔"), path, path, path)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing file")
	return cmd
}

func newValidateCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a config file and print what it would check",
		Example: `  netpulse validate -c netpulse.yaml
  netpulse validate -c /etc/netpulse/netpulse.yaml && systemctl reload netpulse`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(path)
			if err != nil {
				return usageError(err)
			}
			if _, err := runner.BuildJobs(cfg); err != nil {
				return usageError(err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s %s is valid: %s, %s, concurrency %d\n\n",
				output.StatusStyle(checks.StatusOK).Render("✔"), path,
				pluralize(len(cfg.Targets), "target"), pluralize(cfg.CheckCount(), "check"), cfg.Concurrency)
			nameW := 6
			for _, t := range cfg.Targets {
				nameW = max(nameW, len(t.Name))
			}
			fmt.Fprintln(out, output.Muted.Render(fmt.Sprintf("  %-*s  %-28s  %-9s  %s", nameW, "TARGET", "HOST", "INTERVAL", "CHECKS")))
			for _, t := range cfg.Targets {
				fmt.Fprintf(out, "  %-*s  %-28s  %-9s  %s\n", nameW, t.Name, t.Host,
					time.Duration(t.Interval), strings.Join(t.Kinds(), ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "config", "c", "netpulse.yaml", "path to config file")
	return cmd
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}
