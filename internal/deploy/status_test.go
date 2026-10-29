package deploy

import (
	"github.com/j4n-e4t/rollout/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	out := strings.Join([]string{
		"@dirs",
		"web",
		"old",
		"@records",
		"web\t" + `{"version":"v2","commit":"0123456789abcdef","hash":"sha256:aa","deployedAt":"2026-10-30T10:00:00Z","deployedBy":"julian@mac","rollout":"dev","images":[{"ID":"sha256:bb","ContainerName":"web-nginx-1","Repository":"nginx","Tag":"alpine"}]}`,
		"broken\tnot json",
		"@containers",
		"web\t/opt/rollout/web\tnginx\tweb-nginx-1\tnginx:alpine\trunning\tUp 2 minutes (healthy)",
		"web\t/opt/rollout/web\tmigrate\tweb-migrate-1\tapp:1\texited\tExited (0) 2 minutes ago",
		"api\t/opt/rollout/api\tapi\tapi-api-1\tapi:2\texited\tExited (1) 5 seconds ago",
		"other\t/home/me/other\tdb\tother-db-1\tpostgres\trunning\tUp 1 hour",
		"legacy\t/root/legacy\tapp\tlegacy-app-1\tapp:1\trunning\tUp 3 days",
		"@inspect",
		strings.Join([]string{`"/web-nginx-1"`, `"2026-10-29T16:31:18Z"`, `{"Status":"healthy","Log":[]}`, `2`, `"sha256:df22"`,
			`{"com.docker.compose.depends_on":"api:service_healthy:false,cache:service_started:true"}`,
			`{"web_default":{"IPAddress":"172.18.0.2","Aliases":["web-nginx-1","nginx","0123456789ab"]}}`,
			`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"18080"}],"443/tcp":null}`,
			`[{"Type":"volume","Name":"web_data","Source":"/var/lib/docker/volumes/web_data/_data","Destination":"/data","RW":true}]`}, "\t"),
		"",
	}, "\n")

	dirs, cs, records := parseStatus([]byte(out), "/opt/rollout/", map[string]bool{"legacy": true})
	if r := records["web"]; r == nil || r.Release() != "v2" || r.Images[0].Ref() != "nginx:alpine" || r.Images[0].ID != "sha256:bb" {
		t.Errorf("web record = %+v", r)
	}
	if _, ok := records["broken"]; ok {
		t.Error("unparseable record should be ignored")
	}
	var nginx Container
	for _, c := range cs["web"] {
		if c.Name == "web-nginx-1" {
			nginx = c
		}
	}
	if nginx.Health != "healthy" || nginx.RestartCount != 2 || nginx.StartedAt.IsZero() ||
		strings.Join(nginx.DependsOn, ",") != "api,cache" ||
		len(nginx.Networks) != 1 || nginx.Networks[0].IP != "172.18.0.2" || strings.Join(nginx.Networks[0].Aliases, ",") != "nginx" ||
		len(nginx.Ports) != 2 || nginx.Ports[1].HostPort != "18080" ||
		len(nginx.Mounts) != 1 || nginx.Mounts[0].Name != "web_data" {
		t.Errorf("inspect details not applied: %+v", nginx)
	}
	if !dirs["web"] || !dirs["old"] || len(dirs) != 2 {
		t.Errorf("dirs = %v", dirs)
	}
	// legacy runs elsewhere but is named like a configured service (imported).
	if len(cs["web"]) != 2 || len(cs["api"]) != 1 || cs["other"] != nil || len(cs["legacy"]) != 1 {
		t.Errorf("containers = %v", cs)
	}
	if got := serviceState(true, cs["web"]); got != StateRunning {
		t.Errorf("web state = %s", got)
	}
	if got := serviceState(false, cs["api"]); got != StateStopped {
		t.Errorf("api state = %s", got)
	}
	if got := serviceState(true, nil); got != StateStopped {
		t.Errorf("dir without containers = %s", got)
	}
	if got := serviceState(false, nil); got != StateNotDeployed {
		t.Errorf("nothing = %s", got)
	}
}

func TestServiceState(t *testing.T) {
	up := Container{State: "running", Status: "Up 1 minute"}
	tests := []struct {
		cs   []Container
		want string
	}{
		{[]Container{up, {State: "exited", Status: "Exited (137) 1 minute ago"}}, StateDegraded},
		{[]Container{up, {State: "running", Status: "Up 1 minute (unhealthy)"}}, StateUnhealthy},
		{[]Container{{State: "running", Status: "Up 3 seconds (health: starting)"}}, StateStarting},
		{[]Container{{State: "exited", Status: "Exited (0) 5 seconds ago"}}, StateStopped},
		{[]Container{up, {State: "exited", Status: "Exited (0) 5 seconds ago"}}, StateRunning},
	}
	for _, tt := range tests {
		if got := serviceState(true, tt.cs); got != tt.want {
			t.Errorf("serviceState(%v) = %s, want %s", tt.cs, got, tt.want)
		}
	}
}

