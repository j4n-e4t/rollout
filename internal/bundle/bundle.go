// Package bundle packs a service directory into a gzipped tarball that is
// streamed to the server.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Write streams dir as a .tar.gz to w. Paths in the archive are relative to
// dir. Symlinks are stored as links; other non-regular files are skipped.
// Files in replace (keyed by slash-separated relative path) are shipped with
// the given contents instead, readable by the owner only: they are rendered
// env files holding secrets.
func Write(w io.Writer, dir string, replace map[string][]byte) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&fs.ModeSymlink == 0 {
			return nil
		}
		var link string
		if mode&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		// Don't leak local user names/ids into the server's file system.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		data, replaced := replace[hdr.Name]
		if replaced && mode.IsRegular() {
			hdr.Size, hdr.Mode = int64(len(data)), 0o600
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !mode.IsRegular() {
			return nil
		}
		if replaced {
			_, err := tw.Write(data)
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
