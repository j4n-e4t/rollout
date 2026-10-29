package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestModel(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services/web/html"), 0o755)
	os.WriteFile(filepath.Join(dir, "services/web/compose.yaml"), []byte(`
services:
  nginx:
    image: nginx:alpine
    depends_on: [api]
  api:
    image: app:1
`), 0o644)
	os.WriteFile(filepath.Join(dir, "services/web/html/index.html"), []byte("hi"), 0o644)
	cfgPath := filepath.Join(dir, "rollout.json")
	os.WriteFile(cfgPath, []byte(`{"output":{"showUnconfigured":true},"servers": {"vps-1": {"host": "vps-1.ts.net"}}, "services": {"web": {"servers": ["vps-1"]}}}`), 0o644)

	h := Handler(cfgPath, nil, "example.com", "test-token")
	request := func(path string) *http.Request {
		r := httptest.NewRequest("GET", "http://example.com"+path, nil)
		r.Header.Set("X-Rollout-Token", "test-token")
		return r
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("/api/model"))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var m model
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if !m.ShowUnconfigured {
		t.Error("showUnconfigured preference missing from model")
	}
	if len(m.Servers) != 1 || m.Servers[0].Host != "vps-1.ts.net" {
		t.Errorf("servers = %+v", m.Servers)
	}
	web := m.Services[0]
	if web.Hash == "" || len(web.Files) != 2 || web.Topology == nil || len(web.Topology.Services) != 2 {
		t.Fatalf("web = %+v", web)
	}
	if nginx := web.Topology.Services[1]; nginx.Name != "nginx" || nginx.DependsOn[0] != "api" || nginx.Networks[0] != "web_default" {
		t.Errorf("nginx = %+v", nginx)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, request("/"))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("index: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, request("/api/history?server=vps-1&service=../etc"))
	if rec.Code != 400 {
		t.Errorf("history with bad service name: %d", rec.Code)
	}
}

func TestDashboardAccess(t *testing.T) {
	h := Handler("does-not-exist", nil, "127.0.0.1:7878", "secret")
	for _, tt := range []struct{ name, host, origin, token, site string }{
		{"rebinding", "attacker.example:7878", "", "secret", ""},
		{"foreign origin", "127.0.0.1:7878", "http://attacker.example", "secret", ""},
		{"missing token", "127.0.0.1:7878", "", "", ""},
		{"wrong token", "127.0.0.1:7878", "", "wrong", ""},
		{"cross site", "127.0.0.1:7878", "", "secret", "cross-site"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range []string{"/api/model", "/api/status", "/api/history?server=s&service=web"} {
				r := httptest.NewRequest("GET", "http://"+tt.host+path, nil)
				r.Header.Set("Origin", tt.origin)
				r.Header.Set("X-Rollout-Token", tt.token)
				r.Header.Set("Sec-Fetch-Site", tt.site)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusForbidden {
					t.Fatalf("%s: got %d", path, w.Code)
				}
			}
		})
	}
}
