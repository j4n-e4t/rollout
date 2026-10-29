package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "services/web/compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(dir, "rollout.json"), `{
		"defaults": {"user": "deploy"},
		"servers": {"vps-1": {"host": "203.0.113.10", "port": 2222}},
		"services": {"web": {"servers": ["vps-1"]}}
	}`)

	c, err := Load(filepath.Join(dir, "rollout.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := c.Servers["vps-1"]
	if srv.User != "deploy" || srv.Port != 2222 || srv.DeployPath != DefaultDeployPath || srv.StateDir != DefaultDeployPath+"/.rollout" {
		t.Errorf("server defaults not applied: %+v", srv)
	}
	svc := c.Services["web"]
	if svc.ComposeFile != DefaultCompose || !*svc.Wait {
		t.Errorf("service defaults not applied: %+v", svc)
	}
	if got, want := c.ServiceDir(svc), filepath.Join(dir, "services/web"); got != want {
		t.Errorf("ServiceDir = %q, want %q", got, want)
	}
}

func TestStateDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "services/web/compose.yaml"), "services: {}\n")
	load := func(defaults, server string) (*Config, error) {
		writeFile(t, filepath.Join(dir, "rollout.json"), `{
			"defaults": {`+defaults+`},
			"servers": {"a": {"host": "a"`+server+`}},
			"services": {"web": {"servers": ["a"]}}
		}`)
		return Load(filepath.Join(dir, "rollout.json"))
	}
	c, err := load(`"deployPath": "/srv/apps", "stateDir": "/var/lib/rollout"`, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Servers["a"]; s.DeployPath != "/srv/apps" || s.StateDir != "/var/lib/rollout" {
		t.Errorf("defaults not applied: %+v", s)
	}
	c, err = load(`"stateDir": "/var/lib/rollout"`, `, "deployPath": "/srv/x", "stateDir": "/srv/x/.state"`)
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Servers["a"]; s.DeployPath != "/srv/x" || s.StateDir != "/srv/x/.state" {
		t.Errorf("server override not applied: %+v", s)
	}
	for _, bad := range []string{`, "stateDir": "rel"`, `, "deployPath": "/srv", "stateDir": "/srv"`, `, "deployPath": "/srv", "stateDir": "/srv/state"`} {
		if _, err := load("", bad); err == nil || !strings.Contains(err.Error(), "stateDir") {
			t.Errorf("server%s: err = %v, want stateDir error", bad, err)
		}
	}
}

func TestLoadValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rollout.json"), `{
		"servers": {"Bad Name": {}},
		"services": {"api": {"servers": ["nope"]}, "lonely": {}}
	}`)

	_, err := Load(filepath.Join(dir, "rollout.json"))
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{`"Bad Name": name must match`, `host is required`, `unknown server "nope"`, `"lonely": servers is required`, `compose file`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rollout.json"), `{"servers": {}, "sevices": {}}`)
	if _, err := Load(filepath.Join(dir, "rollout.json")); err == nil || !strings.Contains(err.Error(), "sevices") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestPreserveValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "services/web/compose.yaml"), "services: {}\n")
	for _, entry := range []string{"data", "../data", "nested/data", "", ".", "..", "compose.yaml"} {
		c := &Config{Dir: dir, Servers: map[string]*Server{"a": {Host: "a"}}, Services: map[string]*Service{"web": {Servers: []string{"a"}, Preserve: []string{entry}}}}
		c.applyDefaults()
		err := c.Validate()
		if (err == nil) != (entry == "data") {
			t.Errorf("preserve %q: %v", entry, err)
		}
	}
}

func TestLoadHooks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "services/web/compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(dir, "rollout.json"), `{
  "hooks":{"beforeDeploy":"echo global before","afterDeploy":"echo global after"},
  "servers":{"s":{"host":"test.invalid"}},
  "services":{"web":{"servers":["s"],"hooks":{"beforeDeploy":"sh backup.sh","afterDeploy":"docker compose exec -T app ./migrate"}}}
 }`)
	cfg, err := Load(filepath.Join(dir, "rollout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks.BeforeDeploy != "echo global before" || cfg.Hooks.AfterDeploy != "echo global after" || cfg.Services["web"].Hooks.BeforeDeploy != "sh backup.sh" || cfg.Services["web"].Hooks.AfterDeploy != "docker compose exec -T app ./migrate" {
		t.Fatalf("hooks not loaded: %+v, %+v", cfg.Hooks, cfg.Services["web"].Hooks)
	}
}
