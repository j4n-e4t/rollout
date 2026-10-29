package deploy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// Service states reported by Status.
const (
	StateRunning     = "running"
	StateStarting    = "starting"
	StateUnhealthy   = "unhealthy"
	StateDegraded    = "degraded"
	StateStopped     = "stopped"
	StateNotDeployed = "not deployed"
	StateUnreachable = "unreachable"
)

type Container struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	// Dir is the working directory of the container's compose project.
	Dir    string `json:"dir,omitempty"`
	State  string `json:"state"`
	Status string `json:"status"`

	// Details from docker inspect (empty if inspect failed).
	Health       string    `json:"health,omitempty"` // healthy, unhealthy, starting
	StartedAt    time.Time `json:"startedAt,omitzero"`
	RestartCount int       `json:"restartCount"`
	ImageID      string    `json:"imageId,omitempty"`
	DependsOn    []string  `json:"dependsOn,omitempty"` // compose service names
	Networks     []Network `json:"networks,omitempty"`
	Ports        []Port    `json:"ports,omitempty"`
	Mounts       []Mount   `json:"mounts,omitempty"`
}

type Network struct {
	Name    string   `json:"name"`
	IP      string   `json:"ip,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// Port is a container port, published on the host if HostPort is set.
type Port struct {
	Container string `json:"container"` // e.g. "80/tcp"
	HostIP    string `json:"hostIp,omitempty"`
	HostPort  string `json:"hostPort,omitempty"`
}

type Mount struct {
	Type        string `json:"type"` // volume, bind, tmpfs
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

// ServiceStatus is the live state of one service on one server.
type ServiceStatus struct {
	Service    string `json:"service"`
	Server     string `json:"server"`
	HostOS     string `json:"hostOS,omitempty"`
	HostDistro string `json:"hostDistro,omitempty"`
	State      string `json:"state"`
	// InConfig is false for services found on the server that the
	// environment file no longer assigns to it.
	InConfig   bool        `json:"inConfig"`
	Containers []Container `json:"containers"`
	// Record is the live release written by the last successful deploy.
	Record *Record `json:"record,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// Running counts containers that are up.
func (s ServiceStatus) Running() int {
	n := 0
	for _, c := range s.Containers {
		if c.State == "running" {
			n++
		}
	}
	return n
}

// Images returns the distinct images used by the service's containers.
func (s ServiceStatus) Images() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range s.Containers {
		if !seen[c.Image] {
			seen[c.Image] = true
			out = append(out, c.Image)
		}
	}
	sort.Strings(out)
	return out
}

// Status queries every relevant server in parallel. With a non-empty filter,
// only those services (and the servers they're configured on) are reported.
func Status(cfg *config.Config, ssh *remote.SSH, filter []string) []ServiceStatus {
	want := map[string]bool{}
	for _, f := range filter {
		want[f] = true
	}
	var servers []*config.Server
	for _, name := range cfg.ServerNames() {
		if len(want) == 0 || serverHasAny(cfg, name, want) {
			servers = append(servers, cfg.Servers[name])
		}
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		all []ServiceStatus
	)
	for _, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows := serverStatus(cfg, ssh, srv)
			mu.Lock()
			defer mu.Unlock()
			for _, r := range rows {
				if len(want) == 0 || want[r.Service] {
					all = append(all, r)
				}
			}
		}()
	}
	wg.Wait()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Service != all[j].Service {
			return all[i].Service < all[j].Service
		}
		return all[i].Server < all[j].Server
	})
	return all
}

func serverStatus(cfg *config.Config, ssh *remote.SSH, srv *config.Server) []ServiceStatus {
	configured := map[string]bool{}
	for _, name := range cfg.ServiceNames() {
		for _, s := range cfg.Services[name].Servers {
			if s == srv.Name {
				configured[name] = true
			}
		}
	}

	var services []*config.Service
	for _, name := range cfg.ServiceNames() {
		if configured[name] {
			services = append(services, cfg.Services[name])
		}
	}
	out, err := ssh.Output(Target(srv), statusCommand(srv.DeployPath, srv.StateDir, services...))
	if err != nil {
		var rows []ServiceStatus
		for name := range configured {
			rows = append(rows, ServiceStatus{Service: name, Server: srv.Name, State: StateUnreachable, InConfig: true, Error: err.Error()})
		}
		return rows
	}
	dirs, containers, records := parseStatus(out, srv.DeployPath, configured)
	desired, incomplete := parseDesired(out)

	names := map[string]bool{}
	for n := range configured {
		names[n] = true
	}
	for n := range dirs {
		names[n] = true
	}
	for n := range containers {
		names[n] = true
	}
	for n := range records {
		names[n] = true
	}
	rows := make([]ServiceStatus, 0, len(names))
	hostOS, hostDistro := parseHostInfo(string(out))
	for n := range names {
		state := serviceState(dirs[n], containers[n])
		var problem string
		if incomplete[n] {
			state, problem = StateDegraded, "incomplete deployment; recovery required"
			delete(records, n)
		} else if configured[n] && state == StateRunning {
			if !desiredComplete(desired[n], containers[n]) {
				state, problem = StateDegraded, "missing expected containers or unable to resolve live compose configuration"
			}
		}
		rows = append(rows, ServiceStatus{
			Error:      problem,
			Service:    n,
			Server:     srv.Name,
			HostOS:     hostOS,
			HostDistro: hostDistro,
			State:      state,
			InConfig:   configured[n],
			Containers: containers[n],
			Record:     records[n],
		})
	}
	return rows
}

