// Package config loads and validates the rollout environment file
// (rollout.json), which describes servers and the services deployed to them.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultFile       = "rollout.json"
	DefaultDeployPath = "/opt/rollout"
	DefaultCompose    = "compose.yaml"
)

// Config is the parsed environment file.
type Config struct {
	Hooks    Hooks               `json:"hooks,omitempty"`
	Output   Output              `json:"output,omitempty"`
	Defaults Defaults            `json:"defaults"`
	Servers  map[string]*Server  `json:"servers"`
	Services map[string]*Service `json:"services"`

	// Dir is the directory containing the config file. Service paths are
	// resolved relative to it.
	Dir string `json:"-"`
}

// Hooks are POSIX shell commands run remotely around an actual deployment.
type Hooks struct {
	BeforeDeploy string `json:"beforeDeploy,omitempty"`
	AfterDeploy  string `json:"afterDeploy,omitempty"`
}

// Output controls CLI styling and which stacks are visible. Nil styling switches follow the terminal.
type Output struct {
	Plain bool  `json:"plain,omitempty"`
	Color *bool `json:"color,omitempty"`
	Icons *bool `json:"icons,omitempty"`
	// ShowUnconfigured includes services found on servers that are not assigned
	// there in rollout.json, in ls output and the dashboard.
	ShowUnconfigured bool `json:"showUnconfigured,omitempty"`
}

// Defaults apply to every server unless the server overrides them. An empty
// user or port leaves the choice to ssh and ~/.ssh/config.
type Defaults struct {
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	DeployPath string `json:"deployPath,omitempty"`
	StateDir   string `json:"stateDir,omitempty"`
}

type Server struct {
	Host string `json:"host"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
	// HostKey pins the server's public host key ("ssh-ed25519 AAAA...").
	// When empty, known_hosts is used.
	HostKey string `json:"hostKey,omitempty"`
	// DeployPath is where service directories go: <deployPath>/<service>.
	DeployPath string `json:"deployPath,omitempty"`
	// StateDir holds rollout's own data: deploy records, history, locks,
	// staging and rollback copies. Defaults to <deployPath>/.rollout.
	StateDir string `json:"stateDir,omitempty"`

	Name string `json:"-"`
}

type Service struct {
	Hooks Hooks `json:"hooks,omitempty"`
	// Path is the directory holding the compose file and everything it needs.
	// Defaults to services/<name>.
	Path        string   `json:"path,omitempty"`
	ComposeFile string   `json:"composeFile,omitempty"`
	Servers     []string `json:"servers"`
	// Wait makes compose wait for containers to be running/healthy before
	// the deploy counts as successful. Defaults to true.
	Wait *bool `json:"wait,omitempty"`
	// Preserve lists top-level server entries that deploys must never modify or copy.
	Preserve []string `json:"preserve,omitempty"`

	Name string `json:"-"`
}

// nameRe restricts names to what is safe as a directory and compose project name.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidName reports whether s is a valid server or service name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Load reads, defaults and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(abs)
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Defaults.DeployPath == "" {
		c.Defaults.DeployPath = DefaultDeployPath
	}
	for name, s := range c.Servers {
		if s == nil {
			s = &Server{}
			c.Servers[name] = s
		}
		s.Name = name
		if s.User == "" {
			s.User = c.Defaults.User
		}
		if s.Port == 0 {
			s.Port = c.Defaults.Port
		}
		if s.DeployPath == "" {
			s.DeployPath = c.Defaults.DeployPath
		}
		if s.StateDir == "" {
			s.StateDir = c.Defaults.StateDir
		}
		if s.StateDir == "" {
			s.StateDir = path.Join(s.DeployPath, ".rollout")
		}
	}
	for name, s := range c.Services {
		if s == nil {
			s = &Service{}
			c.Services[name] = s
		}
		s.Name = name
		if s.Path == "" {
			s.Path = filepath.Join("services", name)
		}
		if s.ComposeFile == "" {
			s.ComposeFile = DefaultCompose
		}
		if s.Wait == nil {
			t := true
			s.Wait = &t
		}
	}
}

// Validate checks references and that every service directory exists.
func (c *Config) Validate() error {
	var errs []error
	if len(c.Servers) == 0 {
		errs = append(errs, errors.New("no servers defined"))
	}
	for _, name := range sortedKeys(c.Servers) {
		s := c.Servers[name]
		if !nameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("server %q: name must match %s", name, nameRe))
		}
		if s.Host == "" {
			errs = append(errs, fmt.Errorf("server %q: host is required", name))
		}
		if strings.HasPrefix(s.Host, "-") || strings.HasPrefix(s.User, "-") {
			errs = append(errs, fmt.Errorf("server %q: host and user must not start with '-'", name))
		}
		if !strings.HasPrefix(s.DeployPath, "/") {
			errs = append(errs, fmt.Errorf("server %q: deployPath must be absolute", name))
		}
		if !strings.HasPrefix(s.StateDir, "/") {
			errs = append(errs, fmt.Errorf("server %q: stateDir must be absolute", name))
		}
		// Visible directories in deployPath are service directories.
		if dir, leaf := path.Split(path.Clean(s.StateDir)); path.Clean(s.StateDir) == path.Clean(s.DeployPath) ||
			(path.Clean(dir) == path.Clean(s.DeployPath) && !strings.HasPrefix(leaf, ".")) {
			errs = append(errs, fmt.Errorf("server %q: stateDir must not be deployPath or a visible directory in it (use e.g. %s)", name, path.Join(s.DeployPath, ".rollout")))
		}
	}
	for _, name := range sortedKeys(c.Services) {
		s := c.Services[name]
		if !nameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("service %q: name must match %s", name, nameRe))
		}
		if len(s.Servers) == 0 {
			errs = append(errs, fmt.Errorf("service %q: servers is required", name))
		}
		for _, srv := range s.Servers {
			if _, ok := c.Servers[srv]; !ok {
				errs = append(errs, fmt.Errorf("service %q: unknown server %q", name, srv))
			}
		}
		for _, entry := range s.Preserve {
			if entry == "" || entry == "." || entry == ".." || strings.ContainsAny(entry, "/\\\n\r") {
				errs = append(errs, fmt.Errorf("service %q: preserve entries must be top-level names", name))
			}
			if _, err := os.Lstat(filepath.Join(c.ServiceDir(s), entry)); err == nil {
				errs = append(errs, fmt.Errorf("service %q: preserved entry %q must not exist in the bundle", name, entry))
			}
		}
		compose := filepath.Join(c.ServiceDir(s), s.ComposeFile)
		if _, err := os.Stat(compose); err != nil {
			errs = append(errs, fmt.Errorf("service %q: compose file: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Address formats the server as user@host:port, omitting what ssh decides.
func (s *Server) Address() string {
	addr := s.Host
	if s.User != "" {
		addr = s.User + "@" + addr
	}
	if s.Port != 0 {
		addr += ":" + strconv.Itoa(s.Port)
	}
	return addr
}

// ServiceDir returns the absolute local directory of a service.
func (c *Config) ServiceDir(s *Service) string {
	if filepath.IsAbs(s.Path) {
		return s.Path
	}
	return filepath.Join(c.Dir, s.Path)
}

func (c *Config) ServerNames() []string  { return sortedKeys(c.Servers) }
func (c *Config) ServiceNames() []string { return sortedKeys(c.Services) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
