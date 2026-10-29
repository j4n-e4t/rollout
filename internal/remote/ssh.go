// Package remote runs commands on servers through the system ssh binary, so
// ~/.ssh/config, ssh-agent, ProxyJump and Tailscale SSH all work as usual.
package remote

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Options configure how ssh is invoked.
type Options struct {
	// IdentityFiles are passed to ssh with -i.
	IdentityFiles []string
	// InsecureIgnoreHostKey disables host key verification entirely.
	InsecureIgnoreHostKey bool
	// BatchMode makes ssh fail instead of prompting (for CI).
	BatchMode bool
}

// Target is a server to connect to. Empty User and zero Port leave the choice
// to ssh and its config.
type Target struct {
	Name    string
	Host    string
	Port    int
	User    string
	HostKey string // optional pinned key in authorized_keys format
}

// SSH runs commands on targets. Call Close to remove its temporary files.
type SSH struct {
	opts Options
	dir  string // temp dir for the CI key and pinned known_hosts files
}

// New prepares an SSH runner. If ROLLOUT_SSH_KEY is set, its contents are
// written to a private temp file and used as an identity (handy for CI
// secrets).
func New(opts Options) (*SSH, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, errors.New("ssh not found in PATH")
	}
	dir, err := os.MkdirTemp("", "rollout-ssh-")
	if err != nil {
		return nil, err
	}
	s := &SSH{opts: opts, dir: dir}
	if key := os.Getenv("ROLLOUT_SSH_KEY"); key != "" {
		if !strings.HasSuffix(key, "\n") {
			key += "\n" // OpenSSH rejects keys without a trailing newline
		}
		p := filepath.Join(dir, "id_ci")
		if err := os.WriteFile(p, []byte(key), 0o600); err != nil {
			s.Close()
			return nil, err
		}
		s.opts.IdentityFiles = append([]string{p}, s.opts.IdentityFiles...)
	}
	return s, nil
}

func (s *SSH) Close() error { return os.RemoveAll(s.dir) }

// Run executes cmd on t, wiring up the given streams.
func (s *SSH) Run(t Target, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	args, err := s.args(t)
	if err != nil {
		return err
	}
	c := exec.Command("ssh", append(args, cmd)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return describe(t, c.Run())
}

// Interactive runs cmd on t attached to this process's terminal. With tty,
// ssh allocates a remote pseudo-terminal (needed for shells).
func (s *SSH) Interactive(t Target, cmd string, tty bool) error {
	args, err := s.args(t)
	if err != nil {
		return err
	}
	if tty {
		args = append([]string{"-t"}, args...)
	}
	c := exec.Command("ssh", append(args, cmd)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return describe(t, c.Run())
}

// Output executes cmd on t and returns its stdout. Stderr is included in the
// error on failure.
func (s *SSH) Output(t Target, cmd string) ([]byte, error) {
	var stdout, stderr strings.Builder
	err := s.Run(t, cmd, nil, &stdout, &stderr)
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return []byte(stdout.String()), err
}

func (s *SSH) args(t Target) ([]string, error) {
	args := []string{"-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15"}
	if s.opts.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	for _, id := range s.opts.IdentityFiles {
		args = append(args, "-i", id)
	}
	switch {
	case t.HostKey != "":
		// Verify against the pinned key only, via an alias so the
		// known_hosts line doesn't depend on host/port formatting.
		alias := "rollout-" + t.Name
		p := filepath.Join(s.dir, "known_hosts_"+t.Name)
		line := alias + " " + strings.TrimSpace(t.HostKey) + "\n"
		if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
			return nil, err
		}
		args = append(args,
			"-o", "HostKeyAlias="+alias,
			"-o", "UserKnownHostsFile="+p,
			"-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "StrictHostKeyChecking=yes")
	case s.opts.InsecureIgnoreHostKey:
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}
	return append(args, "--", t.Host), nil
}

// ExitError is returned when the remote command ran and exited non-zero.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

func describe(t Target, err error) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	// ssh itself exits 255 on connection and authentication failures.
	if exit.ExitCode() == 255 {
		return fmt.Errorf("ssh to %s failed (see ssh output above)", t.Host)
	}
	return &ExitError{Code: exit.ExitCode()}
}