// statusCommand prints the deployed service directories, their release
// records, and the containers of compose projects: those under the deploy
// path, and those named like a service (e.g. imported, running elsewhere).
func statusCommand(base, state string, services ...*config.Service) string {
	script := `set -eu
base=` + shellQuote(base) + `
state=` + shellQuote(state) + `
` + hostProbe + `echo "@dirs"
if [ -d "$base" ]; then
  for d in "$base"/*/; do [ -d "$d" ] && basename "$d"; done
fi
echo "@records"
for f in "$state"/*.json; do
  [ -f "$f" ] || continue
  printf '%s\t' "$(basename "$f" .json)"; tr -d '\n' < "$f"; echo
done
echo "@containers"
docker ps -a --filter label=com.docker.compose.project --format '{{.Label "com.docker.compose.project"}}	{{.Label "com.docker.compose.project.working_dir"}}	{{.Label "com.docker.compose.service"}}	{{.Names}}	{{.Image}}	{{.State}}	{{.Status}}'
echo "@inspect"
ids=$(docker ps -aq --filter label=com.docker.compose.project)
# Selected fields only: never the environment, which may hold secrets.
[ -z "$ids" ] || docker inspect --format '{{json .Name}}	{{json .State.StartedAt}}	{{json .State.Health}}	{{json .RestartCount}}	{{json .Image}}	{{json .Config.Labels}}	{{json .NetworkSettings.Networks}}	{{json .NetworkSettings.Ports}}	{{json .Mounts}}' $ids || true
`
	script += "echo @pending\n"
	script += `for f in "$state"/*.pending; do
  [ -f "$f" ] || continue
  basename "$f" .pending
done
`
	script += "echo @desired\n"
	for _, svc := range services {
		// In a subshell: composeAt changes directory and "$@".
		script += "desired=$(\n" + composeAt(base+"/"+svc.Name, svc.Name, svc.ComposeFile, "exit 3") +
			"docker compose -p " + shellQuote(svc.Name) + ` "$@" config --format json 2>/dev/null) && rc=0 || rc=$?` + "\n"
		script += "[ \"$rc\" = 3 ] || { [ \"$rc\" = 0 ] || desired='!'; printf '%s\\t' " + shellQuote(svc.Name) + "; printf '%s' \"$desired\" | tr -d '\\n'; echo; }\n"
	}

	return "sh -c " + shellQuote(script)
}

// parseStatus reads statusCommand output into per-service dirs, containers
// and release records.
// Containers belong to a service if their project's working directory is
// <base>/<service>, or if the project is named like a configured service or
// one with a record (e.g. imported from another directory).
func parseStatus(out []byte, base string, configured map[string]bool) (map[string]bool, map[string][]Container, map[string]*Record) {
	dirs := map[string]bool{}
	containers := map[string][]Container{}
	records := map[string]*Record{}
	details := map[string]inspect{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // records can be long lines
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "@") {
			section = line
			continue
		}
		switch section {
		case "@dirs":
			if line != "" {
				dirs[line] = true
			}
		case "@records":
			name, data, ok := strings.Cut(line, "\t")
			var r Record
			if ok && json.Unmarshal([]byte(data), &r) == nil {
				records[name] = &r
			}
		case "@inspect":
			if d, ok := parseInspect(line); ok {
				details[d.name] = d
			}
		case "@containers":
			f := strings.Split(line, "\t")
			if len(f) != 7 {
				continue
			}
			project := f[0]
			dir, name := path.Split(f[1])
			if path.Clean(dir) != path.Clean(base) || name == "" {
				if !configured[project] && records[project] == nil {
					continue
				}
				name = project
			}
			containers[name] = append(containers[name], Container{
				Service: f[2], Name: f[3], Image: f[4], Dir: f[1], State: f[5], Status: f[6],
			})
		}
	}
	for _, cs := range containers {
		for i := range cs {
			if d, ok := details[cs[i].Name]; ok {
				d.apply(&cs[i])
			}
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	}
	return dirs, containers, records
}

func serviceState(deployed bool, cs []Container) string {
	if len(cs) == 0 {
		if deployed {
			return StateStopped
		}
		return StateNotDeployed
	}
	ok, running, starting, unhealthy := 0, 0, false, false
	for _, c := range cs {
		if containerOK(c) {
			ok++
		}
		if c.State == "running" {
			running++
		}
		starting = starting || strings.Contains(c.Status, "health: starting")
		unhealthy = unhealthy || strings.Contains(c.Status, "(unhealthy)")
	}
	switch {
	case running == 0:
		// Even if everything exited cleanly: nothing is serving.
		return StateStopped
	case unhealthy:
		return StateUnhealthy
	case ok < len(cs):
		return StateDegraded
	case starting:
		return StateStarting
	}
	return StateRunning
}

