package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
)

// Record describes the release of a service that is live on a server. The
// deploy script writes it to <stateDir>/<service>.json after the containers
// are up, and appends it to <service>.history.jsonl, so every deploy user
// (laptop or CI) sees the same state.
type Record struct {
	// Version is the optional name passed with deploy --release.
	Version string `json:"version,omitempty"`
	// Message describes the deploy, like a commit message: deploy -m, else
	// the subject of the Git commit.
	Message string `json:"message,omitempty"`
	// Commit is the Git HEAD of the environment repo; Dirty is set when the
	// service directory had uncommitted changes.
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Hash covers every shipped file plus the compose file name; equal hashes
	// mean equal bundles.
	Hash string `json:"hash"`
	// Secrets is a salted digest of the secret values rendered into the
	// service's env files; a new value redeploys the service.
	Secrets    string    `json:"secrets,omitempty"`
	DeployedAt time.Time `json:"deployedAt"`
	DeployedBy string    `json:"deployedBy"`
	// Source links to the CI run that deployed, when there is one.
	Source string `json:"source,omitempty"`
	// ImportedFrom is the directory an imported project ran from when
	// `rollout import` adopted it; empty for regular deploys.
	ImportedFrom string  `json:"importedFrom,omitempty"`
	Rollout      string  `json:"rollout"`
	Images       []Image `json:"images,omitempty"`
}

