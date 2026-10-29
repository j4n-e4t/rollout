package deploy

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseHistory(t *testing.T) {
	rec := func(v, at string) string {
		return `{"version":"` + v + `","hash":"sha256:` + v + `","deployedAt":"` + at + `","deployedBy":"me","rollout":"dev"}`
	}
	out := strings.Join([]string{
		"@host", "Linux", "@os-release", `ID="debian"`, "@service web",
		"@current", rec("v3", "2026-10-29T12:00:00Z"),
		"@history", rec("v1", "2026-10-29T10:00:00Z"), rec("v2", "2026-10-29T11:00:00Z"), rec("v3", "2026-10-29T12:00:00Z"),
		"@service api",
		"@current", rec("v9", "2026-10-29T09:00:00Z"), // history file lost
		"@history",
		"@service new",
		"@current", "",
		"@history", "",
	}, "\n")

	hs := parseHistory(out, "vps-1", 2)
	if len(hs) != 3 {
		t.Fatalf("got %d services", len(hs))
	}
	for _, h := range hs {
		if h.HostOS != "Linux" || h.HostDistro != "debian" {
			t.Fatalf("missing host metadata: %+v", h)
		}
	}
	web := hs[0]
	if web.Server != "vps-1" || len(web.Releases) != 2 || web.Releases[0].Version != "v3" || web.Releases[1].Version != "v2" {
		t.Errorf("web = %+v", web)
	}
	if !web.IsCurrent(web.Releases[0]) || web.IsCurrent(web.Releases[1]) {
		t.Error("only v3 should be current")
	}
	if api := hs[1]; len(api.Releases) != 1 || !api.IsCurrent(api.Releases[0]) {
		t.Errorf("api: current release missing from list: %+v", api)
	}
	if n := hs[2]; n.Current != nil || n.Releases == nil || len(n.Releases) != 0 {
		t.Errorf("new = %+v", n)
	}
}

func TestHistoryScriptIsValidShell(t *testing.T) {
	script := historyScript("/opt/roll out", []string{"web", "a'b"}, 20)
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s\n%s", err, out, script)
	}
	if !strings.Contains(script, "tail -n 20") {
		t.Errorf("script: %s", script)
	}
}

func TestInvalidHistoryLimit(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := History(nil, nil, nil, n); err == nil {
			t.Fatal("invalid limit must fail before SSH")
		}
		if got := parseHistory("@service web\n@current\n{\"hash\":\"x\"}\n", "s", n); len(got) != 0 {
			t.Fatal("invalid limit should not produce releases", got)
		}
	}
}
