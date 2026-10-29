package deploy

import (
	"strconv"
	"strings"
	"text/template"

	"github.com/j4n-e4t/rollout/internal/config"
)

// scriptTmpl runs on the server with the service bundle (.tar.gz) on stdin.
//
// Layout on the server:
//
//	<deployPath>/<name>/            the live service directory compose runs from
//	<stateDir>/<name>.json          record of the live release (see Record)
//	<stateDir>/<name>.history.jsonl records of past releases, newest last
//	<stateDir>/<name>.lock          serializes deploys of the same service
//	<stateDir>/<name>.pending       set while the live directory is inconsistent
//	<stateDir>/work/<name>.incoming/     bundle being unpacked
//	<stateDir>/work/<name>.prev/         previous release, kept until the new one is up
//	<stateDir>/work/<name>.record.prev   previous record, for rollback
//	<stateDir>/work/<name>.failed/       last release that failed (for debugging)
//
// The live directory is updated in place rather than swapped: compose doesn't
// recreate containers whose config is unchanged, and their bind mounts must
// keep pointing at the same directory. Files that disappear from the bundle
// are deleted, so persistent data belongs in named volumes or absolute paths.
var scriptTmpl = template.Must(template.New("deploy").Parse(`set -eu
base={{.Base}}
state={{.State}}
name={{.Name}}
compose={{.Compose}}
record={{.Record}}
export ROLLOUT_SERVICE="$name"
export ROLLOUT_SERVER={{.Server}}
export ROLLOUT_DEPLOY_PATH="$base/$name"
export ROLLOUT_STATE_DIR="$state"
export ROLLOUT_RELEASE={{.Release}}
preserve() {
  case "$1" in
    {{.PreservePattern}}) return 0 ;;
  esac
  return 1
}
live="$base/$name"
work="$state/work"
stage="$work/$name.incoming"
prev="$work/$name.prev"
failed="$work/$name.failed"
pending="$state/$name.pending"
record_prev="$work/$name.record.prev"

mkdir -p "$base" "$work"
command -v flock >/dev/null 2>&1 || { echo "rollout: flock is required for safe deployment" >&2; exit 1; }
exec 9>"$state/$name.lock"
flock -w 600 9 || { echo "rollout: timed out waiting for deploy lock" >&2; exit 1; }

[ ! -e "$pending" ] || { echo "rollout: incomplete deployment; recover $live using $prev and $record_prev, then remove $pending before retrying" >&2; exit 1; }
rm -rf "$stage"
mkdir -p "$stage"
tar -xzmf - -C "$stage"  # -m: stamp files now, avoids clock-skew warnings

dc() { docker compose -p "$name" -f "$compose" "$@"; }

kind() { if [ -L "$1" ]; then echo l; elif [ -d "$1" ]; then echo d; elif [ -e "$1" ]; then echo f; fi; }

# Shell globs preserve every filename, including embedded newlines. Each
# recursive call uses a subshell so its variables cannot overwrite its caller.
prune_dir() (
  src=$1 dst=$2
  for entry in "$dst"/* "$dst"/.[!.]* "$dst"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    leaf=${entry##*/}
    if [ "$dst" = "$live" ] && preserve "$leaf"; then continue; fi
    if [ "$(kind "$src/$leaf")" != "$(kind "$entry")" ]; then
      rm -rf "$entry"
    elif [ -d "$entry" ] && [ ! -L "$entry" ]; then
      prune_dir "$src/$leaf" "$entry"
    fi
  done
)

# Copy only managed entries: persistent data must never enter rollback copies.
copy_managed() {
  mkdir -p "$2"
  for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    leaf=${entry##*/}
    preserve "$leaf" && continue
    cp -a "$entry" "$2/"
  done
}

# Keep unchanged-type entries in place so existing bind mounts stay valid.
sync_dir() {
  mkdir -p "$2"
  prune_dir "$1" "$2"
  if [ {{.HasPreserve}} = 1 ]; then
    copy_managed "$1" "$2"
  else
    cp -a "$1/." "$2/"
  fi
}

# A pending marker makes status fail closed if recovery itself fails.
mutated=0
had_live=0
adopt_dir=
adopt_files=
recover() {
  result=$?
  trap - 0 HUP INT TERM
  if [ "$mutated" = 1 ]; then
    echo "rollout: deployment failed; restoring $name" >&2
    set +e
    rm -rf "$failed"
    mv "$stage" "$failed"
    # Do not put this subshell in an if/|| condition: that disables set -e
    # inside shell functions and could start containers after a failed sync.
    (
      set -e
      if [ "$had_live" = 1 ]; then
        sync_dir "$prev" "$live"
        (cd "$live" && dc up -d --remove-orphans {{.UpFlags}})
      elif [ -n "$adopt_dir" ]; then
        # The project ran before rollout deployed it: put its original
        # configuration back instead of stopping it.
        rm -rf "$live"
        cd "$adopt_dir"
        set --
        set -f; old_ifs=$IFS; IFS=,
        for f in $adopt_files; do set -- "$@" -f "$f"; done
        IFS=$old_ifs; set +f
        docker compose -p "$name" "$@" up -d --remove-orphans
      else
        (cd "$live" && dc down --remove-orphans)
        rm -rf "$live"
      fi
      if [ -f "$record_prev" ]; then
        cp "$record_prev" "$state/$name.json.recover"
        mv "$state/$name.json.recover" "$state/$name.json"
      else
        rm -f "$state/$name.json"
      fi
      rm -f "$pending"
    )
    restored=$?
    if [ "$restored" != 0 ]; then
      echo "rollout: recovery failed; backup retained at $prev, service marked incomplete" >&2
    fi
    [ "$result" != 0 ] || result=1
  fi
  exit "$result"
}
trap recover 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# write_record stores the release record, completed with the running images.
write_record() {
  dir="$state"
  imgs=$(cd "$live" && dc images --format json 2>/dev/null | tr -d '\n') || imgs=
  case "$imgs" in "["*) ;; *) imgs='[]' ;; esac
  printf '%s,"images":%s}\n' "$record" "$imgs" > "$dir/$name.json.tmp"
  mv "$dir/$name.json.tmp" "$dir/$name.json"
}

# History is secondary to the committed live record. Its failure must not
# roll back a release whose new record has already been published.
write_history() {
  cat "$state/$name.json" >> "$state/$name.history.jsonl" || return 1
  tail -n 200 "$state/$name.history.jsonl" > "$state/$name.history.tmp" || return 1
  mv "$state/$name.history.tmp" "$state/$name.history.jsonl"
}

# Refuse bundles that would overwrite entries reserved for persistent data.
for entry in "$stage"/* "$stage"/.[!.]* "$stage"/..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  if preserve "${entry##*/}"; then
    echo "rollout: bundle contains preserved entry ${entry##*/}" >&2
    exit 1
  fi
done

echo "rollout: validating compose file"
(cd "$stage" && dc config -q)

# Pull while the old release is still running, so the switch below only has
# to recreate containers.
echo "rollout: pulling images"
(cd "$stage" && dc pull --policy {{.Pull}} --ignore-buildable --quiet)

# Hooks run in their own shell: cd/exit/variables cannot change the deploy script.
# Use the staged bundle so hooks also work on a service's first deployment.
{{range .BeforeHooks}}
echo "rollout: running beforeDeploy hook"
(cd "$stage" && sh -eu -c {{.}} </dev/null) || { rc=$?; echo "rollout: beforeDeploy hook failed (exit $rc)" >&2; exit "$rc"; }
{{end}}
rm -rf "$prev"
if [ -d "$live" ]; then
  copy_managed "$live" "$prev"
  had_live=1
else
  # A project of this name may already run from elsewhere (see rollout
  # import); remember where, so a failed deploy can restore it.
  cid=$(docker ps -aq --filter "label=com.docker.compose.project=$name" | head -n 1)
  if [ -n "$cid" ]; then
    adopt_dir=$(docker inspect -f {{.WorkingDirFormat}} "$cid")
    adopt_files=$(docker inspect -f {{.ConfigFilesFormat}} "$cid")
  fi
fi
rm -f "$record_prev"
if [ -f "$state/$name.json" ]; then
  cp "$state/$name.json" "$record_prev"
fi
: > "$pending"
mutated=1
sync_dir "$stage" "$live"

echo "rollout: starting $name"
(cd "$live" && dc up -d --remove-orphans {{.UpFlags}})
write_record
mutated=0
rm -f "$pending"
write_history || echo "rollout: warning: release committed, but history update failed" >&2
rm -rf "$prev" "$stage" "$failed" "$record_prev"
echo "rollout: $name is up"
{{range .AfterHooks}}
echo "rollout: running afterDeploy hook"
(cd "$live" && sh -eu -c {{.}} </dev/null) || { rc=$?; echo "rollout: afterDeploy hook failed (exit $rc); release is already deployed" >&2; exit "$rc"; }
{{end}}
exit 0
`))

