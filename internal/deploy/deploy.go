// Package deploy plans and executes rollouts of services to servers.
package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/j4n-e4t/rollout/internal/bundle"
	"github.com/j4n-e4t/rollout/internal/config"
	"github.com/j4n-e4t/rollout/internal/remote"
	"github.com/j4n-e4t/rollout/internal/secrets"
)

// Change says how a service's local bundle compares to what a server runs.
type Change string

const (
	ChangeNew        Change = "new"
	ChangeExisting   Change = "existing" // runs on the server, but rollout has no record of it
	ChangeChanged    Change = "changed"
	ChangeSecrets    Change = "secrets changed"
	ChangeUnchanged  Change = "unchanged"
	ChangeNotRunning Change = "unchanged, not running"
	ChangeUnknown    Change = "unknown" // server unreachable or not checked
)

// Item is one service on one server in a plan.
type Item struct {
	Service *config.Service
	Hash    string // hash of the local bundle
	Secrets *secrets.Rendered
	Change  Change
	Current *Record // live release on the server, if any
	// RunsFrom is the directory an existing, unrecorded project runs from.
	RunsFrom string
	Error    string // why the server state is unknown
}

// Deploy reports whether the item needs deploying. Unchanged services that
// still run are skipped unless force is set; everything else is deployed.
func (it Item) Deploy(force bool) bool {
	return force || it.Change != ChangeUnchanged
}

// Step deploys a set of services to one server, in order.
type Step struct {
	Server *config.Server
	Items  []Item
}

