package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

const (
	showReleases = 5
	showFiles    = 50
)

// serviceDetails is everything show reports about a service on one server.
type serviceDetails struct {
	deploy.ServiceStatus
	Releases []deploy.Record `json:"releases"`
	Files    []deploy.File   `json:"files"`
	// FileCount is the number of deployed files; Files holds the first ones.
	FileCount int      `json:"fileCount"`
	Errors    []string `json:"errors,omitempty"`
}

func newShowCmd(g *globalFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "show <service>",
		Aliases: []string{"inspect"},
		Short:   "Show a service's containers, recent deployments and files",
		Long: `Show everything about one service on each of its servers: the live
release, its containers with health, uptime, images and published ports,
volumes, recent deployments and the deployed files.
Exits non-zero if the service isn't running everywhere it's configured.`,
		Example: `  rollout show web
  rollout show web --json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeServices(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			svc, ok := cfg.Services[args[0]]
			if !ok {
				return fmt.Errorf("unknown service %q", args[0])
			}
			ssh, err := g.newSSH()
			if err != nil {
				return err
			}
			defer ssh.Close()

			details := make([]serviceDetails, len(svc.Servers))
			var (
				wg sync.WaitGroup
				mu sync.Mutex // guards Errors
			)
			var rows []deploy.ServiceStatus
			wg.Go(func() { rows = deploy.Status(cfg, ssh, args) })
			for i, name := range svc.Servers {
				srv, d := cfg.Servers[name], &details[i]
				wg.Go(func() {
					hs, err := deploy.History(ssh, srv, args, showReleases)
					if err != nil {
						mu.Lock()
						d.Errors = append(d.Errors, "history: "+err.Error())
						mu.Unlock()
					} else if len(hs) > 0 {
						d.Releases = hs[0].Releases
					}
				})
				wg.Go(func() {
					files, n, err := deploy.Files(ssh, srv, svc, showFiles)
					if err != nil {
						mu.Lock()
						d.Errors = append(d.Errors, "files: "+err.Error())
						mu.Unlock()
					}
					d.Files, d.FileCount = files, n
				})
			}
			wg.Wait()
			for i, name := range svc.Servers {
				for _, r := range rows {
					if r.Server == name {
						details[i].ServiceStatus = r
					}
				}
			}

			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(details); err != nil {
					return err
				}
			} else {
				p := g.presentation(cfg, out)
				for i, d := range details {
					if i > 0 {
						fmt.Fprintln(out)
					}
					printService(out, d, cfg.Servers[d.Server], svc, p)
				}
			}
			return statusError(rows)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func printService(out io.Writer, d serviceDetails, srv *config.Server, svc *config.Service, p presentation) {
	title := p.stack(d.Service) + " on " + p.server(d.Server, d.HostOS, d.HostDistro) + "  " + p.state(d.State)
	if len(d.Containers) > 0 {
		title += p.dim(fmt.Sprintf("  %d/%d containers up", d.Running(), len(d.Containers)))
	}
	fmt.Fprintln(out, title)
	for _, e := range append([]string{d.Error}, d.Errors...) {
		if e != "" {
			fmt.Fprintln(out, p.paint("31", e))
		}
	}
	fmt.Fprintln(out)

	dir := srv.DeployPath + "/" + d.Service
	info := &table{}
	field := func(k, v string) { info.row("  "+p.dim(k), v) }
	if rec := d.Record; rec != nil {
		if rec.Version != "" {
			field("Release", p.paint("1", rec.Version))
		}
		if rec.Message != "" {
			field("Message", rec.Subject())
		}
		field("Deployed", rec.DeployedAt.Local().Format("2006-01-02 15:04")+" "+p.dim("("+ago(rec.DeployedAt)+")")+" by "+rec.DeployedBy)
		if rec.Commit != "" {
			commit := rec.Commit
			if rec.Dirty {
				commit += p.paint("33", " with uncommitted changes")
			}
			field("Commit", commit)
		}
		field("Bundle", deploy.ShortHash(rec.Hash)+p.dim(" · rollout "+rec.Rollout))
		if rec.Source != "" {
			field("CI run", rec.Source)
		}
		if rec.ImportedFrom != "" {
			dir = rec.ImportedFrom + p.dim(" (imported; redeploy to move it to "+dir+")")
		}
	} else {
		field("Deployed", p.dim("no deploy record"))
	}
	field("Directory", dir)
	info.print(out)

	if len(d.Containers) > 0 {
		fmt.Fprintln(out)
		p.heading(out, "Containers")
		containerTable(d.Containers, p).print(out)
		if t := mountTable(d.Containers, srv.DeployPath+"/"+d.Service, p); t != nil {
			fmt.Fprintln(out)
			p.heading(out, "Volumes")
			t.print(out)
		}
	}

	fmt.Fprintln(out)
	p.heading(out, "Recent deployments")
	printReleases(out, deploy.ServiceHistory{Current: d.Record, Releases: d.Releases}, showReleases, p)

	fmt.Fprintln(out)
	p.heading(out, "Files "+p.dim(fmt.Sprintf("(%d)", d.FileCount)))
	if len(d.Files) == 0 {
		fmt.Fprintln(out, p.dim("  none in "+srv.DeployPath+"/"+d.Service))
		return
	}
	files := p.table("PATH", "SIZE")
	for _, f := range d.Files {
		files.row(f.Path, p.dim(humanBytes(f.Size)))
	}
	if more := d.FileCount - len(d.Files); more > 0 {
		files.row(p.dim(fmt.Sprintf("… %d more", more)), "")
	}
	files.print(out)
	if len(svc.Preserve) > 0 {
		fmt.Fprintln(out, p.fit(p.dim("Not listed (preserved data): "+strings.Join(svc.Preserve, ", "))))
	}
}

// anonymous reports whether a volume name is a Docker-generated ID.
func anonymous(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, r := range name {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func containerTable(cs []deploy.Container, p presentation) *table {
	names := map[string]int{}
	for _, c := range cs {
		names[c.Service]++
	}
	t := p.table("CONTAINER", "STATE", "UP", "RESTARTS", "IMAGE", "PORTS")
	for _, c := range cs {
		// The compose service is shorter; replicas need the container name.
		name := c.Service
		if name == "" || names[name] > 1 {
			name = c.Name
		}
		up := "-"
		if c.State == "running" && !c.StartedAt.IsZero() {
			up = duration(time.Since(c.StartedAt))
		}
		restarts := strconv.Itoa(c.RestartCount)
		if c.RestartCount > 0 {
			restarts = p.paint("33", restarts)
		} else {
			restarts = p.dim(restarts)
		}
		t.row(name, p.container(c), up, restarts, c.Image, ports(c.Ports, p))
	}
	return t
}

// container is the state of one container, preferring its health check.
func (p presentation) container(c deploy.Container) string {
	ok := func(s string) string { return p.paint("32", p.icon("", s)) }
	warn := func(s string) string { return p.paint("33", p.icon("", s)) }
	bad := func(s string) string { return p.paint("31", p.icon("", s)) }
	switch {
	case c.State == "running" && c.Health == "unhealthy":
		return bad("unhealthy")
	case c.State == "running" && c.Health == "starting":
		return warn("starting")
	case c.State == "running" && c.Health == "healthy":
		return ok("healthy")
	case c.State == "running":
		return ok("running")
	case c.State == "exited":
		// Status is like "Exited (0) 2 hours ago".
		code := "?"
		if _, rest, found := strings.Cut(c.Status, "("); found {
			code, _, _ = strings.Cut(rest, ")")
		}
		if code == "0" {
			return p.dim(p.icon("", "exited (0)"))
		}
		return bad("exited (" + code + ")")
	}
	return warn(c.State)
}

// ports lists published ports as host→container; IPv4 and IPv6 wildcard
// bindings of the same port are shown once.
func ports(ps []deploy.Port, p presentation) string {
	var out []string
	seen := map[string]bool{}
	for _, port := range ps {
		if port.HostPort == "" {
			continue
		}
		s := port.HostPort + "→" + strings.TrimSuffix(port.Container, "/tcp")
		if port.HostIP != "" && port.HostIP != "0.0.0.0" && port.HostIP != "::" {
			s = port.HostIP + ":" + s
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return p.dim("-")
	}
	return strings.Join(out, ", ")
}

func mountTable(cs []deploy.Container, dir string, p presentation) *table {
	t := p.table("CONTAINER", "SOURCE", "MOUNTED AT", "MODE")
	n := 0
	for _, c := range cs {
		for _, m := range c.Mounts {
			src := m.Source
			switch m.Type {
			case "volume":
				src = m.Name + p.dim(" (volume)")
				if anonymous(m.Name) {
					src = p.dim("anonymous volume")
				}
			case "bind":
				if rel, ok := strings.CutPrefix(m.Source, dir+"/"); ok {
					src = "./" + path.Clean(rel)
				}
			case "tmpfs":
				src = p.dim("tmpfs")
			}
			mode := p.dim("rw")
			if !m.RW {
				mode = "ro"
			}
			t.row(c.Service, src, m.Destination, mode)
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return t
}

func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "-"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}
