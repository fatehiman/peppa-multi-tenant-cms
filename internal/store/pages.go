// Package store holds the plain SQL queries for one site.db. No ORM, no reflection.
package store

import (
	"database/sql"
	"errors"
	"html/template"
	"strings"
	"time"
)

// Page is one row of the pages table.
type Page struct {
	ID                int64
	Slug              string
	Title             string
	BodyHTML          string
	Template          string
	Module            string
	ModuleSettings    string
	Status            string
	Sort              int
	CreatedAt         int64
	UpdatedAt         int64
	SEOTitle          string
	MetaDescription   string
	Canonical         string
	Robots            string
	OGTitle           string
	OGDescription     string
	OGImage           string
	JSONLD            string
	Alternates        string
	SitemapPriority   string
	SitemapChangefreq string
	InSitemap         bool
}

const pageCols = `id, slug, title, body_html, template, module, module_settings, status, sort, created_at, updated_at,
 seo_title, meta_description, canonical, robots, og_title, og_description, og_image, json_ld, alternates,
 sitemap_priority, sitemap_changefreq, in_sitemap`

type scanner interface{ Scan(...any) error }

func scanPage(s scanner) (*Page, error) {
	p := &Page{}
	err := s.Scan(&p.ID, &p.Slug, &p.Title, &p.BodyHTML, &p.Template, &p.Module, &p.ModuleSettings, &p.Status, &p.Sort,
		&p.CreatedAt, &p.UpdatedAt, &p.SEOTitle, &p.MetaDescription, &p.Canonical, &p.Robots, &p.OGTitle,
		&p.OGDescription, &p.OGImage, &p.JSONLD, &p.Alternates, &p.SitemapPriority, &p.SitemapChangefreq, &p.InSitemap)
	return p, err
}

