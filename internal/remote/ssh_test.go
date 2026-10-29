package remote

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	s := &SSH{opts: Options{IdentityFiles: []string{"/k"}, BatchMode: true}, dir: t.TempDir()}

	args, err := s.args(Target{Name: "web-1", Host: "web-1.tailnet.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-p ") || strings.Contains(joined, "-l ") {
		t.Errorf("port/user should be left to ssh config: %s", joined)
	}
	if !strings.Contains(joined, "BatchMode=yes") || !strings.Contains(joined, "-i /k") {
		t.Errorf("missing options: %s", joined)
	}
	if !slices.Equal(args[len(args)-2:], []string{"--", "web-1.tailnet.ts.net"}) {
		t.Errorf("host must come last after --: %v", args)
	}

	args, err = s.args(Target{Name: "web-1", Host: "203.0.113.10", Port: 2222, User: "deploy", HostKey: "ssh-ed25519 AAAAkey"})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(args, " ")
	for _, want := range []string{"-p 2222", "-l deploy", "HostKeyAlias=rollout-web-1", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	kh, err := os.ReadFile(s.dir + "/known_hosts_web-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(kh) != "rollout-web-1 ssh-ed25519 AAAAkey\n" {
		t.Errorf("known_hosts = %q", kh)
	}
}
