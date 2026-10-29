package bundle

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHash(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode)
	}
	hash := func(extra ...string) string {
		t.Helper()
		h, err := Hash(dir, extra...)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	write("compose.yaml", "services: {}\n", 0o644)
	write("conf/app.ini", "x=1\n", 0o644)
	base := hash("compose.yaml")

	// Timestamps and non-exec permission bits don't matter.
	os.Chtimes(filepath.Join(dir, "conf/app.ini"), time.Now(), time.Unix(0, 0))
	os.Chmod(filepath.Join(dir, "conf/app.ini"), 0o664)
	if got := hash("compose.yaml"); got != base {
		t.Error("hash changed on mtime/permission change")
	}

	changes := []struct {
		name   string
		change func()
	}{
		{"content", func() { write("conf/app.ini", "x=2\n", 0o644) }},
		{"exec bit", func() { os.Chmod(filepath.Join(dir, "conf/app.ini"), 0o755) }},
		{"new file", func() { write("extra.txt", "", 0o644) }},
		{"empty dir", func() { os.Mkdir(filepath.Join(dir, "data"), 0o755) }},
		{"symlink", func() { os.Symlink("compose.yaml", filepath.Join(dir, "link")) }},
		{"rename", func() { os.Rename(filepath.Join(dir, "conf"), filepath.Join(dir, "config")) }},
	}
	for _, c := range changes {
		before := hash("compose.yaml")
		c.change()
		if hash("compose.yaml") == before {
			t.Errorf("hash unchanged after %s", c.name)
		}
	}
	if hash("compose.yaml") == hash("compose.prod.yaml") {
		t.Error("extra input not mixed into hash")
	}
}
