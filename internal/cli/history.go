package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

func newHistoryCmd(g *globalFlags) *cobra.Command {
	var n int
	var server string
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "history [service...]",
		Aliases: []string{"releases"},
		Short:   "List the releases deployed to each server",
		Long: `List the releases deployed to each server, newest first.

Reads the deploy records on the servers: the live release from
.rollout/<service>.json and earlier ones from .rollout/<service>.history.jsonl.
Without arguments, shows every service.`,
		Example: `  rollout history
  rollout history web -n 50
  rollout history web -s vps-2 --json`,
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			if n <= 0 {
				return fmt.Errorf("--limit must be greater than zero")
			}
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			for _, a := range args {
				if _, ok := cfg.Services[a]; !ok {
					return fmt.Errorf("unknown service %q", a)
				}
			}
			if server != "" {
				if _, ok := cfg.Servers[server]; !ok {
					return fmt.Errorf("unknown server %q", server)
				}
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			// Which services to ask each server about, as configured.
			byServer := map[string][]string{}
			services := args
			if len(services) == 0 {
				services = cfg.ServiceNames()
			}
			for _, svc := range services {
				for _, srv := range cfg.Services[svc].Servers {
					if server == "" || srv == server {
						byServer[srv] = append(byServer[srv], svc)
					}
				}
			}
			if len(byServer) == 0 {
				return fmt.Errorf("no such service on %s", server)
			}

			var (
				mu   sync.Mutex
				wg   sync.WaitGroup
				all  = map[string][]deploy.ServiceHistory{}
				errs []error
			)
			for srv, svcs := range byServer {
				wg.Add(1)
				go func() {
					defer wg.Done()
					hs, err := deploy.History(ssh, cfg.Servers[srv], svcs, n)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", srv, err))
						return
					}
					all[srv] = hs
				}()
			}
			wg.Wait()

			// Print grouped by service, then server, in config order.
			var ordered []deploy.ServiceHistory
			for _, svc := range services {
				for _, srv := range cfg.ServerNames() {
					for _, h := range all[srv] {
						if h.Service == svc {
							ordered = append(ordered, h)
						}
					}
				}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(ordered); err != nil {
					return err
				}
			} else {
				printHistory(out, ordered, n, g.presentation(cfg, out))
			}
			return errors.Join(errs...)
		},
	}
	cmd.Flags().IntVarP(&n, "limit", "n", 10, "releases to show per service and server")
	cmd.Flags().StringVarP(&server, "server", "s", "", "only this server")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func printHistory(out io.Writer, hs []deploy.ServiceHistory, limit int, p presentation) {
	for i, h := range hs {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s on %s\n", p.stack(h.Service), p.server(h.Server, h.HostOS, h.HostDistro))
		if len(h.Releases) == 0 {
			fmt.Fprintln(out, "  never deployed")
			continue
		}
		w := &table{}
		p.header(w, "", "RELEASE", "COMMIT", "DEPLOYED", "", "BY", "FILES", "MESSAGE")
		for j, r := range h.Releases {
			marker := ""
			if h.IsCurrent(r) {
				marker = p.paint("32", p.icon("\uf058", "current"))
			}
			commit := "-"
			if len(r.Commit) >= 7 {
				commit = r.Commit[:7]
			}
			// Compare with the release before it (next in the list).
			files := "-" // oldest shown; older ones may exist beyond --limit
			if j+1 < len(h.Releases) {
				switch prev := h.Releases[j+1]; {
				case prev.Hash != r.Hash:
					files = "changed"
				case prev.Secrets != r.Secrets:
					files = "secrets"
				default:
					files = "same"
				}
			} else if len(h.Releases) < limit {
				files = "first deploy"
			}
			if r.ImportedFrom != "" {
				files = "imported"
			}
			w.row("  "+marker, r.Release(), commit,
				r.DeployedAt.Local().Format("2006-01-02 15:04"), "("+ago(r.DeployedAt)+")", r.DeployedBy, files, truncate(r.Subject(), 60))
		}
		w.print(out)
	}
	if len(hs) > 0 {
		fmt.Fprintln(out, "\n"+p.paint("32", p.icon("\uf058", "current"))+" = live release")
	}
}
