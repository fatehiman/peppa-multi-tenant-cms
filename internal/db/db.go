// Package db opens SQLite files with the hostkit pragmas and runs embedded migrations.
package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Open opens (or creates) a SQLite file with WAL, NORMAL sync, foreign keys,
// a 5 s busy timeout and a 1 MB page cache. One connection only: SQLite has one
// writer anyway, and one connection keeps RAM low.
func Open(file string) (*sql.DB, error) {
	_, statErr := os.Stat(file)
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(5000)", "cache_size(-1000)"} {
		q.Add("_pragma", p)
	}
	d, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	if statErr != nil { // new file: only the hostkit user may read it
		_ = os.Chmod(file, 0o600)
	}
	return d, nil
}

// Migrate applies every file NNNN_*.sql in dir whose number is greater than
// PRAGMA user_version. Each file runs in its own transaction.
func Migrate(d *sql.DB, fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	type mig struct {
		n    int
		name string
	}
	var list []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, _ := strings.Cut(e.Name(), "_")
		n, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("migration %s: bad number", e.Name())
		}
		list = append(list, mig{n, e.Name()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n < list[j].n })
	var cur int
	if err := d.QueryRow("PRAGMA user_version").Scan(&cur); err != nil {
		return err
	}
	for _, m := range list {
		if m.n <= cur {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, m.name))
		if err != nil {
			return err
		}
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.n)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// VacuumInto writes a consistent copy of the open database to dest (online backup).
func VacuumInto(d *sql.DB, dest string) error {
	_, err := d.Exec("VACUUM INTO ?", dest)
	return err
}