// Image is a container's image as reported by `docker compose images`.
type Image struct {
	Container  string `json:"ContainerName"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	ID         string `json:"ID"`
}

// Ref returns repository:tag.
func (i Image) Ref() string {
	if i.Tag == "" {
		return i.Repository
	}
	return i.Repository + ":" + i.Tag
}

// Release is a short human label: the version name, else the short commit.
func (r *Record) Release() string {
	if r == nil {
		return ""
	}
	label := r.Version
	if label == "" && len(r.Commit) >= 7 {
		label = r.Commit[:7]
	}
	if label == "" {
		label = ShortHash(r.Hash)
	}
	if r.Dirty {
		label += "-dirty"
	}
	return label
}

// newRecord describes a release about to be deployed. Images are filled in on
// the server once the containers run.
func newRecord(cfg *config.Config, svc *config.Service, hash, version, message, rolloutVersion string) Record {
	r := Record{
		Version:    version,
		Message:    strings.TrimSpace(message),
		Hash:       hash,
		DeployedAt: time.Now().UTC().Truncate(time.Second),
		DeployedBy: deployer(),
		Rollout:    rolloutVersion,
	}
	if out, err := exec.Command("git", "-C", cfg.Dir, "rev-parse", "HEAD").Output(); err == nil {
		r.Commit = strings.TrimSpace(string(out))
		rel, err := filepath.Rel(cfg.Dir, cfg.ServiceDir(svc))
		if err == nil {
			out, err := exec.Command("git", "-C", cfg.Dir, "status", "--porcelain", "--", rel).Output()
			r.Dirty = err == nil && len(strings.TrimSpace(string(out))) > 0
		}
		if r.Message == "" {
			if out, err := exec.Command("git", "-C", cfg.Dir, "log", "-1", "--format=%s").Output(); err == nil {
				r.Message = strings.TrimSpace(string(out))
			}
		}
	}
	if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
		r.Source = os.Getenv("GITHUB_SERVER_URL") + "/" + os.Getenv("GITHUB_REPOSITORY") + "/actions/runs/" + id
	}
	return r
}

func deployer() string {
	if actor := os.Getenv("GITHUB_ACTOR"); actor != "" {
		return actor + " (github actions)"
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return name + "@" + hostname()
}

// hostname returns a stable machine name. On macOS os.Hostname follows the
// network (DHCP/mDNS) and changes between deploys; LocalHostName doesn't.
func hostname() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "LocalHostName").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return strings.TrimSpace(string(out))
		}
	}
	host, _ := os.Hostname()
	return strings.TrimSuffix(host, ".local")
}

// prefix returns the record's JSON without the closing brace, so the server
// can append the "images" field it collects after `compose up`.
func (r Record) prefix() string {
	b, err := json.Marshal(r)
	if err != nil {
		panic(err) // plain struct, cannot fail
	}
	return strings.TrimSuffix(string(b), "}")
}

// Subject returns the first line of the message.
func (r *Record) Subject() string {
	if r == nil {
		return ""
	}
	s, _, _ := strings.Cut(r.Message, "\n")
	return s
}

// ShortHash trims "sha256:" and shortens a hash or image ID for display.
func ShortHash(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	return h[:min(12, len(h))]
}

// ServiceHistory is the release history of one service on one server.
type ServiceHistory struct {
	Service    string `json:"service"`
	Server     string `json:"server"`
	HostOS     string `json:"hostOS,omitempty"`
	HostDistro string `json:"hostDistro,omitempty"`
	// Current is the live release from <service>.json; nil if never deployed.
	Current *Record `json:"current,omitempty"`
	// Releases are past deploys from <service>.history.jsonl, newest first.
	Releases []Record `json:"releases"`
}

// IsCurrent reports whether r is the live release.
func (h ServiceHistory) IsCurrent(r Record) bool {
	return h.Current != nil && h.Current.Hash == r.Hash && h.Current.DeployedAt.Equal(r.DeployedAt)
}

// History reads the live record and up to n past releases of each service on
// srv, in one SSH session.
func History(ssh *remote.SSH, srv *config.Server, services []string, n int) ([]ServiceHistory, error) {
	if n <= 0 {
		return nil, fmt.Errorf("history limit must be greater than zero")
	}
	out, err := ssh.Output(Target(srv), "sh -c "+shellQuote(historyScript(srv.StateDir, services, n)))
	if err != nil {
		return nil, err
	}
	return parseHistory(string(out), srv.Name, n), nil
}

// historyScript prints, per service, its live record and the last n history
// lines, in sections parseHistory understands.
func historyScript(stateDir string, services []string, n int) string {
	var b strings.Builder
	b.WriteString(hostProbe)
	b.WriteString("dir=" + shellQuote(stateDir) + "\n")
	for _, svc := range services {
		q := shellQuote(svc)
		b.WriteString("echo @service " + q + "\n")
		b.WriteString("echo @current; [ -f \"$dir\"/" + q + ".json ] && tr -d '\\n' < \"$dir\"/" + q + ".json; echo\n")
		b.WriteString("echo @history; [ -f \"$dir\"/" + q + ".history.jsonl ] && tail -n " + strconv.Itoa(n) + " \"$dir\"/" + q + ".history.jsonl\n")
	}
	b.WriteString("exit 0\n")
	return b.String()
}

func parseHistory(out, server string, n int) []ServiceHistory {
	var all []ServiceHistory
	if n <= 0 {
		return all
	}
	hostOS, hostDistro := parseHostInfo(out)
	var cur *ServiceHistory
	section := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "@service "):
			all = append(all, ServiceHistory{Service: strings.TrimPrefix(line, "@service "), Server: server, HostOS: hostOS, HostDistro: hostDistro})
			cur, section = &all[len(all)-1], ""
		case line == "@current" || line == "@history":
			section = line
		case cur != nil && strings.TrimSpace(line) != "":
			var r Record
			if json.Unmarshal([]byte(line), &r) != nil {
				continue
			}
			if section == "@current" {
				cur.Current = &r
			} else {
				cur.Releases = append(cur.Releases, r)
			}
		}
	}
	for i := range all {
		h := &all[i]
		slices.Reverse(h.Releases)
		// The live release is normally the newest history entry; keep it
		// listed even if the history file was trimmed or lost.
		if h.Current != nil && !slices.ContainsFunc(h.Releases, h.IsCurrent) {
			h.Releases = append([]Record{*h.Current}, h.Releases...)
		}
		if len(h.Releases) > n {
			h.Releases = h.Releases[:n]
		}
		if h.Releases == nil {
			h.Releases = []Record{}
		}
	}
	return all
}
