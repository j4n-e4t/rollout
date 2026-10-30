package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/secrets"
)

type secretRow struct {
	Service string `json:"service"`
	secrets.Ref
	Source string `json:"source,omitempty"`
}

// newResolver takes the names mapped under "secrets" in rollout.json from
// setec, and other names from the environment, then ROLLOUT_SECRETS.
func newResolver(cfg *config.Config) (*secrets.Resolver, error) {
	refs := map[string]secrets.SetecRef{}
	for name, s := range cfg.Secrets {
		refs[name] = secrets.SetecRef{Name: s.Setec, Optional: s.Optional}
	}
	return secrets.NewResolver(&secrets.Setec{Server: cfg.SetecServer()}, refs)
}

func newSecretsCmd(g *globalFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "secrets [service...]",
		Short: "List the secrets services reference in their .env files",
		Long: `List the secrets services reference in their .env files, and whether each
is set where rollout runs.

Commit .env files (or any *.env) with references instead of values:

  DB_PASSWORD=${DB_PASSWORD}
  DATABASE_URL=postgres://app:${DB_PASSWORD}@db/app
  LOG_LEVEL=${LOG_LEVEL:-info}

rollout deploy takes names mapped in rollout.json from setec only:

  "setec": { "server": "https://setec.example.ts.net" },
  "secrets": { "DB_PASSWORD": { "setec": "prod/api/db-password" } }

Other names come from environment variables, then from the JSON object in
ROLLOUT_SECRETS. In GitHub Actions, pass the repository secrets to the action
with secrets: ${{ toJSON(secrets) }}. Exits non-zero if a required secret is
missing or cannot be fetched from setec.`,
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			services := args
			if len(services) == 0 {
				services = cfg.ServiceNames()
			}
			res, err := newResolver(cfg)
			if err != nil {
				return err
			}
			var rows []secretRow
			missing := 0
			for _, name := range services {
				svc, ok := cfg.Services[name]
				if !ok {
					return fmt.Errorf("unknown service %q", name)
				}
				r, err := secrets.Render(cfg.ServiceDir(svc), name, res.Lookup)
				if err != nil {
					return fmt.Errorf("service %q: %w", name, err)
				}
				missing += len(r.Missing())
				for _, ref := range r.Refs {
					rows = append(rows, secretRow{Service: name, Ref: ref, Source: res.Source(ref.Name)})
				}
			}

			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if rows == nil {
					rows = []secretRow{}
				}
				if err := enc.Encode(rows); err != nil {
					return err
				}
			} else if len(rows) == 0 {
				fmt.Fprintln(out, "No secret references in .env files.")
			} else {
				w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "SERVICE\tSECRET\tSTATUS\tSOURCE\tWHERE")
				for _, r := range rows {
					status, source := "set", r.Source
					switch {
					case !r.Set && r.HasDefault:
						status = "default"
					case !r.Set:
						status = "MISSING"
					}
					if source == "" {
						source = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s:%d\n", r.Service, r.Name, status, source, r.File, r.Line)
				}
				w.Flush()
			}
			if err := res.Err(); err != nil {
				return err
			}
			if missing > 0 {
				return fmt.Errorf("%d required secret(s) not set", missing)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}
