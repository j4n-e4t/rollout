package cli

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

func newImportCmd(g *globalFlags) *cobra.Command {
	var all, dryRun, force bool
	var message string
	cmd := &cobra.Command{
		Use:   "import [service...]",
		Short: "Adopt compose projects that already run on the servers",
		Long: `Adopt compose projects that already run on the servers.

For each service and its configured servers, rollout looks for a running
compose project with the service's name, wherever it was started from (for
example /root/paperless-ngx for a service named paperless-ngx). It writes a
deploy record marked "imported" into the state directory. Containers and
files are not touched.

Afterwards ls, history and the dashboard show the service as deployed. The
next deploy sees it as changed and moves it into <deployPath>/<service>,
recreating the containers under the same project name, so named volumes,
networks and absolute bind mounts are kept. The old directory is left as is.`,
		Example: `  rollout import paperless-ngx overleaf --dry-run
  rollout import --all`,
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			services := args
			switch {
			case all && len(args) > 0:
				return errors.New("pass service names or --all, not both")
			case all:
				services = cfg.ServiceNames()
			case len(args) == 0:
				return errors.New("no services given: pass service names or --all")
			}
			for _, s := range services {
				if _, ok := cfg.Services[s]; !ok {
					return fmt.Errorf("unknown service %q", s)
				}
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			out := cmd.OutOrStdout()
			var errs []error
			imported := 0
			for _, name := range services {
				svc := cfg.Services[name]
				for _, srvName := range svc.Servers {
					srv := cfg.Servers[srvName]
					where := name + " on " + srvName
					fail := func(err error) {
						fmt.Fprintf(out, "%s: %v\n", where, err)
						errs = append(errs, fmt.Errorf("%s: %w", where, err))
					}
					p, err := deploy.FindProject(ssh, srv, svc)
					switch {
					case err != nil:
						fail(err)
						continue
					case p == nil || p.Containers == 0:
						fail(fmt.Errorf("no compose project named %q is running", name))
						continue
					case p.HasRecord && !force:
						fail(deploy.ErrHasRecord)
						continue
					}
					files := make([]string, len(p.Files))
					for i, f := range p.Files {
						files[i] = strings.TrimPrefix(strings.TrimPrefix(f, p.Dir), "/")
					}
					noun := "containers"
					if p.Containers == 1 {
						noun = "container"
					}
					fmt.Fprintf(out, "%s: project in %s (%d %s, %s)\n", where, p.Dir, p.Containers, noun, strings.Join(files, ", "))
					live := path.Join(srv.DeployPath, name)
					if path.Clean(p.Dir) != live {
						fmt.Fprintf(out, "  the next deploy moves its files to %s\n", live)
					}
					if dryRun {
						continue
					}
					if _, err := deploy.Import(ssh, srv, svc, p, message, Version, force); err != nil {
						fail(err)
						continue
					}
					imported++
					fmt.Fprintf(out, "  imported: record written to %s/%s.json\n", srv.StateDir, name)
				}
			}
			if !dryRun {
				fmt.Fprintf(out, "rollout: %d imported\n", imported)
			}
			return errors.Join(errs...)
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "import every service")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would be imported")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "replace an existing deploy record")
	cmd.Flags().StringVarP(&message, "message", "m", "", `record message (default: "Imported from <dir>")`)
	return cmd
}
