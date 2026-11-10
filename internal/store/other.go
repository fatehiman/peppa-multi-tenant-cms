package store

import (
	"database/sql"
	"errors"
	"time"
)

// ---------- settings ----------

// AllSettings returns every setting as a map.
func AllSettings(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// SetSettings writes many settings in one transaction. Keys not in m are kept.
func SetSettings(db *sql.DB, m map[string]string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for k, v := range m {
		if _, err := tx.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---------- menus ----------

// Menu is a named menu.
type Menu struct {
	ID  int64
	Key string
}

// MenuItem is one entry; Children holds the second level.
type MenuItem struct {
	ID       int64
	MenuID   int64
	ParentID int64 // 0 = top level
	Label    string
	URL      string
	PageID   int64 // 0 = none
	PageSlug string
	Sort     int
	Children []*MenuItem
}

// EnsureMenu returns the id of menu key, creating it when missing.
func EnsureMenu(db *sql.DB, key string) (int64, error) {
	if _, err := db.Exec("INSERT OR IGNORE INTO menus (key) VALUES (?)", key); err != nil {
		return 0, err
	}
	var id int64
	err := db.QueryRow("SELECT id FROM menus WHERE key = ?", key).Scan(&id)
	return id, err
}

// ListMenus returns all menus.
func ListMenus(db *sql.DB) ([]Menu, error) {
	rows, err := db.Query("SELECT id, key FROM menus ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		if err := rows.Scan(&m.ID, &m.Key); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MenuTree returns the items of menu key as a one-level tree. A missing menu gives nil.
func MenuTree(db *sql.DB, key string) ([]*MenuItem, error) {
	rows, err := db.Query(`SELECT i.id, i.menu_id, coalesce(i.parent_id, 0), i.label, i.url, coalesce(i.page_id, 0),
		coalesce(p.slug, ''), i.sort
		FROM menu_items i JOIN menus m ON m.id = i.menu_id LEFT JOIN pages p ON p.id = i.page_id
		WHERE m.key = ? ORDER BY i.sort, i.id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []*MenuItem
	for rows.Next() {
		it := &MenuItem{}
		if err := rows.Scan(&it.ID, &it.MenuID, &it.ParentID, &it.Label, &it.URL, &it.PageID, &it.PageSlug, &it.Sort); err != nil {
			return nil, err
		}
		all = append(all, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byID := map[int64]*MenuItem{}
	var top []*MenuItem
	for _, it := range all {
		if it.ParentID == 0 {
			top = append(top, it)
			byID[it.ID] = it
		}
	}
	for _, it := range all {
		if it.ParentID != 0 {
			if p := byID[it.ParentID]; p != nil {
				p.Children = append(p.Children, it)
			}
		}
	}
	return top, nil
}

// SaveMenuItem inserts or updates an item. Nesting is limited to one level.
func SaveMenuItem(db *sql.DB, it *MenuItem) error {
	var parent, page any
	if it.ParentID != 0 {
		parent = it.ParentID
	}
	if it.PageID != 0 {
		page = it.PageID
	}
	if it.ID == 0 {
		var maxSort int
		_ = db.QueryRow("SELECT coalesce(max(sort), 0) FROM menu_items WHERE menu_id = ?", it.MenuID).Scan(&maxSort)
		res, err := db.Exec("INSERT INTO menu_items (menu_id, parent_id, label, url, page_id, sort) VALUES (?,?,?,?,?,?)",
			it.MenuID, parent, it.Label, it.URL, page, maxSort+1)
		if err != nil {
			return err
		}
		it.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := db.Exec("UPDATE menu_items SET parent_id=?, label=?, url=?, page_id=? WHERE id=?", parent, it.Label, it.URL, page, it.ID)
	return err
}

// DeleteMenuItem removes an item and its children.
func DeleteMenuItem(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM menu_items WHERE id = ?", id)
	return err
}

// MoveMenuItem moves an item up (-1) or down (+1) among its siblings.
func MoveMenuItem(db *sql.DB, id int64, dir int) error {
	var menuID, parent int64
	if err := db.QueryRow("SELECT menu_id, coalesce(parent_id, 0) FROM menu_items WHERE id = ?", id).Scan(&menuID, &parent); err != nil {
		return err
	}
	return moveRow(db, "menu_items", "menu_id = ? AND coalesce(parent_id, 0) = ?", []any{menuID, parent}, id, dir)
}

// ---------- redirects ----------

// Redirect is one row of the redirects table.
type Redirect struct {
	ID       int64
	FromPath string
	ToURL    string
	Code     int
	Hits     int64
}

// FindRedirect returns the redirect for an exact path, or nil.
func FindRedirect(db *sql.DB, path string) (*Redirect, error) {
	r := &Redirect{}
	err := db.QueryRow("SELECT id, from_path, to_url, code, hits FROM redirects WHERE from_path = ?", path).
		Scan(&r.ID, &r.FromPath, &r.ToURL, &r.Code, &r.Hits)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// HitRedirect adds one to the hit counter.
func HitRedirect(db *sql.DB, id int64) {
	_, _ = db.Exec("UPDATE redirects SET hits = hits + 1 WHERE id = ?", id)
}

// ListRedirects returns all redirects.
func ListRedirects(db *sql.DB) ([]Redirect, error) {
	rows, err := db.Query("SELECT id, from_path, to_url, code, hits FROM redirects ORDER BY from_path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redirect
	for rows.Next() {
		var r Redirect
		if err := rows.Scan(&r.ID, &r.FromPath, &r.ToURL, &r.Code, &r.Hits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRedirect inserts a redirect or replaces the one with the same from_path.
func SaveRedirect(db *sql.DB, from, to string, code int) error {
	_, err := db.Exec(`INSERT INTO redirects (from_path, to_url, code) VALUES (?, ?, ?)
		ON CONFLICT(from_path) DO UPDATE SET to_url = excluded.to_url, code = excluded.code`, from, to, code)
	return err
}

// DeleteRedirect removes a redirect.
func DeleteRedirect(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM redirects WHERE id = ?", id)
	return err
}

// ---------- media ----------

// Media is one uploaded file.
type Media struct {
	ID        int64
	FileName  string
	OrigName  string
	Mime      string
	Size      int64
	Width     int
	Height    int
	Alt       string
	CreatedAt int64
}

const mediaCols = "id, file_name, orig_name, mime, size, width, height, alt, created_at"

func scanMedia(s scanner) (*Media, error) {
	m := &Media{}
	err := s.Scan(&m.ID, &m.FileName, &m.OrigName, &m.Mime, &m.Size, &m.Width, &m.Height, &m.Alt, &m.CreatedAt)
	return m, err
}

// InsertMedia stores a media row.
func InsertMedia(db *sql.DB, m *Media) error {
	m.CreatedAt = time.Now().Unix()
	res, err := db.Exec("INSERT INTO media (file_name, orig_name, mime, size, width, height, alt, created_at) VALUES (?,?,?,?,?,?,?,?)",
		m.FileName, m.OrigName, m.Mime, m.Size, m.Width, m.Height, m.Alt, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

// ListMedia returns all media, newest first.
func ListMedia(db *sql.DB) ([]*Media, error) {
	rows, err := db.Query("SELECT " + mediaCols + " FROM media ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MediaByID returns one media row.
func MediaByID(db *sql.DB, id int64) (*Media, error) {
	m, err := scanMedia(db.QueryRow("SELECT "+mediaCols+" FROM media WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MediaByName returns one media row by stored file name, or nil.
func MediaByName(db *sql.DB, name string) *Media {
	m, err := scanMedia(db.QueryRow("SELECT "+mediaCols+" FROM media WHERE file_name = ?", name))
	if err != nil {
		return nil
	}
	return m
}

// SetMediaAlt changes the alt text.
func SetMediaAlt(db *sql.DB, id int64, alt string) error {
	_, err := db.Exec("UPDATE media SET alt = ? WHERE id = ?", alt, id)
	return err
}

// DeleteMedia removes the row (the caller removes the file).
func DeleteMedia(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM media WHERE id = ?", id)
	return err
}

// ---------- users ----------

// User is a site admin user.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         string
}

// UserByEmail returns a user or ErrNotFound.
func UserByEmail(db *sql.DB, email string) (*User, error) {
	u := &User{}
	err := db.QueryRow("SELECT id, email, password_hash, role FROM users WHERE email = ?", email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// UserByID returns a user or ErrNotFound.
func UserByID(db *sql.DB, id int64) (*User, error) {
	u := &User{}
	err := db.QueryRow("SELECT id, email, password_hash, role FROM users WHERE id = ?", id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ListUsers returns all users.
func ListUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query("SELECT id, email, '', role FROM users ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser inserts a user with an already hashed password.
func CreateUser(db *sql.DB, email, hash, role string) error {
	_, err := db.Exec("INSERT INTO users (email, password_hash, role) VALUES (?, ?, ?)", email, hash, role)
	return err
}

// UpdateUser changes role and, when hash is not empty, the password.
func UpdateUser(db *sql.DB, id int64, role, hash string) error {
	if hash != "" {
		if _, err := db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", hash, id); err != nil {
			return err
		}
	}
	_, err := db.Exec("UPDATE users SET role = ? WHERE id = ?", role, id)
	return err
}

// DeleteUser removes a user.
func DeleteUser(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

// CountAdmins counts users with role admin.
func CountAdmins(db *sql.DB) int {
	var n int
	_ = db.QueryRow("SELECT count(*) FROM users WHERE role = 'admin'").Scan(&n)
	return n
}

// ---------- module_data ----------

// Record is one row of module_data.
type Record struct {
	ID        int64
	Module    string
	Key       string
	ValueJSON string
	CreatedAt int64
}

// ModPut inserts a record and returns its id.
func ModPut(db *sql.DB, module, key, valueJSON string) (int64, error) {
	res, err := db.Exec("INSERT INTO module_data (module, key, value_json, created_at) VALUES (?, ?, ?, ?)",
		module, key, valueJSON, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ModSet replaces all records of module+key with one value.
func ModSet(db *sql.DB, module, key, valueJSON string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM module_data WHERE module = ? AND key = ?", module, key); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("INSERT INTO module_data (module, key, value_json, created_at) VALUES (?, ?, ?, ?)",
		module, key, valueJSON, time.Now().Unix()); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ModGet returns the newest record of module+key, or nil.
func ModGet(db *sql.DB, module, key string) (*Record, error) {
	r := &Record{}
	err := db.QueryRow("SELECT id, module, key, value_json, created_at FROM module_data WHERE module = ? AND key = ? ORDER BY id DESC LIMIT 1",
		module, key).Scan(&r.ID, &r.Module, &r.Key, &r.ValueJSON, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ModList returns records of a module with the given key ("" = all keys), newest first.
func ModList(db *sql.DB, module, key string, limit, offset int) ([]Record, error) {
	rows, err := db.Query(`SELECT id, module, key, value_json, created_at FROM module_data
		WHERE module = ? AND (? = '' OR key = ?) ORDER BY id DESC LIMIT ? OFFSET ?`, module, key, key, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Module, &r.Key, &r.ValueJSON, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModDelete removes one record of a module.
func ModDelete(db *sql.DB, module string, id int64) error {
	_, err := db.Exec("DELETE FROM module_data WHERE module = ? AND id = ?", module, id)
	return err
}
