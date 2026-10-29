package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "conf/app.ini"), []byte("x=1\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PW=${PW}\n"), 0o644)
	os.Symlink("conf/app.ini", filepath.Join(dir, "link.ini"))

	var buf bytes.Buffer
	if err := Write(&buf, dir, map[string][]byte{".env": []byte("PW=\"secret\"\n")}); err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]*tar.Header{}
	contents := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = hdr
		b, _ := io.ReadAll(tr)
		contents[hdr.Name] = string(b)
	}

	for _, name := range []string{"compose.yaml", "conf", "conf/app.ini", "link.ini"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing %s in archive", name)
		}
	}
	if contents["conf/app.ini"] != "x=1\n" {
		t.Errorf("conf/app.ini = %q", contents["conf/app.ini"])
	}
	if h := got["conf/app.ini"]; h.Mode&0o777 != 0o600 || h.Uid != 0 || h.Uname != "" {
		t.Errorf("unexpected header for conf/app.ini: mode=%o uid=%d uname=%q", h.Mode, h.Uid, h.Uname)
	}
	if h := got[".env"]; contents[".env"] != "PW=\"secret\"\n" || h.Size != int64(len(contents[".env"])) || h.Mode&0o777 != 0o600 {
		t.Errorf(".env not replaced: %q mode=%o", contents[".env"], h.Mode)
	}
	if h := got["link.ini"]; h.Typeflag != tar.TypeSymlink || h.Linkname != "conf/app.ini" {
		t.Errorf("link.ini not stored as symlink: %+v", h)
	}
}
