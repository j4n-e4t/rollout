package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
)

func TestOutputPolicy(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		name              string
		opts              config.Output
		plain, terminal   bool
		ci, term, noColor string
		want              presentation
	}{
		{name: "terminal", terminal: true, want: presentation{color: true, icons: true}},
		{name: "pipe"},
		{name: "CI", terminal: true, ci: "1"},
		{name: "dumb", terminal: true, term: "dumb"},
		{name: "no color", terminal: true, noColor: "1", want: presentation{icons: true}},
		{name: "no icons", terminal: true, opts: config.Output{Icons: &no}, want: presentation{color: true}},
		{name: "explicit styling in pipe", opts: config.Output{Color: &yes, Icons: &yes}, want: presentation{color: true, icons: true}},
		{name: "config plain wins", terminal: true, opts: config.Output{Plain: true, Color: &yes, Icons: &yes}},
		{name: "flag plain wins", terminal: true, plain: true, opts: config.Output{Color: &yes, Icons: &yes}},
		{name: "NO_COLOR wins", terminal: true, noColor: "1", opts: config.Output{Color: &yes}, want: presentation{icons: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CI", tt.ci)
			t.Setenv("TERM", tt.term)
			t.Setenv("NO_COLOR", tt.noColor)
			if got := outputPresentation(tt.opts, tt.plain, tt.terminal); got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}

func TestStatusPresentation(t *testing.T) {
	rows := []deploy.ServiceStatus{
		{Service: "api", Server: "production", HostOS: "Linux", State: deploy.StateRunning, InConfig: true, Containers: []deploy.Container{{Name: "api-1", Image: "secret-image:latest", State: "running"}, {Name: "db-1", Image: "db:17", State: "running"}}},
		{Service: "longer-stack", Server: "mac", HostOS: "Darwin", State: deploy.StateUnreachable, InConfig: true, Error: "connection refused"},
	}
	var styled, plain bytes.Buffer
	if err := printStatus(&styled, rows, presentation{color: true, icons: true}); err != nil {
		t.Fatal(err)
	}
	if err := printStatus(&plain, rows, presentation{}); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{styled.String(), plain.String()} {
		if strings.Contains(out, "IMAGES") || strings.Contains(out, "secret-image") {
			t.Fatalf("images leaked into summary: %q", out)
		}
		if !strings.Contains(out, "2/2") || !strings.Contains(out, "connection refused") {
			t.Fatal(out)
		}
	}
	assertPlain(t, plain.String())
	if strings.ContainsAny(plain.String(), "│╭─") || !strings.ContainsAny(styled.String(), "│╭─") {
		t.Fatalf("borders belong to styled output only:\n%s\n%s", plain.String(), styled.String())
	}
	if !strings.Contains(styled.String(), "\x1b[") || !strings.Contains(styled.String(), "\uf17c") || !strings.Contains(styled.String(), "\uf179") {
		t.Fatal(styled.String())
	}
	lines := strings.Split(styled.String(), "\n")
	// State text begins at the same terminal column despite different
	// icon/color sequences, and every line of the box is equally wide.
	first := strings.Index(lines[3], "\uf058")
	second := strings.Index(lines[4], "\uf057")
	if ansi.StringWidth(lines[3][:first]) != ansi.StringWidth(lines[4][:second]) {
		t.Fatalf("misaligned columns: %q", styled.String())
	}
	for _, l := range lines[:6] {
		if ansi.StringWidth(l) != ansi.StringWidth(lines[0]) {
			t.Fatalf("ragged border: %q", styled.String())
		}
	}
}

func assertPlain(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, "\x1b") {
		t.Fatalf("ANSI in plain output: %q", s)
	}
	for _, r := range s {
		if unicode.Is(unicode.Co, r) {
			t.Fatalf("Nerd Font glyph in plain output: %q", s)
		}
	}
}

func TestLsOutputModes(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services", "web")
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncat <<'STATUS'\n@host\nLinux\n@os-release\nID=ubuntu\n@dirs\nweb\nunmanaged\n@records\n@containers\nweb\t/opt/rollout/web\tapp\tweb-app-1\texample:latest\trunning\tUp 1 hour\n@desired\nweb\t{\"services\":{\"app\":{}}}\nSTATUS\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ROLLOUT_SSH_KEY", "")
	t.Setenv("CI", "1")
	t.Setenv("NO_COLOR", "")
	cfgPath := filepath.Join(dir, "rollout.json")
	for _, tt := range []struct {
		name, output     string
		args             []string
		styled, json     bool
		showUnconfigured bool
	}{
		{name: "pipe"},
		{name: "explicit hide", output: `"showUnconfigured":false`},
		{name: "show unconfigured", output: `"showUnconfigured":true`, showUnconfigured: true},
		{name: "JSON shows unconfigured", output: `"showUnconfigured":true`, args: []string{"--json"}, json: true, showUnconfigured: true},
		{name: "enabled", output: `"color":true,"icons":true`, styled: true},
		{name: "plain config", output: `"plain":true,"color":true,"icons":true`},
		{name: "plain flag", output: `"color":true,"icons":true`, args: []string{"--plain"}},
		{name: "json", output: `"color":true,"icons":true`, args: []string{"--json"}, json: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := `{"output":{` + tt.output + `},"servers":{"s":{"host":"test.invalid"}},"services":{"web":{"servers":["s"]}}}`
			if err := os.WriteFile(cfgPath, []byte(data), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := NewRoot()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{"-c", cfgPath, "ls"}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err, out.String())
			}
			if got := strings.Contains(out.String(), "unmanaged"); got != tt.showUnconfigured {
				t.Fatalf("unconfigured visible = %v, want %v: %s", got, tt.showUnconfigured, out.String())
			}
			if tt.styled {
				if !strings.Contains(out.String(), "\x1b[") {
					t.Fatal(out.String())
				}
			} else {
				assertPlain(t, out.String())
			}
			if tt.json {
				var rows []deploy.ServiceStatus
				if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
					t.Fatal(err, out.String())
				}
				wantRows := 1
				if tt.showUnconfigured {
					wantRows = 2
				}
				if len(rows) != wantRows {
					t.Fatalf("got %d rows, want %d", len(rows), wantRows)
				}
				var web *deploy.ServiceStatus
				for i := range rows {
					if rows[i].Service == "web" {
						web = &rows[i]
					}
				}
				if web == nil || !web.InConfig || web.HostOS != "Linux" || web.HostDistro != "ubuntu" || web.Containers[0].Image != "example:latest" {
					t.Fatalf("unexpected JSON: %+v", rows)
				}
			}
		})
	}
}