// PullPolicy is passed to `docker compose pull --policy`.
type PullPolicy string

const (
	PullMissing PullPolicy = "missing"
	PullAlways  PullPolicy = "always"
)

type scriptParams struct {
	Server, Release                             string
	BeforeHooks, AfterHooks                     []string
	Base, State, Name, Compose, Record, UpFlags string
	WorkingDirFormat, ConfigFilesFormat         string
	PreservePattern, HasPreserve                string
	Pull                                        PullPolicy
}

type deployScript struct {
	Server                     string
	BeforeHooks, AfterHooks    []string
	Base, State, Name, Compose string
	Wait                       bool
	WaitTimeout                int
	Pull                       PullPolicy
	Preserve                   []string
	Record                     Record
}

// command returns the command line passed to the SSH session. The script is
// wrapped in sh -c so it doesn't depend on the user's login shell.
func (d deployScript) command() string {
	var up []string
	if d.Wait {
		up = append(up, "--wait")
		if d.WaitTimeout > 0 {
			up = append(up, "--wait-timeout", strconv.Itoa(d.WaitTimeout))
		}
	}
	pull := d.Pull
	if pull != PullAlways {
		pull = PullMissing
	}
	patterns := []string{"''"}
	hasPreserve := "0"
	for _, entry := range d.Preserve {
		patterns = append(patterns, shellQuote(entry))
		hasPreserve = "1"
	}
	var b strings.Builder
	quoteHooks := func(hooks []string) []string {
		var quoted []string
		for _, h := range hooks {
			if strings.TrimSpace(h) != "" {
				quoted = append(quoted, shellQuote(h))
			}
		}
		return quoted
	}
	err := scriptTmpl.Execute(&b, scriptParams{
		Server:            shellQuote(d.Server),
		Release:           shellQuote(d.Record.Release()),
		BeforeHooks:       quoteHooks(d.BeforeHooks),
		AfterHooks:        quoteHooks(d.AfterHooks),
		PreservePattern:   strings.Join(patterns, "|"),
		HasPreserve:       hasPreserve,
		Base:              shellQuote(d.Base),
		State:             shellQuote(d.State),
		Name:              shellQuote(d.Name),
		Compose:           shellQuote(d.Compose),
		Record:            shellQuote(d.Record.prefix()),
		UpFlags:           strings.Join(up, " "),
		WorkingDirFormat:  shellQuote(`{{index .Config.Labels "com.docker.compose.project.working_dir"}}`),
		ConfigFilesFormat: shellQuote(`{{index .Config.Labels "com.docker.compose.project.config_files"}}`),
		Pull:              pull,
	})
	if err != nil {
		panic(err) // template is static; params are plain strings
	}
	return "sh -c " + shellQuote(b.String())
}

