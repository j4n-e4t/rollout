package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j4n-e4t/rollout/internal/bundle"
)

// Run the real generated POSIX script with local fake Docker, never SSH.
func TestDeployRecovery(t *testing.T) {
	for _, shell := range []string{"sh", "dash"} {
		shellPath, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			for _, mode := range []string{"success", "history", "sync", "record", "commit-signal", "signal", "up", "recovery", "first", "adopted", "preserve-collision"} {
				t.Run(mode, func(t *testing.T) {
					base := t.TempDir()
					bin := t.TempDir()
					if err := os.Symlink(shellPath, filepath.Join(bin, "sh")); err != nil {
						t.Fatal(err)
					}
					src := t.TempDir()
					realCP, err := exec.LookPath("cp")
					if err != nil {
						t.Fatal(err)
					}
					realMV, err := exec.LookPath("mv")
					if err != nil {
						t.Fatal(err)
					}
					write := func(path, value string, perm os.FileMode) {
						t.Helper()
						if err := os.WriteFile(path, []byte(value), perm); err != nil {
							t.Fatal(err)
						}
					}
					for _, p := range []string{filepath.Join(base, "web", "nested"), filepath.Join(base, ".rollout")} {
						if err := os.MkdirAll(p, 0755); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.MkdirAll(filepath.Join(base, "web", "data"), 0755); err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(base, "web", "data", "database"), "precious data", 0600)
					write(filepath.Join(base, "web", "compose.yaml"), "old", 0644)
					write(filepath.Join(base, "web", "nested", "removed\nfile"), "remove me", 0644)
					write(filepath.Join(base, "web", "type-change"), "old file", 0644)
					write(filepath.Join(base, ".rollout", "web.json"), `{"hash":"old"}`, 0644)
					write(filepath.Join(src, "compose.yaml"), "new", 0644)
					if err := os.Mkdir(filepath.Join(src, "nested"), 0755); err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(src, "nested", "keep"), "keep", 0644)
					if err := os.Mkdir(filepath.Join(base, "web", "type-link"), 0755); err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(base, "web", "type-link", "old"), "old", 0644)
					if err := os.Symlink("compose.yaml", filepath.Join(src, "type-link")); err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(base, "external"), "untouched", 0644)
					if err := os.Symlink(filepath.Join(base, "external"), filepath.Join(base, "web", "same-link")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("compose.yaml", filepath.Join(src, "same-link")); err != nil {
						t.Fatal(err)
					}

					if err := os.MkdirAll(filepath.Join(src, "type-change"), 0755); err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(src, "type-change", "child"), "new", 0644)
					if mode == "first" || mode == "adopted" {
						if err := os.MkdirAll(filepath.Join(base, "old dir"), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.RemoveAll(filepath.Join(base, "web")); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(filepath.Join(base, ".rollout", "web.json")); err != nil {
							t.Fatal(err)
						}
					}
					write(filepath.Join(bin, "flock"), "#!/bin/sh\nexit 0\n", 0755)
					if mode == "history" {
						if err := os.Mkdir(filepath.Join(base, ".rollout", "web.history.jsonl"), 0755); err != nil {
							t.Fatal(err)
						}
					}
					write(filepath.Join(bin, "docker"), `#!/bin/sh
printf '%s\n' "$*" >> "$AUDIT_BASE/calls"
case "$*" in
  'ps -aq --filter label=com.docker.compose.project=web') [ "$AUDIT_MODE" != adopted ] || echo c1;;
  inspect*project.working_dir*) echo "$AUDIT_BASE/old dir";;
  inspect*project.config_files*) echo "$AUDIT_BASE/old dir/a.yml,$AUDIT_BASE/old dir/b.yml";;
  *' images '*) echo '[]';;
  *' up '*)
    if [ "$AUDIT_MODE" = recovery ]; then exit 1; fi
    if [ "$AUDIT_MODE" = up ] || [ "$AUDIT_MODE" = first ] || [ "$AUDIT_MODE" = adopted ]; then
      if [ ! -e "$AUDIT_BASE/up-failed" ]; then touch "$AUDIT_BASE/up-failed"; exit 1; fi
    fi;;
esac
`, 0755)
					write(filepath.Join(bin, "cp"), `#!/bin/sh
if [ "$AUDIT_MODE" = signal ] && [ "$2" = "$AUDIT_BASE/.rollout/work/web.incoming/compose.yaml" ]; then
  kill -TERM "$PPID"
  exit 1
fi
if [ "$AUDIT_MODE" = sync ] && [ "$2" = "$AUDIT_BASE/.rollout/work/web.incoming/compose.yaml" ]; then
  echo partial > "$AUDIT_BASE/web/compose.yaml"
  echo 'simulated disk full' >&2
  exit 1
fi
exec "$REAL_CP" "$@"
`, 0755)
					write(filepath.Join(bin, "mv"), `#!/bin/sh
if [ "$AUDIT_MODE" = record ] && [ "$1" = "$AUDIT_BASE/.rollout/web.json.tmp" ]; then exit 1; fi
if [ "$AUDIT_MODE" = commit-signal ] && [ "$1" = "$AUDIT_BASE/.rollout/web.json.tmp" ]; then
  "$REAL_MV" "$@" || exit 1
  kill -TERM "$PPID"
  exit 1
fi
exec "$REAL_MV" "$@"
`, 0755)
					if mode == "preserve-collision" {
						if err := os.Mkdir(filepath.Join(src, "data"), 0755); err != nil {
							t.Fatal(err)
						}
						write(filepath.Join(src, "data", "database"), "must not overwrite", 0600)
					}
					var archive bytes.Buffer
					if err := bundle.Write(&archive, src, nil); err != nil {
						t.Fatal(err)
					}
					script := deployScript{Base: base, State: filepath.Join(base, ".rollout"), Name: "web", Compose: "compose.yaml", Preserve: []string{"data"}, Wait: true, Record: Record{Hash: "new"}}.command()
					cmd := exec.Command(shellPath, "-c", script)
					cmd.Stdin = &archive
					cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "AUDIT_BASE="+base, "AUDIT_MODE="+mode, "REAL_CP="+realCP, "REAL_MV="+realMV)
					out, runErr := cmd.CombinedOutput()
					if (runErr == nil) != (mode == "success" || mode == "history") {
						t.Fatalf("err=%v, output=%s", runErr, out)
					}
					read := func(path string) string {
						t.Helper()
						b, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						return string(b)
					}
					if mode == "success" || mode == "history" {
						if mode == "history" && !strings.Contains(string(out), "history update failed") {
							t.Fatal("history failure not reported", string(out))
						}
						if read(filepath.Join(base, "web", "compose.yaml")) != "new" {
							t.Fatal("new files missing")
						}
						if _, err := os.Stat(filepath.Join(base, "web", "nested", "removed\nfile")); !os.IsNotExist(err) {
							t.Fatalf("removed newline filename retained: %v", err)
						}
						if read(filepath.Join(base, "web", "nested", "keep")) != "keep" {
							t.Fatal("retained directory not synced")
						}
						if link, err := os.Readlink(filepath.Join(base, "web", "type-link")); err != nil || link != "compose.yaml" {
							t.Fatalf("directory to link failed: %q %v", link, err)
						}
						if link, err := os.Readlink(filepath.Join(base, "web", "same-link")); err != nil || link != "compose.yaml" {
							t.Fatalf("link update failed: %q %v", link, err)
						}
						if read(filepath.Join(base, "web", "type-change", "child")) != "new" {
							t.Fatal("type change failed")
						}
						if !strings.Contains(read(filepath.Join(base, ".rollout", "web.json")), `"hash":"new"`) {
							t.Fatal("new record missing")
						}
					} else if mode == "adopted" {
						calls := read(filepath.Join(base, "calls"))
						if strings.Contains(calls, "down") {
							t.Fatal("a project that ran before the deploy must not be taken down:\n" + calls)
						}
						want := "compose -p web -f " + filepath.Join(base, "old dir", "a.yml") + " -f " + filepath.Join(base, "old dir", "b.yml") + " up -d --remove-orphans"
						if !strings.Contains(calls, want) {
							t.Fatalf("original project not restored; want %q in:\n%s", want, calls)
						}
						if _, err := os.Stat(filepath.Join(base, "web")); !os.IsNotExist(err) {
							t.Fatalf("live directory not removed: %v", err)
						}
					} else if mode == "first" {
						if _, err := os.Stat(filepath.Join(base, "web")); !os.IsNotExist(err) {
							t.Fatalf("first deploy not removed: %v", err)
						}
						if !strings.Contains(read(filepath.Join(base, "calls")), "down --remove-orphans") {
							t.Fatal("first deploy containers not cleaned up")
						}
					} else if mode != "recovery" {
						if read(filepath.Join(base, "web", "compose.yaml")) != "old" {
							t.Fatal("old compose not restored")
						}
						if read(filepath.Join(base, "web", "nested", "removed\nfile")) != "remove me" {
							t.Fatal("deleted file not restored")
						}
						if read(filepath.Join(base, "web", "type-change")) != "old file" {
							t.Fatal("type change not restored")
						}
						if read(filepath.Join(base, ".rollout", "web.json")) != `{"hash":"old"}` {
							t.Fatal("record not restored")
						}
					}
					if mode != "first" && mode != "adopted" {
						if read(filepath.Join(base, "web", "data", "database")) != "precious data" {
							t.Fatal("persistent data changed")
						}
						if _, err := os.Stat(filepath.Join(base, ".rollout", "work", "web.prev", "data")); !os.IsNotExist(err) {
							t.Fatal("persistent data copied into rollback backup")
						}
					}
					if read(filepath.Join(base, "external")) != "untouched" {
						t.Fatal("symlink target overwritten")
					}
					_, pendingErr := os.Stat(filepath.Join(base, ".rollout", "web.pending"))
					if mode == "recovery" {
						if pendingErr != nil {
							t.Fatal("failed recovery must retain pending marker")
						}
						if _, err := os.Stat(filepath.Join(base, ".rollout", "work", "web.prev", "compose.yaml")); err != nil {
							t.Fatal("backup lost", err)
						}
					} else if !os.IsNotExist(pendingErr) {
						t.Fatalf("pending marker retained: %v", pendingErr)
					}
				})
			}
		})
	}
}

