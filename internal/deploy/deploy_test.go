package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j4n-e4t/rollout/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	dir := t.TempDir()
	for _, svc := range []string{"web", "db"} {
		os.MkdirAll(filepath.Join(dir, svc), 0o755)
		os.WriteFile(filepath.Join(dir, svc, "compose.yaml"), []byte("services: {}\n# "+svc+"\n"), 0o644)
	}
	return &config.Config{
		Dir: dir,
		Servers: map[string]*config.Server{
			"a": {Name: "a"},
			"b": {Name: "b"},
		},
		Services: map[string]*config.Service{
			"web": {Name: "web", Path: "web", ComposeFile: "compose.yaml", Servers: []string{"a", "b"}},
			"db":  {Name: "db", Path: "db", ComposeFile: "compose.yaml", Servers: []string{"a"}},
		},
	}
}

func summarize(steps []Step) string {
	var parts []string
	for _, s := range steps {
		var svcs []string
		for _, it := range s.Items {
			svcs = append(svcs, it.Service.Name)
		}
		parts = append(parts, s.Server.Name+"="+strings.Join(svcs, ","))
	}
	return strings.Join(parts, " ")
}

func TestPlan(t *testing.T) {
	cfg := testConfig(t)
	tests := []struct {
		services      []string
		want, wantErr string
	}{
		{services: []string{"web", "db"}, want: "a=web,db b=web"},
		{services: []string{"web", "web"}, want: "a=web b=web"},
		{services: []string{"db", "web"}, want: "a=db,web b=web"},
		{services: []string{"nope"}, wantErr: `unknown service "nope"`},
	}
	for _, tt := range tests {
		steps, err := Plan(cfg, tt.services, noSecrets)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Plan(%v) err = %v, want %q", tt.services, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("Plan(%v): %v", tt.services, err)
			continue
		}
		if got := summarize(steps); got != tt.want {
			t.Errorf("Plan(%v) = %q, want %q", tt.services, got, tt.want)
		}
	}
}

func noSecrets(string) (string, bool) { return "", false }

func TestPlanSecrets(t *testing.T) {
	cfg := testConfig(t)
	os.WriteFile(filepath.Join(cfg.Dir, "web", ".env"), []byte("DB_PASSWORD=${DB_PASSWORD}\n"), 0o644)
	_, err := Plan(cfg, []string{"web", "db"}, noSecrets)
	if err == nil || !strings.Contains(err.Error(), "web: DB_PASSWORD") {
		t.Fatalf("Plan without the secret: err = %v, want it named", err)
	}
	steps, err := Plan(cfg, []string{"web"}, func(n string) (string, bool) { return "pw", n == "DB_PASSWORD" })
	if err != nil {
		t.Fatal(err)
	}
	it := steps[0].Items[0]
	if string(it.Secrets.Files[".env"]) != "DB_PASSWORD=\"pw\"\n" || it.Secrets.Digest == "" {
		t.Errorf("rendered = %q, digest %q", it.Secrets.Files[".env"], it.Secrets.Digest)
	}
}

func TestItemDeploy(t *testing.T) {
	for change, want := range map[Change]bool{
		ChangeNew: true, ChangeExisting: true, ChangeChanged: true, ChangeSecrets: true, ChangeNotRunning: true, ChangeUnknown: true, ChangeUnchanged: false,
	} {
		if got := (Item{Change: change}).Deploy(false); got != want {
			t.Errorf("Deploy(%s) = %v, want %v", change, got, want)
		}
	}
	if !(Item{Change: ChangeUnchanged}).Deploy(true) {
		t.Error("force must deploy unchanged services")
	}
}

func TestComposeCommandQuotes(t *testing.T) {
	srv := &config.Server{Name: "a", DeployPath: "/opt/rollout"}
	svc := &config.Service{Name: "web", ComposeFile: "compose.yaml"}
	cmd := ComposeCommand(srv, svc, "exec", "nginx", "sh", "-c", "echo 'hi' $HOME")
	script := strings.TrimPrefix(cmd, "sh -c ")
	if out, err := exec.Command("sh", "-c", "sh -n -c "+script).CombinedOutput(); err != nil {
		t.Fatalf("script does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(cmd, "-p") || !strings.Contains(cmd, "/opt/rollout/web") {
		t.Errorf("unexpected command %s", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(`it's a "test" $HOME`)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `it's a "test" $HOME` {
		t.Errorf("round trip = %q", got)
	}
}

func TestRemoteCommandIsValidShell(t *testing.T) {
	cmd := deployScript{
		Base: "/opt/rollout", State: "/var/lib/rollout", Name: "web", Compose: "compose.yaml", Wait: true, WaitTimeout: 120,
		Record: Record{Version: "it's v1", Hash: "sha256:aa"},
	}.command()
	for _, want := range []string{"--wait-timeout 120", "--policy missing", `"version":"it`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in %s", want, cmd)
		}
	}
	// sh -n parses without executing; the outer sh -c receives the script.
	script := strings.TrimPrefix(cmd, "sh -c ")
	if out, err := exec.Command("sh", "-c", "sh -n -c "+script).CombinedOutput(); err != nil {
		t.Fatalf("script does not parse: %v\n%s", err, out)
	}
}

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &PrefixWriter{prefix: "[x] ", out: &buf}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\nthree"))
	w.Flush()
	if got, want := buf.String(), "[x] one\n[x] two\n[x] three\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSkippedServicesDoNotRunHooks(t *testing.T) {
	cfg := testConfig(t)
	cfg.Hooks = config.Hooks{BeforeDeploy: "exit 99", AfterDeploy: "exit 99"}
	cfg.Services["web"].Hooks = cfg.Hooks
	steps := []Step{{Server: cfg.Servers["a"], Items: []Item{{Service: cfg.Services["web"], Change: ChangeUnchanged, Current: &Record{Version: "v1"}}}}}
	var out bytes.Buffer
	// No SSH runner is needed: unchanged services must not execute remote hooks.
	result, err := Run(cfg, steps, Options{Out: &out})
	if err != nil || result.Deployed != 0 || result.Skipped != 1 {
		t.Fatalf("result=%+v err=%v output=%s", result, err, out.String())
	}
}