func TestDistroIconsMatchInStatusAndHistory(t *testing.T) {
	for _, tt := range []struct{ os, distro, icon string }{
		{"Linux", "ubuntu", "\uf31b"},
		{"Linux", "debian", "\uf306"},
		{"Linux", "alpine", "\uf300"},
		{"Linux", "fedora", "\uf30a"},
		{"Linux", "custom-distro", "\uf17c"},
		{"Linux", "", "\uf17c"},
		{"Darwin", "ubuntu", "\uf179"},
		{"", "", "\uf233"},
	} {
		t.Run(tt.os+"/"+tt.distro, func(t *testing.T) {
			row := deploy.ServiceStatus{Service: "web", Server: "host", HostOS: tt.os, HostDistro: tt.distro, State: deploy.StateRunning, InConfig: true}
			history := deploy.ServiceHistory{Service: "web", Server: "host", HostOS: tt.os, HostDistro: tt.distro}
			for _, p := range []presentation{{icons: true}, {}} {
				var statusOut, historyOut bytes.Buffer
				if err := printStatus(&statusOut, []deploy.ServiceStatus{row}, p); err != nil {
					t.Fatal(err)
				}
				printHistory(&historyOut, []deploy.ServiceHistory{history}, 10, p)
				for _, out := range []string{statusOut.String(), historyOut.String()} {
					if p.icons {
						if !strings.Contains(out, tt.icon+" host") {
							t.Fatalf("missing host icon %q: %q", tt.icon, out)
						}
					} else {
						assertPlain(t, out)
					}
				}
			}
		})
	}
}
