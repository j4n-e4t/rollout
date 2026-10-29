package cli

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/dashboard"
)

func newDashboardCmd(g *globalFlags) *cobra.Command {
	var addr string
	var noOpen bool
	cmd := &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"ui"},
		Short:   "Open a local web dashboard of servers, services and their live state",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Fail early on a broken config rather than in the browser.
			if _, err := config.Load(g.configPath); err != nil {
				return err
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			defer ln.Close()
			token, err := dashboard.SessionToken()
			if err != nil {
				return err
			}
			authority := ln.Addr().String()
			url := "http://" + authority + "/?token=" + token
			fmt.Fprintf(cmd.OutOrStdout(), "rollout dashboard on %s (ctrl-c to stop)\n", url)
			if !noOpen {
				openBrowser(url)
			}
			return http.Serve(ln, dashboard.Handler(g.configPath, ssh, authority, token))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7878", "address to listen on")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "don't open a browser")
	return cmd
}

func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}
