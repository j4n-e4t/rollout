package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j4n-e4t/rollout/internal/bundle"
	"gopkg.in/yaml.v3"
)

func TestDeployPullAlwaysAndMissingContainers(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services", "web")
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "compose.yaml"), []byte("services:\n  app:\n    image: example:latest\n  worker:\n    image: example:latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "rollout.json")
	if err := os.WriteFile(config, []byte(`{"hooks":{"beforeDeploy":"exit 99","afterDeploy":"exit 99"},"servers":{"s":{"host":"audit.invalid"}},"services":{"web":{"servers":["s"]}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := bundle.Hash(svcDir, "composeFile=compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\ncase \"$*\" in *beforeDeploy*|*afterDeploy*) echo \"dry run attempted hooks\" >&2; exit 99;; esac\ncat \"$ROLLOUT_TEST_STATUS\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(dir, "status")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ROLLOUT_TEST_STATUS", status)
	t.Setenv("ROLLOUT_SSH_KEY", "")
	t.Setenv("CI", "1")
	for _, tt := range []struct {
		name                     string
		missing, pending, always bool
		want                     string
	}{
		{name: "complete", want: "skip"},
		{name: "pull always", always: true, want: "deploy"},
		{name: "missing service", missing: true, want: "deploy"},
		{name: "failed recovery", pending: true, want: "deploy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := "@dirs\nweb\n@records\nweb\t{\"hash\":\"" + hash + "\"}\n@containers\nweb\t/opt/rollout/web\tapp\tweb-app-1\texample:latest\trunning\tUp 1 hour\n"
			if !tt.missing {
				data += "web\t/opt/rollout/web\tworker\tweb-worker-1\texample:latest\trunning\tUp 1 hour\n"
			}
			data += "@inspect\n@pending\n"
			if tt.pending {
				data += "web\n"
			}
			data += "@desired\nweb\t{\"services\":{\"app\":{},\"worker\":{}}}\n"
			if err := os.WriteFile(status, []byte(data), 0644); err != nil {
				t.Fatal(err)
			}
			args := []string{"-c", config, "deploy", "--all", "--dry-run"}
			if tt.always {
				args = append(args, "--pull", "always")
			}
			cmd := NewRoot()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err, out.String())
			}
			if !strings.Contains(out.String(), tt.want+"  web") {
				t.Fatalf("expected %s: %s", tt.want, out.String())
			}
		})
	}
}

func TestHistoryRejectsInvalidLimit(t *testing.T) {
	for _, n := range []string{"0", "-1"} {
		cmd := NewRoot()
		cmd.SetArgs([]string{"history", "-n", n, "-c", "does-not-exist"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--limit") {
			t.Fatalf("got %v", err)
		}
	}
}

func TestActionArguments(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	data, err := os.ReadFile("../../action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct{ Steps []struct{ Name, Run string } }
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range action.Runs.Steps {
		if step.Name == "Run rollout" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("action run script missing")
	}
	dir := t.TempDir()
	fake := "#!" + python + "\nimport json, sys\nprint(json.dumps(sys.argv[1:]))\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args string
		want []string
	}{
		{`deploy --all -m "Ship worker"`, []string{"deploy", "--all", "-m", "Ship worker"}},
		{`exec web app -- sh -c 'echo "$HOME"; $(touch /tmp/should-not-execute)'`, []string{"exec", "web", "app", "--", "sh", "-c", `echo "$HOME"; $(touch /tmp/should-not-execute)`}},
		{`logs web '*'`, []string{"logs", "web", "*"}},
	} {
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "ARGS="+tt.args, "RUNNER_TEMP="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got []string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err, string(out))
		}
		if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
			t.Fatalf("got %q want %q", got, tt.want)
		}
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), `ARGS=deploy -m "unterminated`, "RUNNER_TEMP="+dir)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "Invalid rollout arguments") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}
