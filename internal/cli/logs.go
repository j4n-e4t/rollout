package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// ExitCode is returned to make rollout exit with a remote command's status
// without printing an error (e.g. after `rollout exec`).
type ExitCode int

func (e ExitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

func newLogsCmd(g *globalFlags) *cobra.Command {
	var follow, timestamps bool
	var tail int
	var server string
	cmd := &cobra.Command{
		Use:   "logs <service> [container...]",
		Short: "Show a service's container logs",
		Long: `Show a service's container logs, via docker compose logs on its servers.

Containers are compose service names from the service's compose file; without
any, all are shown. A service on several servers shows all of them, prefixed
with the server name, unless --server picks one.`,
		Example: `  rollout logs web
  rollout logs web nginx -f
  rollout logs api worker -n 500 -s vps-2`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, svc, err := loadService(g, args[0])
			if err != nil {
				return err
			}
			servers := svc.Servers
			if server != "" {
				if servers, err = pickServer(svc, server, false, !g.presentation(cfg, cmd.OutOrStdout()).color); err != nil {
					return err
				}
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			composeArgs := []string{"logs", "--tail", strconv.Itoa(tail)}
			if !g.presentation(cfg, cmd.OutOrStdout()).color {
				composeArgs = append(composeArgs, "--no-color")
			}
			if follow {
				composeArgs = append(composeArgs, "--follow")
			}
			if timestamps {
				composeArgs = append(composeArgs, "--timestamps")
			}
			composeArgs = append(composeArgs, args[1:]...)

			if len(servers) == 1 {
				srv := cfg.Servers[servers[0]]
				return remoteExit(ssh.Interactive(deploy.Target(srv), deploy.ComposeCommand(srv, svc, composeArgs...), false))
			}
			// Several servers: stream them side by side with a prefix.
			out := deploy.NewSyncWriter(cmd.OutOrStdout())
			var wg sync.WaitGroup
			errs := make([]error, len(servers))
			for i, name := range servers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					srv := cfg.Servers[name]
					w := deploy.NewPrefixWriter("["+name+"] ", out)
					defer w.Flush()
					if err := ssh.Run(deploy.Target(srv), deploy.ComposeCommand(srv, svc, composeArgs...), nil, w, w); err != nil {
						errs[i] = fmt.Errorf("%s: %w", name, err)
					}
				}()
			}
			wg.Wait()
			return errors.Join(errs...)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new log lines")
	cmd.Flags().IntVarP(&tail, "tail", "n", 100, "lines to show from the end of each log")
	cmd.Flags().BoolVarP(&timestamps, "timestamps", "t", false, "show timestamps")
	cmd.Flags().StringVarP(&server, "server", "s", "", "only this server (one of the service's servers)")
	return cmd
}

func newExecCmd(g *globalFlags) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "exec <service> <container> [-- command...]",
		Short: "Run a command or open a shell in a service's container",
		Long: `Run a command in a running container via docker compose exec.

Without a command, opens an interactive shell (bash if available, else sh).
For a service on several servers, pass --server or pick one interactively.`,
		Example: `  rollout exec web nginx
  rollout exec api app -- ./manage.py migrate
  rollout exec db postgres -s vps-2 -- psql -U app`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, svc, err := loadService(g, args[0])
			if err != nil {
				return err
			}
			servers, err := pickServer(svc, server, true, !g.presentation(cfg, cmd.OutOrStdout()).color)
			if err != nil {
				return err
			}
			srv := cfg.Servers[servers[0]]
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			tty := isTerminal()
			command := args[2:]
			if len(command) == 0 {
				command = []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
			}
			composeArgs := []string{"exec"}
			if !tty {
				composeArgs = append(composeArgs, "-T")
			}
			composeArgs = append(composeArgs, args[1])
			composeArgs = append(composeArgs, command...)
			return remoteExit(ssh.Interactive(deploy.Target(srv), deploy.ComposeCommand(srv, svc, composeArgs...), tty))
		},
	}
	cmd.Flags().StringVarP(&server, "server", "s", "", "server to run on (one of the service's servers)")
	return cmd
}

func loadService(g *globalFlags, name string) (*config.Config, *config.Service, error) {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return nil, nil, err
	}
	svc, ok := cfg.Services[name]
	if !ok {
		return nil, nil, fmt.Errorf("unknown service %q", name)
	}
	return cfg, svc, nil
}

// pickServer resolves which of the service's servers to use: the --server
// flag, the only server, or (if prompt and interactive) a choice.
func pickServer(svc *config.Service, flag string, prompt, accessible bool) ([]string, error) {
	if flag != "" {
		for _, s := range svc.Servers {
			if s == flag {
				return []string{flag}, nil
			}
		}
		return nil, fmt.Errorf("service %q doesn't run on %q (servers: %s)", svc.Name, flag, strings.Join(svc.Servers, ", "))
	}
	if len(svc.Servers) == 1 {
		return svc.Servers, nil
	}
	if !prompt || !isTerminal() {
		return nil, fmt.Errorf("service %q runs on %s: pick one with --server", svc.Name, strings.Join(svc.Servers, ", "))
	}
	var picked string
	opts := huh.NewOptions(svc.Servers...)
	field := huh.NewSelect[string]().Title("Server").Options(opts...).Value(&picked)
	field.WithAccessible(accessible)
	if accessible {
		field.WithTheme(&huh.Theme{})
	}
	if err := field.Run(); err != nil {
		return nil, err
	}
	return []string{picked}, nil
}

// remoteExit turns a remote non-zero exit into a silent ExitCode.
func remoteExit(err error) error {
	var exit *remote.ExitError
	if errors.As(err, &exit) {
		return ExitCode(exit.Code)
	}
	return err
}
