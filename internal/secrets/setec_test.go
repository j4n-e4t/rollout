package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSetec serves /api/get from values; names in denied report 403.
func fakeSetec(t *testing.T, values map[string]string, denied ...string) (*Setec, map[string]int) {
	t.Helper()
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/get" || r.Header.Get("Sec-X-Tailscale-No-Browsers") != "setec" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		calls[req.Name]++
		for _, d := range denied {
			if d == req.Name {
				http.Error(w, "access denied", http.StatusForbidden)
				return
			}
		}
		v, ok := values[req.Name]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(struct {
			Value   []byte
			Version int
		}{[]byte(v), 1})
	}))
	t.Cleanup(srv.Close)
	return &Setec{Server: srv.URL + "/", Client: srv.Client()}, calls
}

func TestSetecGet(t *testing.T) {
	s, _ := fakeSetec(t, map[string]string{"prod/db": "p$w\"d\n"}, "prod/denied")
	if v, err := s.Get(context.Background(), "prod/db"); err != nil || v != "p$w\"d\n" {
		t.Errorf("Get = %q, %v", v, err)
	}
	if _, err := s.Get(context.Background(), "prod/denied"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("denied: err = %v", err)
	}
	if _, err := s.Get(context.Background(), "prod/nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing: err = %v", err)
	}
	if _, err := (&Setec{}).Get(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "no setec server") {
		t.Errorf("no server: err = %v", err)
	}
}

func TestResolver(t *testing.T) {
	s, calls := fakeSetec(t, map[string]string{"prod/db": "from-setec"}, "prod/denied")
	t.Setenv(EnvVar, `{"FROM_JSON":"j","DB":"json"}`)
	t.Setenv("DB", "env")
	t.Setenv("FROM_ENV", "e")
	t.Setenv("DENIED", "env")
	r, err := NewResolver(s, map[string]SetecRef{
		"DB":       {Name: "prod/db"},
		"DENIED":   {Name: "prod/denied"},
		"GONE":     {Name: "prod/gone"},
		"OPTIONAL": {Name: "prod/gone", Optional: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, want, source string }{
		{"DB", "from-setec", "setec:prod/db"},
		{"FROM_ENV", "e", "env"},
		{"FROM_JSON", "j", EnvVar},
	} {
		if v, ok := r.Lookup(tt.name); !ok || v != tt.want {
			t.Errorf("Lookup(%s) = %q, %v; want %q", tt.name, v, ok, tt.want)
		}
		if got := r.Source(tt.name); got != tt.source {
			t.Errorf("Source(%s) = %q, want %q", tt.name, got, tt.source)
		}
	}
	r.Lookup("DB")
	if calls["prod/db"] != 1 {
		t.Errorf("setec calls = %v, want one fetch of prod/db", calls)
	}
	if r.Err() != nil {
		t.Errorf("Err = %v before any failure", r.Err())
	}
	if _, ok := r.Lookup("DENIED"); ok {
		t.Error("DENIED should not be set, even though the environment has it")
	}
	r.Lookup("DENIED")
	if calls["prod/denied"] != 1 {
		t.Errorf("failed fetch retried: %d calls", calls["prod/denied"])
	}
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "DENIED") {
		t.Errorf("Err = %v, want it to name DENIED", err)
	}

	// An optional secret missing from setec is unset without an error; a
	// required one is an error.
	if _, ok := r.Lookup("OPTIONAL"); ok || r.Source("OPTIONAL") != "" {
		t.Errorf("OPTIONAL should be unset, source %q", r.Source("OPTIONAL"))
	}
	if err := r.Err(); strings.Contains(err.Error(), "OPTIONAL") {
		t.Errorf("Err = %v, want no error for OPTIONAL", err)
	}
	r.Lookup("GONE")
	if err := r.Err(); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "GONE") {
		t.Errorf("Err = %v, want not found for GONE", err)
	}
}