func TestStatusCommandIsValidShell(t *testing.T) {
	script := strings.TrimPrefix(statusCommand("/opt/rollout", "/opt/rollout/.rollout", &config.Service{Name: "web", ComposeFile: "compose.yaml"}), "sh -c ")
	if out, err := exec.Command("sh", "-c", "sh -n -c "+script).CombinedOutput(); err != nil {
		t.Fatalf("script does not parse: %v\n%s", err, out)
	}
}

func TestDesiredContainers(t *testing.T) {
	for _, tt := range []struct {
		name, config string
		cs           []Container
		want         bool
	}{
		{"missing service", `{"services":{"app":{},"worker":{}}}`, []Container{{Service: "app", State: "running"}}, false},
		{"missing replica", `{"services":{"app":{"deploy":{"replicas":2}}}}`, []Container{{Service: "app", State: "running"}}, false},
		{"all replicas", `{"services":{"app":{"deploy":{"replicas":2}}}}`, []Container{{Service: "app", State: "running"}, {Service: "app", State: "running"}}, true},
		{"completed job", `{"services":{"app":{},"migrate":{}}}`, []Container{{Service: "app", State: "running"}, {Service: "migrate", State: "exited", Status: "Exited (0) 1 minute ago"}}, true},
		{"inactive profile omitted by compose", `{"services":{"app":{}}}`, []Container{{Service: "app", State: "running"}}, true},
		{"missing active profile", `{"services":{"app":{},"debug":{"profiles":["debug"]}}}`, []Container{{Service: "app", State: "running"}}, false},
		{"zero replicas", `{"services":{"app":{},"worker":{"deploy":{"replicas":0}}}}`, []Container{{Service: "app", State: "running"}}, true},
		{"scale", `{"services":{"app":{"scale":2}}}`, []Container{{Service: "app", State: "running"}}, false},
		{"bad config", `!`, []Container{{Service: "app", State: "running"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired, pending := parseDesired([]byte("@pending\nother\n@desired\nweb\t" + tt.config + "\n"))
			if !pending["other"] || pending["web"] {
				t.Fatal("bad pending markers", pending)
			}
			if got := desiredComplete(desired["web"], tt.cs); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestStatusDesiredCommand(t *testing.T) {
	base := t.TempDir()
	bin := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
case "$*" in
  *' config --format json') printf '{\n "services": {"app": {"deploy": {"replicas": 2}}}\n}\n';;
esac
`), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", statusCommand(base, filepath.Join(base, ".rollout"), &config.Service{Name: "web", ComposeFile: "alternate compose.yaml"}))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	desired, _ := parseDesired(out)
	if desired["web"] == nil || desired["web"].Services["app"].Deploy.Replicas == nil || *desired["web"].Services["app"].Deploy.Replicas != 2 {
		t.Fatalf("desired output not parsed: %s", out)
	}
}

// A project rollout hasn't deployed is checked where Docker says it runs,
// with the compose files recorded on its containers.
func TestStatusDesiredImported(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(t.TempDir(), "old app")
	bin := t.TempDir()
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
case "$*" in
  'ps -aq --filter label=com.docker.compose.project=legacy') echo c1;;
  'ps -aq --filter label=com.docker.compose.project=gone') ;;
  inspect*project.working_dir*) echo "`+legacy+`";;
  inspect*project.config_files*) echo "`+legacy+`/compose.yml,`+legacy+`/over ride.yml";;
  *' config --format json')
    [ "$PWD" = "`+legacy+`" ] || exit 9
    echo "{\"services\":{\"app\":{}},\"args\":\"$*\"}";;
esac
`), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", statusCommand(base, filepath.Join(base, ".rollout"),
		&config.Service{Name: "legacy", ComposeFile: "compose.yaml"}, &config.Service{Name: "gone", ComposeFile: "compose.yaml"}))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(string(out), "compose -p legacy -f "+legacy+"/compose.yml -f "+legacy+"/over ride.yml config") {
		t.Errorf("compose not run with the recorded files: %s", out)
	}
	desired, _ := parseDesired(out)
	if desired["legacy"] == nil || desired["gone"] != nil {
		t.Fatalf("desired = %v\n%s", desired, out)
	}
}

func TestParseHostOS(t *testing.T) {
	for _, tt := range []struct{ response, want string }{
		{"@host\nLinux\n@dirs\nweb\n", "Linux"},
		{"@host\nDarwin\n@dirs\n", "Darwin"},
		{"@host\n@dirs\nweb\n", ""},
		{"@dirs\nweb\n", ""},
	} {
		if got, _ := parseHostInfo(tt.response); got != tt.want {
			t.Errorf("parseHostInfo(%q) = %q, want %q", tt.response, got, tt.want)
		}
	}
	if out, err := exec.Command("sh", "-n", "-c", statusCommand("/opt/rollout", "/opt/rollout/.rollout")).CombinedOutput(); err != nil {
		t.Fatalf("status shell: %v: %s", err, out)
	}
}
