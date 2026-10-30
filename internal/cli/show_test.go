package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services", "web"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services", "web", "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The fake ssh answers the status, history and files scripts.
	inspect := `"/web-app-1"	"2026-10-29T10:00:00Z"	{"Status":"healthy"}	2	"sha256:abc"	{}	{}	{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"}]}	[{"Type":"bind","Source":"/opt/rollout/web/data","Destination":"/data","RW":true}]`
	record := `{"version":"v1.4","message":"Raise workers\nbody","commit":"3fa46ac32a7ac705a9aff70a6fc2cf095e2a8cef","hash":"sha256:715f6c2d0000","deployedAt":"2026-10-29T17:10:02Z","deployedBy":"julian@mac","rollout":"0.0.1"}`
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*@total*) printf '42\\tcompose.yaml\\n7\\tconfig/app.toml\\n@total 2\\n' ;;\n" +
		"*@history*) cat <<'OUT'\n@service web\n@current\n" + record + "\n@history\n" + record + "\nOUT\n;;\n" +
		"*) cat <<'OUT'\n@host\nLinux\n@os-release\nID=debian\n@dirs\nweb\n@records\nweb\t" + record + "\n@containers\nweb\t/opt/rollout/web\tapp\tweb-app-1\tnginx:alpine\trunning\tUp 1 hour (healthy)\n@inspect\n" + inspect + "\n@desired\nweb\t{\"services\":{\"app\":{}}}\nOUT\n;;\nesac\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CI", "1")
	cfgPath := filepath.Join(dir, "rollout.json")
	if err := os.WriteFile(cfgPath, []byte(`{"servers":{"s":{"host":"test.invalid"}},"services":{"web":{"servers":["s"],"preserve":["data"]}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := NewRoot()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"-c", cfgPath, "show", "web"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err, out.String())
		}
		return out.String()
	}

	out := run()
	assertPlain(t, out)
	for _, want := range []string{"web on s  running  1/1 containers up", "v1.4", "Raise workers", "app", "healthy", "nginx:alpine",
		"8080→80", "./data", "/data", "Recent deployments", "Files (2)", "config/app.toml", "42 B", "Not listed (preserved data): data"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "8080→80, 8080→80") || strings.Contains(out, "body") {
		t.Errorf("duplicate port or full message:\n%s", out)
	}

	var details []serviceDetails
	if err := json.Unmarshal([]byte(run("--json")), &details); err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].FileCount != 2 || len(details[0].Releases) != 1 || details[0].Containers[0].Health != "healthy" {
		t.Fatalf("unexpected JSON: %+v", details)
	}
}