// containerOK is true for running containers and one-shot containers that
// exited cleanly (e.g. migrations next to a running app).
func containerOK(c Container) bool {
	return c.State == "running" || (c.State == "exited" && strings.HasPrefix(c.Status, "Exited (0)"))
}

func serverHasAny(cfg *config.Config, server string, services map[string]bool) bool {
	for name := range services {
		if svc, ok := cfg.Services[name]; ok {
			for _, s := range svc.Servers {
				if s == server {
					return true
				}
			}
		}
	}
	return false
}

// inspect holds the docker inspect fields printed by statusCommand.
type inspect struct {
	name      string
	startedAt time.Time
	health    *struct{ Status string }
	restarts  int
	image     string
	labels    map[string]string
	networks  map[string]*struct {
		IPAddress string
		Aliases   []string
	}
	ports  map[string][]struct{ HostIp, HostPort string }
	mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

func parseInspect(line string) (inspect, bool) {
	f := strings.Split(line, "\t")
	if len(f) != 9 {
		return inspect{}, false
	}
	var d inspect
	targets := []any{&d.name, &d.startedAt, &d.health, &d.restarts, &d.image, &d.labels, &d.networks, &d.ports, &d.mounts}
	for i, t := range targets {
		if err := json.Unmarshal([]byte(f[i]), t); err != nil {
			return inspect{}, false
		}
	}
	d.name = strings.TrimPrefix(d.name, "/")
	return d, true
}

func (d inspect) apply(c *Container) {
	if d.health != nil {
		c.Health = d.health.Status
	}
	if d.startedAt.Year() > 1 {
		c.StartedAt = d.startedAt
	}
	c.RestartCount = d.restarts
	c.ImageID = d.image
	// Compose writes "db:service_healthy:false,cache:service_started:false".
	for _, dep := range strings.Split(d.labels["com.docker.compose.depends_on"], ",") {
		if name, _, _ := strings.Cut(dep, ":"); name != "" {
			c.DependsOn = append(c.DependsOn, name)
		}
	}
	for name, n := range d.networks {
		if n == nil {
			continue
		}
		var aliases []string
		for _, a := range n.Aliases {
			// Compose adds the container name and ID as aliases; keep the rest.
			if a != c.Name && !strings.HasPrefix(c.Name, a) && len(a) != 12 {
				aliases = append(aliases, a)
			}
		}
		c.Networks = append(c.Networks, Network{Name: name, IP: n.IPAddress, Aliases: aliases})
	}
	sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Name < c.Networks[j].Name })
	for port, bindings := range d.ports {
		if len(bindings) == 0 {
			c.Ports = append(c.Ports, Port{Container: port})
		}
		for _, b := range bindings {
			c.Ports = append(c.Ports, Port{Container: port, HostIP: b.HostIp, HostPort: b.HostPort})
		}
	}
	sort.Slice(c.Ports, func(i, j int) bool {
		if c.Ports[i].Container != c.Ports[j].Container {
			return c.Ports[i].Container < c.Ports[j].Container
		}
		return c.Ports[i].HostIP < c.Ports[j].HostIP
	})
	for _, m := range d.mounts {
		c.Mounts = append(c.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
	}
}

// desiredProject uses Docker's resolved live configuration, including includes,
// interpolation and replicas, rather than the dashboard's minimal YAML parser.
type desiredProject struct {
	Services map[string]struct {
		Scale  *int `json:"scale"`
		Deploy struct {
			Replicas *int `json:"replicas"`
		} `json:"deploy"`
	} `json:"services"`
}

func parseDesired(out []byte) (map[string]*desiredProject, map[string]bool) {
	desired := map[string]*desiredProject{}
	pending := map[string]bool{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "@") {
			section = line
			continue
		}
		switch section {
		case "@pending":
			pending[line] = true
		case "@desired":
			name, data, ok := strings.Cut(line, "\t")
			var p desiredProject
			if ok && json.Unmarshal([]byte(data), &p) == nil {
				desired[name] = &p
			}
		}
	}
	return desired, pending
}

func desiredComplete(p *desiredProject, cs []Container) bool {
	if p == nil || len(p.Services) == 0 {
		return false
	}
	counts := map[string]int{}
	for _, c := range cs {
		if containerOK(c) {
			counts[c.Service]++
		}
	}
	for name, svc := range p.Services {
		// Docker's resolved config already excludes inactive profiles. Every
		// service returned here must have its expected number of containers.
		want := 1
		if svc.Deploy.Replicas != nil {
			want = *svc.Deploy.Replicas
		}
		if svc.Scale != nil {
			want = *svc.Scale
		}
		if want < 0 || counts[name] < want {
			return false
		}
	}
	return true
}
