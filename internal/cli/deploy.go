package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

type deployFlags struct {
	all         bool
	dryRun      bool
	yes         bool
	force       bool
	release     string
	message     string
	pull        string
	waitTimeout int
}

func newDeployCmd(g *globalFlags) *cobra.Command {
	f := &deployFlags{}
	cmd := &cobra.Command{
		Use:   "deploy [service...]",
		Short: "Push services to their servers and run docker compose up",
		Long: `Push services to their servers and run docker compose up.

Each service goes to the servers listed for it in the environment file.
Without arguments in an interactive terminal, rollout asks which services to
deploy. In CI, pass service names or --all.

Services whose files match the release recorded on a server, and that are
running there, are skipped; --force deploys them anyway. Images are pulled
before the running release is touched.

References like ${DB_PASSWORD} in a service's .env files are filled in from
setec, the environment or ROLLOUT_SECRETS before shipping; see rollout secrets.`,
		Example: `  rollout deploy                  # pick interactively
  rollout deploy web api          # deploy two services
  rollout deploy --all --dry-run  # show what would change
  rollout deploy web --release v1.4 --force
  rollout deploy api -m "Raise worker concurrency to 8"`,
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, g, f, args)
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.all, "all", "a", false, "deploy every service")
	fl.BoolVarP(&f.dryRun, "dry-run", "n", false, "print the plan without deploying")
	fl.BoolVarP(&f.yes, "yes", "y", false, "skip the confirmation prompt")
	fl.BoolVarP(&f.force, "force", "f", false, "deploy services even if unchanged")
	fl.StringVarP(&f.release, "release", "r", "", "name for this release, stored in the deploy record (e.g. v1.4)")
	fl.StringVarP(&f.message, "message", "m", "", "describe this deploy, like a commit message (default: the Git commit subject)")
	fl.StringVar(&f.pull, "pull", "missing", `image pull policy before switching: "missing" or "always"`)
	fl.IntVar(&f.waitTimeout, "wait-timeout", 300, "seconds to wait for containers to become healthy")
	return cmd
}

func runDeploy(cmd *cobra.Command, g *globalFlags, f *deployFlags, args []string) error {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	interactive := isTerminal()
	out := cmd.OutOrStdout()
	p := g.presentation(cfg, out)

	services := args
	switch {
	case f.all && len(args) > 0:
		return errors.New("pass service names or --all, not both")
	case f.all:
		services = cfg.ServiceNames()
	case len(services) == 0 && interactive:
		if services, err = pickServices(cfg, !p.color); err != nil {
			return err
		}
	case len(services) == 0:
		return errors.New("no services given: pass service names or --all")
	}
	if len(services) == 0 {
		return errors.New("nothing selected")
	}

	pull := deploy.PullPolicy(f.pull)
	if pull != deploy.PullMissing && pull != deploy.PullAlways {
		return fmt.Errorf(`--pull must be "missing" or "always", not %q`, f.pull)
	}

	resolver, err := newResolver(cfg)
	if err != nil {
		return err
	}
	steps, err := deploy.Plan(cfg, services, resolver.Lookup)
	// A setec failure explains a "missing" secret better than Plan can.
	if serr := resolver.Err(); serr != nil {
		return serr
	}
	if err != nil {
		return err
	}
	ssh, err := g.newSSH()
	if err != nil {
		return err
	}
	defer ssh.Close()

	deploy.Check(cfg, ssh, steps)
	// An explicit registry refresh must not be short-circuited by file hashes.
	force := f.force || pull == deploy.PullAlways
	pending := printPlan(out, steps, force, p)
	if pending == 0 {
		fmt.Fprintln(out, "Nothing to deploy: everything is up to date (use --force to redeploy).")
		return nil
	}
	if f.dryRun {
		return nil
	}
	if interactive && !f.yes {
		ok := true
		confirm := huh.NewConfirm().Title("Deploy?").Value(&ok)
		confirm.WithAccessible(!p.color)
		if !p.color {
			confirm.WithTheme(&huh.Theme{})
		}
		if err := confirm.Run(); err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	res, err := deploy.Run(cfg, steps, deploy.Options{
		SSH:         ssh,
		WaitTimeout: f.waitTimeout,
		Pull:        pull,
		Force:       force,
		Version:     f.release,
		Message:     f.message,
		Rollout:     Version,
		Out:         out,
	})
	if err != nil {
		return fmt.Errorf("deploy failed:\n%w", err)
	}
	fmt.Fprintf(out, "rollout: %d deployed, %d unchanged\n", res.Deployed, res.Skipped)
	return nil
}

// printPlan shows what will happen per server and returns how many
// service/server pairs will be deployed.
func printPlan(w io.Writer, steps []deploy.Step, force bool, p presentation) int {
	pending := 0
	fmt.Fprintln(w, p.paint("1", "Plan:"))
	for _, s := range steps {
		fmt.Fprintf(w, "  %s (%s)\n", p.server(s.Server.Name, "", ""), p.paint("90", s.Server.Address()))
		tw := &table{}
		for _, it := range s.Items {
			action := "deploy"
			if !it.Deploy(force) {
				action = "skip"
			} else {
				pending++
			}
			detail := ""
			if it.Current != nil {
				detail = "live: " + it.Current.Release() + ", " + ago(it.Current.DeployedAt) + " by " + it.Current.DeployedBy
				if subj := it.Current.Subject(); subj != "" {
					detail += ": " + truncate(subj, 50)
				}
			}
			if it.RunsFrom != "" {
				detail = "running from " + it.RunsFrom + "; deploy takes it over"
			}
			if it.Error != "" {
				detail = it.Error
			}
			code, icon := "36", "\uf0ee"
			if action == "skip" {
				code, icon = "90", "\uf04e"
			}
			if it.Error != "" {
				code = "31"
			}
			tw.row("    "+p.paint(code, p.icon(icon, action)), p.stack(it.Service.Name), string(it.Change), p.paint("90", detail))
		}
		tw.print(w)
	}
	return pending
}

func pickServices(cfg *config.Config, accessible bool) ([]string, error) {
	var opts []huh.Option[string]
	for _, name := range cfg.ServiceNames() {
		label := name + "  -> " + strings.Join(cfg.Services[name].Servers, ", ")
		opts = append(opts, huh.NewOption(label, name))
	}
	var picked []string
	field := huh.NewMultiSelect[string]().
		Title("Services to deploy").
		Description("space to select, enter to confirm").
		Options(opts...).
		Value(&picked)
	field.WithAccessible(accessible)
	if accessible {
		field.WithTheme(&huh.Theme{})
	}
	err := field.Run()
	return picked, err
}
