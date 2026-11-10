// Package backup copies databases safely while running (VACUUM INTO) and archives site folders.
package backup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hostkit/internal/db"
	"hostkit/internal/tenant"
)

// snapshot writes a consistent copy of the site's database to a temp file.
func snapshot(reg *tenant.Registry, s *tenant.Site) (string, error) {
	d, err := reg.DB(s)
	if err != nil {
		return "", err
	}
	return snapshotDB(d, os.TempDir(), "hostkit-"+s.Key)
}

func snapshotDB(d *sql.DB, dir, prefix string) (string, error) {
	f, err := os.CreateTemp(dir, prefix+"-*.db")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	os.Remove(name) // VACUUM INTO needs a new file
	if err := db.VacuumInto(d, name); err != nil {
		return "", err
	}
	return name, nil
}

// fileEntry is one file to put in an archive.
type fileEntry struct {
	src  string // path on disk
	name string // name in the archive
}

func siteFiles(reg *tenant.Registry, s *tenant.Site, dbCopy string) []fileEntry {
	files := []fileEntry{{dbCopy, s.Key + "/site.db"}}
	up := reg.UploadsDir(s.Key)
	_ = filepath.WalkDir(up, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(up, p)
		files = append(files, fileEntry{p, s.Key + "/uploads/" + filepath.ToSlash(rel)})
		return nil
	})
	return files
}

// WriteZip streams a zip of the site folder (database copy + uploads) to w.
// The zip holds "<key>/site.db" and "<key>/uploads/…", so it unpacks straight into sites/.
func WriteZip(w io.Writer, reg *tenant.Registry, s *tenant.Site) error {
	snap, err := snapshot(reg, s)
	if err != nil {
		return err
	}
	defer os.Remove(snap)
	zw := zip.NewWriter(w)
	for _, f := range siteFiles(reg, s, snap) {
		in, err := os.Open(f.src)
		if err != nil {
			return err
		}
		out, err := zw.Create(f.name)
		if err == nil {
			_, err = io.Copy(out, in)
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// WriteTarGz writes a .tar.gz of the site folder to path.
func WriteTarGz(path string, reg *tenant.Registry, s *tenant.Site) error {
	snap, err := snapshot(reg, s)
	if err != nil {
		return err
	}
	defer os.Remove(snap)
	return tarGz(path, siteFiles(reg, s, snap))
}

func tarGz(path string, files []fileEntry) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		st, err := os.Stat(f.src)
		if err != nil {
			continue
		}
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			out.Close()
			return err
		}
		in, err := os.Open(f.src)
		if err != nil {
			out.Close()
			return err
		}
		_, err = io.Copy(tw, in)
		in.Close()
		if err != nil {
			out.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Nightly backs up main.db and every site into dir/<YYYY-MM-DD>/ and deletes
// date folders older than keepDays.
func Nightly(mainDB *sql.DB, reg *tenant.Registry, dir string, keepDays int) error {
	day := filepath.Join(dir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(day, 0o700); err != nil {
		return err
	}
	snap, err := snapshotDB(mainDB, day, "main")
	if err != nil {
		return fmt.Errorf("main.db: %w", err)
	}
	if err := tarGz(filepath.Join(day, "main.tar.gz"), []fileEntry{{snap, "main.db"}}); err != nil {
		return err
	}
	os.Remove(snap)
	var failed []string
	for _, s := range reg.All() {
		if err := WriteTarGz(filepath.Join(day, s.Key+".tar.gz"), reg, s); err != nil {
			slog.Error("backup site", "site", s.Key, "err", err)
			failed = append(failed, s.Key)
		}
	}
	// prune
	entries, _ := os.ReadDir(dir)
	var days []string
	for _, e := range entries {
		if _, err := time.Parse("2006-01-02", e.Name()); e.IsDir() && err == nil {
			days = append(days, e.Name())
		}
	}
	sort.Strings(days)
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	for _, d := range days {
		if d < cutoff {
			_ = os.RemoveAll(filepath.Join(dir, d))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("backup failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}