func queryPages(db *sql.DB, q string, args ...any) ([]*Page, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

func one(p *Page, err error) (*Page, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PageBySlug returns a page by slug (any status).
func PageBySlug(db *sql.DB, slug string) (*Page, error) {
	return one(scanPage(db.QueryRow("SELECT "+pageCols+" FROM pages WHERE slug = ?", slug)))
}

// PageByID returns a page by id.
func PageByID(db *sql.DB, id int64) (*Page, error) {
	return one(scanPage(db.QueryRow("SELECT "+pageCols+" FROM pages WHERE id = ?", id)))
}

// ListPages returns all pages in admin order.
func ListPages(db *sql.DB) ([]*Page, error) {
	return queryPages(db, "SELECT "+pageCols+" FROM pages ORDER BY sort, slug")
}

// PagesByPrefix returns published pages whose slug starts with prefix (and is not
// the prefix itself), newest first.
func PagesByPrefix(db *sql.DB, prefix string, limit, offset int) ([]*Page, error) {
	return queryPages(db, "SELECT "+pageCols+` FROM pages
		WHERE status = 'published' AND substr(slug, 1, ?) = ? AND slug <> ?
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		len(prefix), prefix, strings.TrimSuffix(prefix, "/"), limit, offset)
}

// CountByPrefix counts the pages PagesByPrefix would return without a limit.
func CountByPrefix(db *sql.DB, prefix string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM pages WHERE status = 'published' AND substr(slug, 1, ?) = ? AND slug <> ?`,
		len(prefix), prefix, strings.TrimSuffix(prefix, "/")).Scan(&n)
	return n, err
}

// SitemapPages returns published pages with in_sitemap = 1.
func SitemapPages(db *sql.DB) ([]*Page, error) {
	return queryPages(db, "SELECT "+pageCols+" FROM pages WHERE status = 'published' AND in_sitemap = 1 ORDER BY sort, slug")
}

// SavePage inserts (ID == 0) or updates a page. It sets updated_at (and created_at on insert).
func SavePage(db *sql.DB, p *Page) error {
	now := time.Now().Unix()
	p.UpdatedAt = now
	if p.ID == 0 {
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
		var maxSort int
		_ = db.QueryRow("SELECT coalesce(max(sort), 0) FROM pages").Scan(&maxSort)
		p.Sort = maxSort + 1
		res, err := db.Exec(`INSERT INTO pages (slug, title, body_html, template, module, module_settings, status, sort,
			created_at, updated_at, seo_title, meta_description, canonical, robots, og_title, og_description, og_image,
			json_ld, alternates, sitemap_priority, sitemap_changefreq, in_sitemap)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Slug, p.Title, p.BodyHTML, p.Template, p.Module, p.ModuleSettings, p.Status, p.Sort, p.CreatedAt, p.UpdatedAt,
			p.SEOTitle, p.MetaDescription, p.Canonical, p.Robots, p.OGTitle, p.OGDescription, p.OGImage, p.JSONLD,
			p.Alternates, p.SitemapPriority, p.SitemapChangefreq, p.InSitemap)
		if err != nil {
			return err
		}
		p.ID, err = res.LastInsertId()
		return err
	}
	_, err := db.Exec(`UPDATE pages SET slug=?, title=?, body_html=?, template=?, module=?, module_settings=?, status=?,
		updated_at=?, seo_title=?, meta_description=?, canonical=?, robots=?, og_title=?, og_description=?, og_image=?,
		json_ld=?, alternates=?, sitemap_priority=?, sitemap_changefreq=?, in_sitemap=? WHERE id=?`,
		p.Slug, p.Title, p.BodyHTML, p.Template, p.Module, p.ModuleSettings, p.Status, p.UpdatedAt,
		p.SEOTitle, p.MetaDescription, p.Canonical, p.Robots, p.OGTitle, p.OGDescription, p.OGImage, p.JSONLD,
		p.Alternates, p.SitemapPriority, p.SitemapChangefreq, p.InSitemap, p.ID)
	return err
}

// DeletePage removes a page.
func DeletePage(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM pages WHERE id = ?", id)
	return err
}

// MovePage swaps the sort value of a page with its neighbour (dir = -1 up, +1 down).
func MovePage(db *sql.DB, id int64, dir int) error {
	return moveRow(db, "pages", "1=1", nil, id, dir)
}

// moveRow renumbers the rows matching where (in sort order) and swaps id with its neighbour.
func moveRow(db *sql.DB, table, where string, args []any, id int64, dir int) error {
	rows, err := db.Query("SELECT id FROM "+table+" WHERE "+where+" ORDER BY sort, id", args...)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var x int64
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, x)
	}
	rows.Close()
	for i, x := range ids {
		if x == id {
			j := i + dir
			if j >= 0 && j < len(ids) {
				ids[i], ids[j] = ids[j], ids[i]
			}
			break
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for i, x := range ids {
		if _, err := tx.Exec("UPDATE "+table+" SET sort = ? WHERE id = ?", i+1, x); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// PageView is what templates see as .Page (and items of `pages`).
type PageView struct {
	ID              int64
	Title           string
	Slug            string
	URL             string // path including the site's BasePath
	Body            template.HTML
	MetaDescription string
	Template        string
	Module          string
	Image           string // og_image
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// View makes a PageView; basePath is "" or like "/de".
func (p *Page) View(basePath string) *PageView {
	return &PageView{
		ID: p.ID, Title: p.Title, Slug: p.Slug, URL: PagePath(basePath, p.Slug),
		Body: template.HTML(p.BodyHTML), MetaDescription: p.MetaDescription, Template: p.Template,
		Module: p.Module, Image: p.OGImage,
		CreatedAt: time.Unix(p.CreatedAt, 0), UpdatedAt: time.Unix(p.UpdatedAt, 0),
	}
}

// PagePath builds the public path of a slug: "" → "/" (or "/de"), "about" → "/about" (or "/de/about").
func PagePath(basePath, slug string) string {
	if slug == "" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	return basePath + "/" + slug
}

// ModuleSetting reads key from the page's key=value lines.
func (p *Page) ModuleSetting(key string) string {
	return ParseKV(p.ModuleSettings)[key]
}

// ParseKV parses "key=value" lines.
func ParseKV(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) != "" {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// SearchPages runs an FTS5 MATCH query over published pages, best match first.
func SearchPages(db *sql.DB, match string, limit int) ([]*Page, error) {
	cols := strings.ReplaceAll(pageCols, "\n", " ")
	var pref []string
	for _, c := range strings.Split(cols, ",") {
		pref = append(pref, "p."+strings.TrimSpace(c))
	}
	return queryPages(db, "SELECT "+strings.Join(pref, ", ")+` FROM pages_fts f JOIN pages p ON p.id = f.rowid
		WHERE pages_fts MATCH ? AND p.status = 'published' AND p.module <> 'redirect' ORDER BY rank LIMIT ?`, match, limit)
}
