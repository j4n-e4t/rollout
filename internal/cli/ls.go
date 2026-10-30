package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

func newLsCmd(g *globalFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls [service...]",
		Aliases: []string{"list", "status"},
		Short:   "Show where services run and their live state on each server",
		Long: `Show where services run and their live state on each server.

rollout connects to every server and reports each configured service's
containers. Set output.showUnconfigured to true in rollout.json to also
list services found on servers that the config does not assign to them.
Use "rollout show <service>" for containers, deployments and files.
Exits non-zero if any configured service isn't running.`,
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			for _, a := range args {
				if _, ok := cfg.Services[a]; !ok {
					return fmt.Errorf("unknown service %q", a)
				}
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			rows := deploy.Status(cfg, ssh, args)
			if !cfg.Output.ShowUnconfigured {
				rows = slices.DeleteFunc(rows, func(r deploy.ServiceStatus) bool { return !r.InConfig })
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rows); err != nil {
					return err
				}
			} else if err := printStatus(out, rows, g.presentation(cfg, out)); err != nil {
				return err
			}
			return statusError(rows)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func printStatus(out io.Writer, rows []deploy.ServiceStatus, p presentation) error {
	w := p.table("SERVICE", "SERVER", "STATE", "CONTAINERS", "RELEASE", "DEPLOYED")
	var notes []string
	for _, r := range rows {
		state := p.state(r.State)
		if !r.InConfig {
			state += p.dim(" (not in config)")
		}
		containers := "-"
		if len(r.Containers) > 0 {
			containers = fmt.Sprintf("%d/%d", r.Running(), len(r.Containers))
		}
		release, deployed := "-", "-"
		if r.Record != nil {
			release, deployed = r.Record.Release(), ago(r.Record.DeployedAt)
		}
		w.row(p.stack(r.Service), p.server(r.Server, r.HostOS, r.HostDistro), state, containers, release, p.dim(deployed))
		if r.Error != "" {
			notes = append(notes, fmt.Sprintf("%s: %s", r.Server, r.Error))
		}
	}
	if err := w.print(out); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range notes {
		if !seen[n] {
			seen[n] = true
			fmt.Fprintln(out, "\n"+n)
		}
	}
	return nil
}

func statusError(rows []deploy.ServiceStatus) error {
	bad := 0
	for _, r := range rows {
		if r.InConfig && r.State != deploy.StateRunning {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d service(s) not running as configured", bad)
	}
	return nil
}
