// Package compose reads the parts of a compose file the dashboard draws:
// services, their images, ports, volumes, networks and dependencies. It is
// not a full compose implementation (no interpolation, extends or include).
package compose

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is a compose file's topology. Network and volume names are the
// names Docker uses (<project>_<key> unless external or renamed), so they can
// be matched with live containers.
type Project struct {
	Services []Service `json:"services"`
	Volumes  []string  `json:"volumes"`
	Networks []string  `json:"networks"`
}

type Service struct {
	Name        string   `json:"name"`
	Image       string   `json:"image,omitempty"`
	Build       bool     `json:"build,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Volumes     []Volume `json:"volumes,omitempty"`
	Networks    []string `json:"networks,omitempty"`
	DependsOn   []string `json:"dependsOn,omitempty"`
	Healthcheck bool     `json:"healthcheck,omitempty"`
	Restart     string   `json:"restart,omitempty"`
	Replicas    int      `json:"replicas,omitempty"`
}

type Volume struct {
	Type   string `json:"type"` // volume, bind, tmpfs
	Source string `json:"source,omitempty"`
	Target string `json:"target"`
}

// Load parses the compose file at path; project is the compose project name.
func Load(path, project string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Services map[string]struct {
			Image       string    `yaml:"image"`
			Build       yaml.Node `yaml:"build"`
			Ports       []yaml.Node
			Volumes     []yaml.Node
			Networks    yaml.Node
			DependsOn   yaml.Node `yaml:"depends_on"`
			Healthcheck *struct {
				Disable bool `yaml:"disable"`
			}
			Restart string
			Deploy  struct {
				Replicas int
			}
		}
		Volumes  map[string]*resource
		Networks map[string]*resource
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	netName := func(key string) string { return dockerName(project, key, raw.Networks[key]) }
	volName := func(key string) string { return dockerName(project, key, raw.Volumes[key]) }
	p := &Project{}
	for _, k := range sortedKeys(raw.Volumes) {
		p.Volumes = append(p.Volumes, volName(k))
	}
	for _, k := range sortedKeys(raw.Networks) {
		p.Networks = append(p.Networks, netName(k))
	}
	for name, s := range raw.Services {
		svc := Service{
			Name:        name,
			Image:       s.Image,
			Build:       !s.Build.IsZero(),
			Networks:    mapNames(keysOrList(s.Networks), netName),
			DependsOn:   keysOrList(s.DependsOn),
			Healthcheck: s.Healthcheck != nil && !s.Healthcheck.Disable,
			Restart:     s.Restart,
			Replicas:    s.Deploy.Replicas,
		}
		for _, n := range s.Ports {
			svc.Ports = append(svc.Ports, port(n))
		}
		for _, n := range s.Volumes {
			v := volume(n)
			if v.Type == "volume" && v.Source != "" {
				v.Source = volName(v.Source)
			}
			svc.Volumes = append(svc.Volumes, v)
		}
		if len(svc.Networks) == 0 {
			svc.Networks = []string{netName("default")}
		}
		p.Services = append(p.Services, svc)
	}
	sort.Slice(p.Services, func(i, j int) bool { return p.Services[i].Name < p.Services[j].Name })
	return p, nil
}

// port renders short ("8080:80") and long ({target: 80, published: 8080}) syntax.
func port(n yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	var long struct {
		Target    string
		Published string
		HostIP    string `yaml:"host_ip"`
		Protocol  string
	}
	n.Decode(&long)
	s := long.Target
	if long.Published != "" {
		s = long.Published + ":" + s
	}
	if long.HostIP != "" {
		s = long.HostIP + ":" + s
	}
	if long.Protocol != "" && long.Protocol != "tcp" {
		s += "/" + long.Protocol
	}
	return s
}

// volume parses short ("data:/data:ro", "./conf:/etc/conf") and long syntax.
func volume(n yaml.Node) Volume {
	if n.Kind != yaml.ScalarNode {
		var long Volume
		n.Decode(&long)
		return long
	}
	parts := strings.Split(n.Value, ":")
	if len(parts) == 1 {
		return Volume{Type: "volume", Target: parts[0]} // anonymous
	}
	v := Volume{Source: parts[0], Target: parts[1], Type: "volume"}
	if strings.HasPrefix(v.Source, ".") || strings.HasPrefix(v.Source, "/") || strings.HasPrefix(v.Source, "~") {
		v.Type = "bind"
	}
	return v
}

// keysOrList reads fields that accept a list or a map (networks, depends_on).
func keysOrList(n yaml.Node) []string {
	var out []string
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	}
	sort.Strings(out)
	return out
}

// resource is a top-level network or volume definition.
type resource struct {
	Name     string
	External yaml.Node
}

// dockerName mirrors compose's naming: an explicit name, the key itself for
// external resources, else <project>_<key>.
func dockerName(project, key string, r *resource) string {
	if r != nil {
		if r.Name != "" {
			return r.Name
		}
		var ext bool
		if r.External.Kind == yaml.MappingNode || (r.External.Decode(&ext) == nil && ext) {
			return key
		}
	}
	return project + "_" + key
}

func mapNames(keys []string, f func(string) string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = f(k)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
