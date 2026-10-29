package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Hash returns a content hash of dir covering exactly what Write ships:
// paths, file contents, symlink targets and the executable bit. Timestamps,
// owners and other permission bits are ignored so the same Git checkout hashes
// identically on a laptop and in CI. extra is mixed in for settings that
// change the deployment without changing files (e.g. the compose file name).
func Hash(dir string, extra ...string) (string, error) {
	h := sha256.New()
	for _, e := range extra {
		fmt.Fprintf(h, "extra %q\n", e)
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			fmt.Fprintf(h, "dir %q\n", rel)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "link %q %q\n", rel, target)
		case mode.IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			fh := sha256.New()
			_, err = io.Copy(fh, f)
			f.Close()
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "file %q %t %x\n", rel, mode&0o111 != 0, fh.Sum(nil))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