func TestDeployRequiresFlock(t *testing.T) {
	base := t.TempDir()
	bin := t.TempDir()
	mkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mkdir, filepath.Join(bin, "mkdir")); err != nil {
		t.Fatal(err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", deployScript{Base: base, State: filepath.Join(base, ".rollout"), Name: "web", Compose: "compose.yaml"}.command())
	// The generated command starts sh again, so provide it in the isolated PATH.
	if err := os.Symlink(sh, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "PATH="+bin)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "flock is required") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(base, ".rollout", "work", "web.incoming")); !os.IsNotExist(err) {
		t.Fatal("touched staging without a lock")
	}
}

func TestDeployHooks(t *testing.T) {
	for _, tt := range []struct {
		name             string
		before, after    []string
		failUp           bool
		wantErr, wantNew bool
		wantEvents       string
	}{
		{name: "order and context", before: []string{`printf 'global-before:%s:%s:%s\n' "$ROLLOUT_SERVICE" "$ROLLOUT_SERVER" "$ROLLOUT_RELEASE" >> "$HOOK_EVENTS"; test "$PWD" = "$ROLLOUT_STATE_DIR/work/web.incoming"; test "$(cat compose.yaml)" = new; cd /; exit 0`, `echo service-before >> "$HOOK_EVENTS"`}, after: []string{`test "$PWD" = "$ROLLOUT_DEPLOY_PATH"; test "$(cat compose.yaml)" = new; grep -q '"hash":"new"' "$ROLLOUT_STATE_DIR/web.json"; echo service-after >> "$HOOK_EVENTS"`, `echo global-after >> "$HOOK_EVENTS"`}, wantNew: true, wantEvents: "global-before:web:production:v2\nservice-before\nup\nservice-after\nglobal-after\n"},
		{name: "before failure", before: []string{`echo before >> "$HOOK_EVENTS"; false; echo unreachable >> "$HOOK_EVENTS"`}, after: []string{`echo after >> "$HOOK_EVENTS"`}, wantErr: true, wantEvents: "before\n"},
		{name: "after failure keeps release", after: []string{`echo after >> "$HOOK_EVENTS"; exit 7`, `echo unreachable >> "$HOOK_EVENTS"`}, wantErr: true, wantNew: true, wantEvents: "up\nafter\n"},
		{name: "deploy failure skips after", before: []string{`echo before >> "$HOOK_EVENTS"`}, after: []string{`echo after >> "$HOOK_EVENTS"`}, failUp: true, wantErr: true, wantEvents: "before\nup\nup\n"},
		{name: "shell quoting", before: []string{"printf '%s\\n' \"it's a hook\" >> \"$HOOK_EVENTS\""}, wantNew: true, wantEvents: "it's a hook\nup\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "deploy path")
			state := filepath.Join(dir, "state dir")
			src := filepath.Join(dir, "source")
			bin := filepath.Join(dir, "bin")
			events := filepath.Join(dir, "events")
			for _, d := range []string{filepath.Join(base, "web"), state, src, bin} {
				if err := os.MkdirAll(d, 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, data string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(base, "web", "compose.yaml"), "old", 0644)
			write(filepath.Join(state, "web.json"), `{"hash":"old"}`, 0644)
			write(filepath.Join(src, "compose.yaml"), "new", 0644)
			write(filepath.Join(bin, "flock"), "#!/bin/sh\nexit 0\n", 0755)
			write(filepath.Join(bin, "docker"), `#!/bin/sh
case "$*" in
 *' up '*)
  echo up >> "$HOOK_EVENTS"
  if [ "$FAIL_UP" = 1 ] && [ ! -e "$HOOK_EVENTS.failed" ]; then touch "$HOOK_EVENTS.failed"; exit 1; fi;;
 *' images '*) echo '[]';;
esac
`, 0755)
			var archive bytes.Buffer
			if err := bundle.Write(&archive, src, nil); err != nil {
				t.Fatal(err)
			}
			command := deployScript{Base: base, State: state, Name: "web", Server: "production", Compose: "compose.yaml", Record: Record{Hash: "new", Version: "v2"}, BeforeHooks: tt.before, AfterHooks: tt.after}.command()
			cmd := exec.Command("sh", "-c", command)
			cmd.Stdin = &archive
			failUp := "0"
			if tt.failUp {
				failUp = "1"
			}
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOOK_EVENTS="+events, "FAIL_UP="+failUp)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v, output=%s", err, out)
			}
			gotEvents, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotEvents) != tt.wantEvents {
				t.Fatalf("events=%q want=%q; output=%s", gotEvents, tt.wantEvents, out)
			}
			live, err := os.ReadFile(filepath.Join(base, "web", "compose.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			wantLive := "old"
			if tt.wantNew {
				wantLive = "new"
			}
			if string(live) != wantLive {
				t.Fatalf("live=%q want=%q", live, wantLive)
			}
			record, err := os.ReadFile(filepath.Join(state, "web.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(record), `"hash":"`+wantLive+`"`) {
				t.Fatalf("wrong record: %s", record)
			}
			if tt.name == "after failure keeps release" && !strings.Contains(string(out), "release is already deployed") {
				t.Fatal(string(out))
			}
			if _, err := os.Stat(filepath.Join(state, "web.pending")); !os.IsNotExist(err) {
				t.Fatal("pending marker after completed recovery/deploy", err)
			}
		})
	}
}
