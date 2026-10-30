package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// ErrNotFound reports a secret that does not exist in setec.
var ErrNotFound = errors.New("secret not found")

// SetecRef names the setec secret a ${NAME} reference is filled from.
type SetecRef struct {
	Name string
	// Optional treats a secret missing from setec as unset, so the
	// reference's default applies, instead of failing.
	Optional bool
}

// Setec fetches secrets from a setec server (github.com/tailscale/setec).
// setec authorizes callers by their Tailscale identity, so the deploying
// machine must be on the tailnet and granted "get" on the secrets.
type Setec struct {
	Server string
	Client *http.Client
}

// Get returns the active value of the named secret.
func (s *Setec) Get(ctx context.Context, name string) (string, error) {
	if s.Server == "" {
		return "", errors.New("no setec server: set setec.server in rollout.json or SETEC_SERVER")
	}
	body, err := json.Marshal(struct{ Name string }{name})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Server, "/")+"/api/get", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Required by setec so browser scripts cannot call the API.
	req.Header.Set("Sec-X-Tailscale-No-Browsers", "setec")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("setec %q: %w", name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("setec %q: %w", name, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return "", fmt.Errorf("setec %q: access denied (grant get in the tailnet policy's tailscale.com/cap/secrets)", name)
	case http.StatusNotFound:
		return "", fmt.Errorf("setec %q: %w", name, ErrNotFound)
	default:
		return "", fmt.Errorf("setec %q: %s: %s", name, resp.Status, strings.TrimSpace(string(data)))
	}
	// Value is []byte, which encoding/json carries as base64.
	var v struct{ Value []byte }
	if err := json.Unmarshal(data, &v); err != nil {
		return "", fmt.Errorf("setec %q: decode response: %w", name, err)
	}
	return string(v.Value), nil
}

// Resolver takes names mapped to a setec secret from setec only, so a stray
// environment variable cannot shadow them. Other names come from the process
// environment, then ROLLOUT_SECRETS. Setec values are fetched on first use,
// so only secrets the deployed services reference are read.
type Resolver struct {
	setec *Setec
	refs  map[string]SetecRef // keyed by secret name
	bag   map[string]string   // ROLLOUT_SECRETS

	fetched map[string]string
	absent  map[string]bool // optional secrets missing from setec
	errs    map[string]error
}

// NewResolver returns a resolver for the names in refs mapped to setec
// secrets. refs may be empty; then setec is not used.
func NewResolver(setec *Setec, refs map[string]SetecRef) (*Resolver, error) {
	r := &Resolver{setec: setec, refs: refs, fetched: map[string]string{}, absent: map[string]bool{}, errs: map[string]error{}}
	if raw := os.Getenv(EnvVar); strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &r.bag); err != nil {
			return nil, fmt.Errorf("%s must be a JSON object of strings: %w", EnvVar, err)
		}
	}
	return r, nil
}

// Lookup implements Lookup. A setec secret that cannot be fetched is not set;
// Err reports why.
func (r *Resolver) Lookup(name string) (string, bool) {
	if ref, ok := r.refs[name]; ok {
		if v, ok := r.fetched[name]; ok {
			return v, true
		}
		if _, failed := r.errs[name]; failed || r.absent[name] {
			return "", false
		}
		v, err := r.setec.Get(context.Background(), ref.Name)
		switch {
		case ref.Optional && errors.Is(err, ErrNotFound):
			r.absent[name] = true
			return "", false
		case err != nil:
			r.errs[name] = err
			return "", false
		}
		r.fetched[name] = v
		return v, true
	}
	if v, ok := os.LookupEnv(name); ok {
		return v, true
	}
	v, ok := r.bag[name]
	return v, ok
}

// Source describes where Lookup takes name from: "setec:<name>", "env",
// EnvVar, or "" when it is not set anywhere.
func (r *Resolver) Source(name string) string {
	if ref, ok := r.refs[name]; ok {
		if r.absent[name] {
			return ""
		}
		return "setec:" + ref.Name
	}
	if _, ok := os.LookupEnv(name); ok {
		return "env"
	}
	if _, ok := r.bag[name]; ok {
		return EnvVar
	}
	return ""
}

// Err reports the setec secrets that could not be fetched.
func (r *Resolver) Err() error {
	names := make([]string, 0, len(r.errs))
	for n := range r.errs {
		names = append(names, n)
	}
	slices.Sort(names)
	errs := make([]error, 0, len(names))
	for _, n := range names {
		errs = append(errs, fmt.Errorf("%s: %w", n, r.errs[n]))
	}
	return errors.Join(errs...)
}
