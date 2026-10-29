// Package dashboard serves a local, read-only web view of the environment:
// the configuration from rollout.json, the desired topology from each
// service's compose file, and the live state of every server.
package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/j4n-e4t/rollout/internal/bundle"
	"github.com/j4n-e4t/rollout/internal/compose"
	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/deploy"
	"github.com/j4n-e4t/rollout/internal/remote"
)

//go:embed index.html
var indexHTML []byte

// Handler serves the dashboard. The config is reloaded on every request so
// edits to rollout.json and service files show up on refresh.
func Handler(configPath string, ssh *remote.SSH, authority, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(configPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, buildModel(cfg, configPath))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(configPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, deploy.Status(cfg, ssh, nil))
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(configPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		srv, ok := cfg.Servers[r.URL.Query().Get("server")]
		svc := r.URL.Query().Get("service")
		// The service may be gone from the config but still on the server.
		if !ok || !config.ValidName(svc) {
			http.Error(w, "unknown server or service", http.StatusBadRequest)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 200 {
			n = 30
		}
		hist, err := deploy.History(ssh, srv, []string{svc}, n)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, hist[0].Releases)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		origin := r.Header.Get("Origin")
		if !strings.EqualFold(r.Host, authority) || (origin != "" && origin != "http://"+authority) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && (token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Rollout-Token")), []byte(token)) != 1) {
			http.Error(w, "invalid dashboard session", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// SessionToken is an unguessable credential for one dashboard process.
func SessionToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type model struct {
	ShowUnconfigured bool           `json:"showUnconfigured"`
	ConfigPath       string         `json:"configPath"`
	Servers          []serverModel  `json:"servers"`
	Services         []serviceModel `json:"services"`
}

type serverModel struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	Host       string `json:"host"`
	DeployPath string `json:"deployPath"`
	Pinned     bool   `json:"pinned"`
}

type serviceModel struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	ComposeFile string           `json:"composeFile"`
	Compose     string           `json:"compose"`
	Servers     []string         `json:"servers"`
	Files       []string         `json:"files"`
	Wait        bool             `json:"wait"`
	Hash        string           `json:"hash"` // of the local bundle, to compare with deploy records
	Topology    *compose.Project `json:"topology,omitempty"`
	Error       string           `json:"error,omitempty"`
}

func buildModel(cfg *config.Config, configPath string) model {
	m := model{ConfigPath: configPath, ShowUnconfigured: cfg.Output.ShowUnconfigured}
	for _, name := range cfg.ServerNames() {
		s := cfg.Servers[name]
		m.Servers = append(m.Servers, serverModel{Name: name, Address: s.Address(), Host: s.Host, DeployPath: s.DeployPath, Pinned: s.HostKey != ""})
	}
	for _, name := range cfg.ServiceNames() {
		svc := cfg.Services[name]
		dir := cfg.ServiceDir(svc)
		sm := serviceModel{
			Name: name, Path: svc.Path, ComposeFile: svc.ComposeFile,
			Servers: svc.Servers, Files: listFiles(dir), Wait: *svc.Wait,
		}
		composePath := filepath.Join(dir, svc.ComposeFile)
		if data, err := os.ReadFile(composePath); err == nil {
			sm.Compose = string(data)
		}
		if p, err := compose.Load(composePath, name); err == nil {
			sm.Topology = p
		} else {
			sm.Error = err.Error()
		}
		if h, err := bundle.Hash(dir, "composeFile="+svc.ComposeFile); err == nil {
			sm.Hash = h
		}
		m.Services = append(m.Services, sm)
	}
	return m
}

func listFiles(dir string) []string {
	var files []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(dir, path); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}
