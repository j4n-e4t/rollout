package deploy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// Project is a compose project found on a server.
type Project struct {
	Dir        string   // working directory it was started from
	Files      []string // compose files it was started with
	Containers int
	HasRecord  bool // the service already has a deploy record
}

// FindProject looks for the compose project named like svc on srv. It
// returns nil if there is none.
func FindProject(ssh *remote.SSH, srv *config.Server, svc *config.Service) (*Project, error) {
	name := shellQuote(svc.Name)
	script := `set -eu
state=` + shellQuote(srv.StateDir) + `
[ ! -f "$state"/` + name + `.json ] || echo @record
ids=$(docker ps -aq --filter label=com.docker.compose.project=` + name + `)
[ -n "$ids" ] || exit 0
cid=$(echo "$ids" | head -n 1)
echo "@count $(echo "$ids" | wc -l)"
echo "@dir $(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$cid")"
echo "@files $(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$cid")"
`
	out, err := ssh.Output(Target(srv), "sh -c "+shellQuote(script))
	if err != nil {
		return nil, err
	}
	var p Project
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "@record":
			p.HasRecord = true
		case "@count":
			p.Containers, _ = strconv.Atoi(strings.TrimSpace(val))
			found = true
		case "@dir":
			p.Dir = val
		case "@files":
			if val != "" {
				p.Files = strings.Split(val, ",")
			}
		}
	}
	if !found {
		if p.HasRecord {
			return &p, nil
		}
		return nil, nil
	}
	return &p, nil
}

// ErrHasRecord means the service already has a deploy record on the server.
var ErrHasRecord = errors.New("already has a deploy record; --force replaces it")

// Import adopts the compose project p, found by FindProject, as svc on srv
// without touching its containers or files: it writes a deploy record marked
// as imported, so status and history treat the service as deployed. The next
// deploy compares it as changed and moves it into <deployPath>/<service>.
func Import(ssh *remote.SSH, srv *config.Server, svc *config.Service, p *Project, message, rolloutVersion string, force bool) (Record, error) {
	if message == "" {
		message = "Imported from " + p.Dir
	}
	rec := Record{
		Version:      "imported",
		Message:      message,
		DeployedAt:   time.Now().UTC().Truncate(time.Second),
		DeployedBy:   deployer(),
		Rollout:      rolloutVersion,
		ImportedFrom: p.Dir,
	}
	forceFlag := "0"
	if force {
		forceFlag = "1"
	}
	script := `set -eu
state=` + shellQuote(srv.StateDir) + `
name=` + shellQuote(svc.Name) + `
record=` + shellQuote(rec.prefix()) + `
mkdir -p "$state"
command -v flock >/dev/null 2>&1 || { echo "rollout: flock is required" >&2; exit 1; }
exec 9>"$state/$name.lock"
flock -w 600 9 || { echo "rollout: timed out waiting for deploy lock" >&2; exit 1; }
if [ -f "$state/$name.json" ] && [ ` + forceFlag + ` != 1 ]; then exit 17; fi
imgs=$(
` + composeAt(srv.DeployPath+"/"+svc.Name, svc.Name, svc.ComposeFile, "exit 3") + `docker compose -p "$name" "$@" images --format json 2>/dev/null | tr -d '\n'
) || imgs=
case "$imgs" in "["*) ;; *) imgs='[]' ;; esac
printf '%s,"images":%s}\n' "$record" "$imgs" > "$state/$name.json.tmp"
mv "$state/$name.json.tmp" "$state/$name.json"
cat "$state/$name.json" >> "$state/$name.history.jsonl"
tail -n 200 "$state/$name.history.jsonl" > "$state/$name.history.tmp"
mv "$state/$name.history.tmp" "$state/$name.history.jsonl"
`
	if _, err := ssh.Output(Target(srv), "sh -c "+shellQuote(script)); err != nil {
		var exit *remote.ExitError
		if errors.As(err, &exit) && exit.Code == 17 {
			return rec, ErrHasRecord
		}
		return rec, fmt.Errorf("writing record: %w", err)
	}
	return rec, nil
}
