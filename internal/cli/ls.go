package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

func newLsCmd(g *globalFlags) *cobra.Command {
	var asJSON, wide bool
	cmd := &cobra.Command{
		Use:     "ls [service...]",
		Aliases: []string{"list", "status"},
		Short:   "Show where services run and their live state on each server",
		Long: `Show where services run and their live state on each server.

rollout connects to every server and reports each configured service's
containers. Set output.showUnconfigured to true in rollout.json to also
list services found on servers that the config does not assign to them.
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
			} else if err := printStatus(out, rows, wide, g.presentation(cfg, out)); err != nil {
				return err
			}
			return statusError(rows)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "show every container")
	return cmd
}

func printStatus(out io.Writer, rows []deploy.ServiceStatus, wide bool, p presentation) error {
	w := &table{}
	p.header(w, "SERVICE", "SERVER", "STATE", "CONTAINERS", "RELEASE", "DEPLOYED")
	var notes []string
	for _, r := range rows {
		state := p.state(r.State)
		if !r.InConfig {
			state += " (not in config)"
		}
		containers := "-"
		if len(r.Containers) > 0 {
			containers = fmt.Sprintf("%d/%d", r.Running(), len(r.Containers))
		}
		release, deployed := "-", "-"
		if r.Record != nil {
			release, deployed = r.Record.Release(), ago(r.Record.DeployedAt)
		}
		w.row(p.stack(r.Service), p.server(r.Server, r.HostOS, r.HostDistro), state, containers, release, p.paint("90", deployed))
		if r.Error != "" {
			notes = append(notes, fmt.Sprintf("%s: %s", r.Server, r.Error))
		}
	}
	if err := w.print(out); err != nil {
		return err
	}
	if wide {
		for _, r := range rows {
			printDetails(out, r, p)
		}
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printDetails prints the full release record and containers of one row.
func printDetails(out io.Writer, r deploy.ServiceStatus, p presentation) {
	fmt.Fprintf(out, "\n%s on %s\n", p.stack(r.Service), p.server(r.Server, r.HostOS, r.HostDistro))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if rec := r.Record; rec != nil {
		commit := orDash(rec.Commit)
		if rec.Dirty {
			commit += " (uncommitted changes)"
		}
		fmt.Fprintf(w, "  release\t%s\n", orDash(rec.Version))
		fmt.Fprintf(w, "  message\t%s\n", orDash(strings.ReplaceAll(rec.Message, "\n", "\n\t")))
		fmt.Fprintf(w, "  commit\t%s\n", commit)
		fmt.Fprintf(w, "  hash\t%s\n", orDash(rec.Hash))
		fmt.Fprintf(w, "  deployed\t%s by %s (rollout %s)\n", rec.DeployedAt.Local().Format("2006-01-02 15:04:05"), rec.DeployedBy, rec.Rollout)
		if rec.Source != "" {
			fmt.Fprintf(w, "  source\t%s\n", rec.Source)
		}
		if rec.ImportedFrom != "" {
			fmt.Fprintf(w, "  imported\tfrom %s (redeploy to move it into rollout's deploy path)\n", rec.ImportedFrom)
		}
	} else {
		fmt.Fprintf(w, "  release\tno deploy record\n")
	}
	ids := map[string]string{}
	if r.Record != nil {
		for _, img := range r.Record.Images {
			ids[img.Container] = img.ID
		}
	}
	for _, c := range r.Containers {
		img := c.Image
		if id := ids[c.Name]; id != "" {
			img += " (" + deploy.ShortHash(id) + ")"
		}
		fmt.Fprintf(w, "  container\t%s\t%s\t%s\t%s\n", c.Name, c.State, c.Status, img)
	}
	w.Flush()
}
