// Package module defines the Module interface, the per-request Ctx and the registry.
// Modules are plain Go code compiled into the binary and switched on per page.
package module

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"sync"

	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// Module is Go code attached to a page (pages.module = Name()).
type Module interface {
	Name() string
	// Handle runs for GET and POST on a page that uses this module.
	// Return data for the template (.Data), or write a response yourself and return handled=true.
	Handle(ctx *Ctx) (data any, handled bool, err error)
	// Admin returns extra screens in the site admin (nil if none).
	Admin() []AdminScreen
}

// PrefixRouter is optional: a module that claims a URL prefix (for example "/api/booking/").
// Handle is then called with ctx.Page == nil for every request under that prefix.
type PrefixRouter interface {
	Prefix() string
}

// SiteFilter is optional: restricts a module to some site keys.
type SiteFilter interface {
	Sites() []string
}

// Cacheable is optional: a module whose output may go into the page cache.
type Cacheable interface {
	Cacheable() bool
}

// SitemapURL is one extra sitemap entry from a module.
type SitemapURL struct {
	Path       string // site path like "/news/item-1" (BasePath is added)
	LastMod    int64
	Priority   string
	ChangeFreq string
}

// Sitemapper is optional: a module that adds URLs to sitemap.xml.
type Sitemapper interface {
	Sitemap(ctx *Ctx) []SitemapURL
}

// Migrator is optional: a module with its own tables. Statements are applied in order,
// once per site that uses the module; the count is kept in settings key "_mig_<name>".
type Migrator interface {
	Migrations() []string
}

// Ctx gives a module everything it needs, scoped to one site.
type Ctx struct {
	Site       *tenant.Site
	Page       *store.Page // nil for prefix routes
	DB         *sql.DB     // this site's site.db only
	W          http.ResponseWriter
	R          *http.Request
	Store      Store
	Settings   map[string]string
	Path       string // request path without the site prefix
	ClientIP   string
	CSRFValid  func() bool // checks the public form token
	Invalidate func()      // clears this site's page cache
}

// URL builds a site URL path: "/contact" → "/de/contact" for a prefixed site.
func (c *Ctx) URL(path string) string { return c.Site.BasePath + path }

// AdminScreen is a module page inside the site admin.
type AdminScreen struct {
	Key    string // URL part: /admin/modules/<module>/<key>
	Title  string
	Handle func(a *AdminCtx) (template.HTML, error)
}

// AdminCtx is given to module admin screens.
type AdminCtx struct {
	Site      *tenant.Site
	DB        *sql.DB
	Store     Store
	W         http.ResponseWriter
	R         *http.Request
	CSRFField template.HTML // hidden input for forms
	BaseURL   string        // URL of this screen
	Done      bool          // set true when the screen wrote a response itself (redirect)
}

// Store is a small helper over the module_data table, scoped to one module.
type Store struct {
	DB     *sql.DB
	Module string
}

// Put adds a record; v is stored as JSON.
func (s Store) Put(key string, v any) (int64, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	return store.ModPut(s.DB, s.Module, key, string(b))
}

// Set replaces the value of key.
func (s Store) Set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return store.ModSet(s.DB, s.Module, key, string(b))
}

// Get loads the newest value of key into v; found=false when there is none.
func (s Store) Get(key string, v any) (bool, error) {
	r, err := store.ModGet(s.DB, s.Module, key)
	if err != nil || r == nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(r.ValueJSON), v)
}

// List returns records of key ("" = all), newest first.
func (s Store) List(key string, limit, offset int) ([]store.Record, error) {
	return store.ModList(s.DB, s.Module, key, limit, offset)
}

// Delete removes one record.
func (s Store) Delete(id int64) error { return store.ModDelete(s.DB, s.Module, id) }

// ---------- registry ----------

var (
	mu  sync.RWMutex
	reg = map[string]Module{}
)

// Register adds a module. Call it from init().
func Register(m Module) {
	mu.Lock()
	defer mu.Unlock()
	reg[m.Name()] = m
}

// Get returns a module by name, or nil.
func Get(name string) Module {
	mu.RLock()
	defer mu.RUnlock()
	return reg[name]
}

// All returns every registered module sorted by name.
func All() []Module {
	mu.RLock()
	out := make([]Module, 0, len(reg))
	for _, m := range reg {
		out = append(out, m)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Allowed reports whether module m may be used by site key.
func Allowed(m Module, siteKey string) bool {
	f, ok := m.(SiteFilter)
	if !ok {
		return true
	}
	for _, k := range f.Sites() {
		if k == siteKey {
			return true
		}
	}
	return false
}

// ForSite returns the modules a site may use.
func ForSite(siteKey string) []Module {
	var out []Module
	for _, m := range All() {
		if Allowed(m, siteKey) {
			out = append(out, m)
		}
	}
	return out
}

// RunMigrations applies module migrations for one site (used as tenant.Registry.OnOpen).
func RunMigrations(s *tenant.Site, d *sql.DB) error {
	for _, m := range ForSite(s.Key) {
		mg, ok := m.(Migrator)
		if !ok {
			continue
		}
		key := "_mig_" + m.Name()
		var done int
		_ = d.QueryRow("SELECT CAST(value AS INTEGER) FROM settings WHERE key = ?", key).Scan(&done)
		list := mg.Migrations()
		for i := done; i < len(list); i++ {
			if _, err := d.Exec(list[i]); err != nil {
				return err
			}
			if _, err := d.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, i+1); err != nil {
				return err
			}
		}
	}
	return nil
}
