package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHostInfo(t *testing.T) {
	for _, tt := range []struct{ name, response, os, distro string }{
		{"Ubuntu", "@host\nLinux\n@os-release\nNAME=Ubuntu\nID=ubuntu\nID_LIKE=debian\n@dirs\nweb\n", "Linux", "ubuntu"},
		{"quoted Debian", "@host\nLinux\n@os-release\nID=\"debian\"\n@service web\n", "Linux", "debian"},
		{"single quoted", "@host\nLinux\n@os-release\nID='alpine'\n", "Linux", "alpine"},
		{"missing release", "@host\nLinux\n@os-release\n@dirs\n", "Linux", ""},
		{"unknown distro", "@host\nLinux\n@os-release\nID=custom-linux\nID_LIKE=debian\n", "Linux", "custom-linux"},
		{"ID_LIKE only", "@host\nLinux\n@os-release\nID_LIKE=debian\n", "Linux", ""},
		{"malformed", "@host\nLinux\n@os-release\nID=\"$(touch sentinel)\"\n", "Linux", ""},
		{"section boundary", "@host\nLinux\n@os-release\n@dirs\nID=debian\n", "Linux", ""},
		{"mac", "@host\nDarwin\n@os-release\n@service web\n", "Darwin", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hostOS, distro := parseHostInfo(tt.response)
			if hostOS != tt.os || distro != tt.distro {
				t.Fatalf("got %q/%q want %q/%q", hostOS, distro, tt.os, tt.distro)
			}
		})
	}
}

func TestHostProbeReadsWithoutExecuting(t *testing.T) {
	dir := t.TempDir()
	etc, vendor := filepath.Join(dir, "etc-release"), filepath.Join(dir, "vendor-release")
	sentinel := filepath.Join(dir, "executed")
	// If the file were sourced this would create sentinel. Reading it must not.
	data := "ID=ubuntu\nNAME=$(touch " + sentinel + ")\n"
	if err := os.WriteFile(etc, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vendor, []byte("ID=debian\n"), 0644); err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer("/etc/os-release", shellQuote(etc), "/usr/lib/os-release", shellQuote(vendor)).Replace(hostProbe)
	for _, want := range []string{"ubuntu", "debian", ""} {
		out, err := exec.Command("sh", "-eu", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("probe failed: %v: %s", err, out)
		}
		_, distro := parseHostInfo(string(out))
		if distro != want {
			t.Fatalf("got %q want %q: %s", distro, want, out)
		}
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatal("os-release was executed", err)
		}
		if want == "ubuntu" {
			if err := os.Remove(etc); err != nil {
				t.Fatal(err)
			}
		}
		if want == "debian" {
			if err := os.Remove(vendor); err != nil {
				t.Fatal(err)
			}
		}
	}
}