// Plan resolves which services go to which servers, as configured in the
// environment file, hashes each service's bundle and renders its env files
// with secrets from lookup. It fails if a required secret is not set. Call
// Check to compare against the servers.
func Plan(cfg *config.Config, services []string, lookup secrets.Lookup) ([]Step, error) {
	hashes := map[string]string{}
	rendered := map[string]*secrets.Rendered{}
	var missing []string
	byServer := map[string][]Item{}
	for _, name := range services {
		svc, ok := cfg.Services[name]
		if !ok {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		if _, ok := hashes[name]; !ok {
			h, err := bundle.Hash(cfg.ServiceDir(svc), "composeFile="+svc.ComposeFile)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			hashes[name] = h
			r, err := secrets.Render(cfg.ServiceDir(svc), name, lookup)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			if m := r.Missing(); len(m) > 0 {
				missing = append(missing, fmt.Sprintf("  %s: %s", name, strings.Join(m, ", ")))
			}
			rendered[name] = r
		}
		for _, srv := range svc.Servers {
			if !contains(byServer[srv], svc) {
				byServer[srv] = append(byServer[srv], Item{Service: svc, Hash: hashes[name], Secrets: rendered[name], Change: ChangeUnknown})
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("secrets not set:\n%s\nset them as environment variables, or pass repository secrets to the GitHub Action (see rollout secrets)", strings.Join(missing, "\n"))
	}
	names := make([]string, 0, len(byServer))
	for n := range byServer {
		names = append(names, n)
	}
	sort.Strings(names)
	steps := make([]Step, 0, len(names))
	for _, n := range names {
		steps = append(steps, Step{Server: cfg.Servers[n], Items: byServer[n]})
	}
	return steps, nil
}

// Check reads the live release records from the servers and sets each item's
// Change.
func Check(cfg *config.Config, ssh *remote.SSH, steps []Step) {
	var services []string
	for _, st := range steps {
		for _, it := range st.Items {
			services = append(services, it.Service.Name)
		}
	}
	live := map[string]ServiceStatus{}
	for _, row := range Status(cfg, ssh, services) {
		live[row.Server+"/"+row.Service] = row
	}
	for i := range steps {
		for j := range steps[i].Items {
			it := &steps[i].Items[j]
			row, ok := live[steps[i].Server.Name+"/"+it.Service.Name]
			switch {
			case !ok || row.State == StateUnreachable:
				it.Change, it.Error = ChangeUnknown, row.Error
			case row.Record == nil && len(row.Containers) > 0:
				it.Change, it.RunsFrom = ChangeExisting, row.Containers[0].Dir
			case row.Record == nil:
				it.Change = ChangeNew
			case row.Record.Hash != it.Hash:
				it.Change, it.Current = ChangeChanged, row.Record
			case row.Record.Secrets != it.Secrets.Digest:
				it.Change, it.Current = ChangeSecrets, row.Record
			case row.State != StateRunning:
				it.Change, it.Current = ChangeNotRunning, row.Record
			default:
				it.Change, it.Current = ChangeUnchanged, row.Record
			}
		}
	}
}

// Target converts a configured server into an ssh target.
func Target(s *config.Server) remote.Target {
	return remote.Target{Name: s.Name, Host: s.Host, Port: s.Port, User: s.User, HostKey: s.HostKey}
}

// Options control execution.
type Options struct {
	SSH         *remote.SSH
	WaitTimeout int // seconds; 0 uses compose's default
	Pull        PullPolicy
	Force       bool   // deploy unchanged services too
	Version     string // optional release name stored in the record
	Message     string // deploy message; defaults to the Git commit subject
	Rollout     string // rollout's own version, stored in the record
	Out         io.Writer
}

// Result counts what Run did.
type Result struct {
	Deployed, Skipped int
}

// Run executes the plan: servers in parallel, services on a server in order.
// A failing service doesn't stop the others; all errors are returned joined.
func Run(cfg *config.Config, steps []Step, opts Options) (Result, error) {
	out := &syncWriter{w: opts.Out}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		res  Result
		errs []error
	)
	for _, step := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, it := range step.Items {
				prefix := fmt.Sprintf("[%s/%s] ", step.Server.Name, it.Service.Name)
				if !it.Deploy(opts.Force) {
					fmt.Fprintf(out, "%sunchanged (%s), skipping\n", prefix, it.Current.Release())
					mu.Lock()
					res.Skipped++
					mu.Unlock()
					continue
				}
				err := deployService(cfg, step.Server, it, opts, out, prefix)
				mu.Lock()
				if err != nil {
					fmt.Fprintf(out, "%sFAILED: %v\n", prefix, err)
					errs = append(errs, fmt.Errorf("%s/%s: %w", step.Server.Name, it.Service.Name, err))
				} else {
					res.Deployed++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return res, errors.Join(errs...)
}

func deployService(cfg *config.Config, srv *config.Server, it Item, opts Options, out *syncWriter, prefix string) error {
	svc := it.Service
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(bundle.Write(pw, cfg.ServiceDir(svc), it.Secrets.Files)) }()
	// Unblock the bundle writer if the remote side stops reading early.
	defer pr.Close()

	stdout := &PrefixWriter{prefix: prefix, out: out}
	stderr := &PrefixWriter{prefix: prefix, out: out}
	defer stdout.Flush()
	defer stderr.Flush()

	script := deployScript{
		Server:      srv.Name,
		BeforeHooks: []string{cfg.Hooks.BeforeDeploy, svc.Hooks.BeforeDeploy},
		AfterHooks:  []string{svc.Hooks.AfterDeploy, cfg.Hooks.AfterDeploy},
		Base:        srv.DeployPath,
		State:       srv.StateDir,
		Name:        svc.Name,
		Compose:     svc.ComposeFile,
		Preserve:    svc.Preserve,
		Wait:        *svc.Wait,
		WaitTimeout: opts.WaitTimeout,
		Pull:        opts.Pull,
		Record:      newRecord(cfg, svc, it.Hash, opts.Version, opts.Message, opts.Rollout),
	}
	script.Record.Secrets = it.Secrets.Digest
	return opts.SSH.Run(Target(srv), script.command(), pr, stdout, stderr)
}

func contains(list []Item, s *config.Service) bool {
	for _, x := range list {
		if x.Service == s {
			return true
		}
	}
	return false
}

// NewSyncWriter makes w safe for concurrent writers.
func NewSyncWriter(w io.Writer) io.Writer { return &syncWriter{w: w} }

// NewPrefixWriter prefixes every line written to it; call Flush at the end.
func NewPrefixWriter(prefix string, out io.Writer) *PrefixWriter {
	return &PrefixWriter{prefix: prefix, out: out}
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// PrefixWriter prefixes each complete line so parallel output stays readable.
type PrefixWriter struct {
	prefix string
	out    io.Writer
	buf    bytes.Buffer
}

func (p *PrefixWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)
	for {
		i := bytes.IndexByte(p.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := p.buf.Next(i + 1)
		if _, err := fmt.Fprintf(p.out, "%s%s", p.prefix, line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (p *PrefixWriter) Flush() {
	if p.buf.Len() > 0 {
		fmt.Fprintf(p.out, "%s%s\n", p.prefix, p.buf.Bytes())
		p.buf.Reset()
	}
}
