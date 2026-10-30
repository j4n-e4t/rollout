package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFilesScript(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"compose.yaml":       "services: {}\n",
		"config/app.toml":    "x",
		".env":               "A=1\n",
		"data/db.sqlite":     "persistent",
		"my data/keep.txt":   "persistent",
		"datadir/shipped.md": "shipped",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("compose.yaml", filepath.Join(dir, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", filesScript(dir, []string{"data", "my data"}, 4)).Output()
	if err != nil {
		t.Fatal(err)
	}
	files, total := parseFiles(string(out))
	want := []File{{".env", 4}, {"compose.yaml", 13}, {"config/app.toml", 1}, {"datadir/shipped.md", 7}}
	if !reflect.DeepEqual(files, want) || total != 5 {
		t.Fatalf("got %v (%d)\n%s", files, total, out)
	}

	out, err = exec.Command("sh", "-c", filesScript(filepath.Join(dir, "missing"), nil, 4)).Output()
	if files, total := parseFiles(string(out)); err != nil || len(files) != 0 || total != 0 {
		t.Fatalf("missing dir: %v %v %d", err, files, total)
	}
}
