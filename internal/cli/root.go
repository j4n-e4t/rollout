// Package cli implements the rollout command line.
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// Version is set at build time via -ldflags.
var Version = "dev"

type globalFlags struct {
	configPath   string
	identities   []string
	insecureHost bool
	plain        bool
}

// NewRoot builds the rollout command tree.
func NewRoot() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:           "rollout",
		Short:         "Deploy Docker Compose services to servers over SSH",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&g.plain, "plain", false, "disable colors and icons in rollout output")
	pf.StringVarP(&g.configPath, "config", "c", envOr("ROLLOUT_CONFIG", config.DefaultFile), "path to the environment file")
	pf.StringSliceVarP(&g.identities, "identity", "i", nil, "private key file passed to ssh (repeatable)")
	pf.BoolVar(&g.insecureHost, "insecure-ignore-host-key", false, "do not verify server host keys")
	root.AddCommand(newDeployCmd(g), newImportCmd(g), newLsCmd(g), newShowCmd(g), newHistoryCmd(g), newSecretsCmd(g), newLogsCmd(g), newExecCmd(g), newDashboardCmd(g), newInitCmd(g), newValidateCmd(g))
	return root
}

// newSSH builds the ssh runner. Outside a terminal, ssh must not prompt.
func (g *globalFlags) newSSH() (*remote.SSH, error) {
	return remote.New(remote.Options{
		IdentityFiles:         g.identities,
		InsecureIgnoreHostKey: g.insecureHost,
		BatchMode:             !isTerminal(),
	})
}

func newValidateCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the environment file for errors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s is valid: %d servers, %d services\n",
				g.configPath, len(cfg.Servers), len(cfg.Services))
			return nil
		},
	}
}

func isTerminal() bool {
	if os.Getenv("CI") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func completeServices(g *globalFlags) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load(g.configPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return cfg.ServiceNames(), cobra.ShellCompDirectiveNoFileComp
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// truncate shortens s to n runes, adding an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ago formats a timestamp as a short relative duration ("5m ago").
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	return duration(d) + " ago"
}

// duration formats d in its largest whole unit ("45s", "9m", "3h", "4d").
func duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