// shellQuote quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ComposeCommand returns a remote command running `docker compose <args>` for
// a service on srv: in its live directory, or, for a project rollout hasn't
// deployed yet (e.g. imported), where that project runs.
func ComposeCommand(srv *config.Server, svc *config.Service, args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	script := composeAt(srv.DeployPath+"/"+svc.Name, svc.Name, svc.ComposeFile,
		"{ echo "+shellQuote("rollout: "+svc.Name+" is not deployed on "+srv.Name)+" >&2; exit 1; }") +
		"exec docker compose -p " + shellQuote(svc.Name) + ` "$@" ` + strings.Join(quoted, " ")
	return "sh -c " + shellQuote(script)
}

// composeAt returns shell code that changes to the directory of compose
// project name and sets "$@" to its -f flags: the live directory if it exists,
// else the working directory and files Docker recorded on the project's
// containers. It runs notFound if neither exists.
func composeAt(live, name, composeFile, notFound string) string {
	return `if [ -d ` + shellQuote(live) + ` ]; then
  cd ` + shellQuote(live) + ` && set -- -f ` + shellQuote(composeFile) + `
else
  cid=$(docker ps -aq --filter label=com.docker.compose.project=` + shellQuote(name) + ` | head -n 1)
  [ -n "$cid" ] || ` + notFound + `
  wd=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$cid")
  cf=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$cid")
  cd "$wd" || ` + notFound + `
  set --
  set -f; old_ifs=$IFS; IFS=,
  for f in $cf; do set -- "$@" -f "$f"; done
  IFS=$old_ifs; set +f
fi
`
}
