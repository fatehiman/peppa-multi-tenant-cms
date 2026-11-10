// Package tenant holds the domain map, the site registry and the lazily opened site databases.
package tenant

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"hostkit/internal/db"
	"hostkit/internal/store"
	"hostkit/migrations"
)

// AdminPath is the URL path of every site admin panel ("https://<domain>/mainadmin").
// It can not be used as a host path prefix.
const AdminPath = "/mainadmin"

// Host is a domain plus an optional path prefix ("" or like "/de").
type Host struct {
	Domain  string
	Prefix  string
	Primary bool
}

// String gives "domain.com" or "domain.com/de".
func (h Host) String() string { return h.Domain + h.Prefix }

// Site is one website (one language of a website).
type Site struct {
	ID            int64
	Key           string
	Name          string
	Theme         string
	Lang          string
	Status        string // active / disabled
	AdminEnabled  bool
	AdminSections []string // main admin override; nil = use theme.json
	CreatedAt     int64
	UpdatedAt     int64
	Hosts         []Host // primary first
	Domain        string // primary domain
	BasePath      string // primary prefix, "" or "/de"
}

// Disabled reports whether the site is switched off.
func (s *Site) Disabled() bool { return s.Status == "disabled" }

// SiteDir is the folder of a site.
func (r *Registry) SiteDir(key string) string { return filepath.Join(r.SitesDir, key) }

// UploadsDir is the media folder of a site.
func (r *Registry) UploadsDir(key string) string { return filepath.Join(r.SitesDir, key, "uploads") }

type siteDB struct {
	db       *sql.DB
	lastUsed time.Time
}

// Registry is the in-memory domain map plus the open site databases.
type Registry struct {
	Main     *sql.DB
	SitesDir string
	// OnOpen runs after a site.db is opened and migrated (module migrations).
	OnOpen func(s *Site, d *sql.DB) error

	mu      sync.RWMutex
	byHost  map[string]*Site // domain + prefix
	byKey   map[string]*Site
	byID    map[int64]*Site
	domains map[string]bool // every known domain

	dbMu sync.Mutex
	dbs  map[string]*siteDB
}

// New loads the registry from main.db.
func New(mainDB *sql.DB, sitesDir string) (*Registry, error) {
	r := &Registry{Main: mainDB, SitesDir: sitesDir, dbs: map[string]*siteDB{}}
	if err := os.MkdirAll(sitesDir, 0o755); err != nil {
		return nil, err
	}
	return r, r.Reload()
}

// Reload rebuilds the domain map from main.db.
func (r *Registry) Reload() error {
	rows, err := r.Main.Query(`SELECT id, key, name, theme, lang, status, admin_enabled, admin_sections, created_at, updated_at FROM sites`)
	if err != nil {
		return err
	}
	byKey := map[string]*Site{}
	byID := map[int64]*Site{}
	for rows.Next() {
		s := &Site{}
		var sections string
		if err := rows.Scan(&s.ID, &s.Key, &s.Name, &s.Theme, &s.Lang, &s.Status, &s.AdminEnabled, &sections, &s.CreatedAt, &s.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		s.AdminSections = SplitList(sections)
		byKey[s.Key] = s
		byID[s.ID] = s
	}
	rows.Close()
	rows, err = r.Main.Query(`SELECT domain, path_prefix, site_id, is_primary FROM domains ORDER BY is_primary DESC, domain, path_prefix`)
	if err != nil {
		return err
	}
	byHost := map[string]*Site{}
	domains := map[string]bool{}
	for rows.Next() {
		var h Host
		var id int64
		if err := rows.Scan(&h.Domain, &h.Prefix, &id, &h.Primary); err != nil {
			rows.Close()
			return err
		}
		s := byID[id]
		if s == nil {
			continue
		}
		s.Hosts = append(s.Hosts, h)
		byHost[h.Domain+h.Prefix] = s
		domains[h.Domain] = true
	}
	rows.Close()
	for _, s := range byKey {
		if len(s.Hosts) > 0 {
			s.Hosts[0].Primary = true
			s.Domain, s.BasePath = s.Hosts[0].Domain, s.Hosts[0].Prefix
		}
	}
	r.mu.Lock()
	r.byHost, r.byKey, r.byID, r.domains = byHost, byKey, byID, domains
	r.mu.Unlock()
	return nil
}

// SplitList splits a comma or space separated list.
func SplitList(s string) []string {
	f := strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\r' })
	if len(f) == 0 {
		return nil
	}
	return f
}

// NormalizeHost lower-cases the host and strips the port and a trailing dot.
func NormalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}

// Match is the result of Lookup.
type Match struct {
	Site    *Site
	Host    Host   // the host that matched (may be an alias)
	Rest    string // path with the prefix removed, always starts with "/"
	WWW     bool   // matched by stripping "www."
	Domain  string // the normalized request domain
	Unknown bool
}

// Lookup finds the site for a host header and URL path. Longest prefix wins and
// matching is per path segment, so "/design" never matches "/de".
func (r *Registry) Lookup(hostHeader, path string) Match {
	domain := NormalizeHost(hostHeader)
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := Match{Domain: domain}
	try := func(d string) bool {
		if !r.domains[d] {
			return false
		}
		// candidate prefixes from the longest (3 segments) to "".
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(segs) > 3 {
			segs = segs[:3]
		}
		for n := len(segs); n >= 0; n-- {
			prefix := ""
			if n > 0 {
				if segs[n-1] == "" {
					continue
				}
				prefix = "/" + strings.ToLower(strings.Join(segs[:n], "/"))
			}
			if s := r.byHost[d+prefix]; s != nil {
				m.Site = s
				for _, h := range s.Hosts {
					if h.Domain == d && h.Prefix == prefix {
						m.Host = h
					}
				}
				m.Rest = path[len(prefix):]
				if m.Rest == "" || m.Rest[0] != '/' {
					m.Rest = "/" + m.Rest
				}
				return true
			}
		}
		return false
	}
	if try(domain) {
		return m
	}
	if strings.HasPrefix(domain, "www.") && try(strings.TrimPrefix(domain, "www.")) {
		m.WWW = true
		return m
	}
	m.Unknown = true
	return m
}

// KnownDomain reports whether any site uses domain.
func (r *Registry) KnownDomain(domain string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.domains[domain]
}

// ByKey returns a site by key, or nil.
func (r *Registry) ByKey(key string) *Site {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[key]
}

// ByID returns a site by id, or nil.
func (r *Registry) ByID(id int64) *Site {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// All returns all sites sorted by name.
func (r *Registry) All() []*Site {
	r.mu.RLock()
	out := make([]*Site, 0, len(r.byKey))
	for _, s := range r.byKey {
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SisterSites returns all sites that have their primary host on domain (including s itself).
func (r *Registry) SisterSites(domain string) []*Site {
	var out []*Site
	for _, s := range r.All() {
		if s.Domain == domain {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BasePath < out[j].BasePath })
	return out
}

// ---------- site databases ----------

// DB returns the open site.db of a site, opening and migrating it on first use.
func (r *Registry) DB(s *Site) (*sql.DB, error) {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	if h := r.dbs[s.Key]; h != nil {
		h.lastUsed = time.Now()
		return h.db, nil
	}
	dir := r.SiteDir(s.Key)
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o755); err != nil {
		return nil, err
	}
	d, err := db.Open(filepath.Join(dir, "site.db"))
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(d, migrations.FS, "site"); err != nil {
		d.Close()
		return nil, fmt.Errorf("site %s: %w", s.Key, err)
	}
	if r.OnOpen != nil {
		if err := r.OnOpen(s, d); err != nil {
			d.Close()
			return nil, fmt.Errorf("site %s modules: %w", s.Key, err)
		}
	}
	r.dbs[s.Key] = &siteDB{db: d, lastUsed: time.Now()}
	slog.Debug("site db opened", "site", s.Key)
	return d, nil
}

// CloseDB closes the database of one site (if open).
func (r *Registry) CloseDB(key string) {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	if h := r.dbs[key]; h != nil {
		h.db.Close()
		delete(r.dbs, key)
	}
}

// CloseAll closes every open site database.
func (r *Registry) CloseAll() {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	for k, h := range r.dbs {
		h.db.Close()
		delete(r.dbs, k)
	}
}

// OpenCount returns how many site databases are open.
func (r *Registry) OpenCount() int {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	return len(r.dbs)
}

// CloseIdle closes databases not used for idle.
func (r *Registry) CloseIdle(idle time.Duration) {
	r.dbMu.Lock()
	defer r.dbMu.Unlock()
	for k, h := range r.dbs {
		if time.Since(h.lastUsed) > idle {
			h.db.Close()
			delete(r.dbs, k)
			slog.Debug("site db closed (idle)", "site", k)
		}
	}
}

// RunIdleCloser closes idle site databases every minute until stop is closed.
func (r *Registry) RunIdleCloser(idle time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.CloseIdle(idle)
			_, _ = r.Main.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now().Unix())
		case <-stop:
			return
		}
	}
}

// ---------- create / update / delete ----------

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
var segRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidKey reports whether a site key is valid (lowercase letters, digits, dashes).
func ValidKey(k string) bool { return keyRe.MatchString(k) && k != "trash" }

// ParseHosts parses "domain.com" or "domain.com/de" lines; the first one is primary.
func ParseHosts(text string) ([]Host, error) {
	var out []Host
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		line = strings.TrimPrefix(strings.TrimPrefix(line, "https://"), "http://")
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		domain, prefix, _ := strings.Cut(line, "/")
		if !domainRe.MatchString(domain) {
			return nil, fmt.Errorf("bad domain %q", domain)
		}
		if strings.HasPrefix(domain, "www.") {
			return nil, fmt.Errorf("%q: do not add www. hosts — www.<domain> always redirects (301) to <domain>", line)
		}
		if prefix != "" {
			for _, seg := range strings.Split(prefix, "/") {
				if !segRe.MatchString(seg) {
					return nil, fmt.Errorf("bad path prefix in %q", line)
				}
			}
			if strings.Count(prefix, "/") > 2 {
				return nil, fmt.Errorf("prefix too deep in %q", line)
			}
			prefix = "/" + prefix
			if prefix == "/admin" || prefix == AdminPath || strings.HasPrefix(prefix, "/_") {
				return nil, fmt.Errorf("reserved prefix in %q", line)
			}
		}
		h := Host{Domain: domain, Prefix: prefix, Primary: len(out) == 0}
		if seen[h.String()] {
			continue
		}
		seen[h.String()] = true
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one host is needed")
	}
	return out, nil
}

// SiteInput is the data to create or update a site.
type SiteInput struct {
	Key           string
	Name          string
	Theme         string
	Lang          string
	Hosts         []Host
	AdminEnabled  bool
	AdminSections string
	// Only for create:
	AdminEmail        string
	AdminPasswordHash string
}

func (r *Registry) checkHosts(siteID int64, hosts []Host) error {
	for _, h := range hosts {
		var other int64
		err := r.Main.QueryRow("SELECT site_id FROM domains WHERE domain = ? AND path_prefix = ?", h.Domain, h.Prefix).Scan(&other)
		if err == nil && other != siteID {
			return fmt.Errorf("host %s is already used by another site", h)
		}
	}
	return nil
}

func writeHosts(tx *sql.Tx, siteID int64, hosts []Host) error {
	if _, err := tx.Exec("DELETE FROM domains WHERE site_id = ?", siteID); err != nil {
		return err
	}
	for i, h := range hosts {
		if _, err := tx.Exec("INSERT INTO domains (domain, path_prefix, site_id, is_primary) VALUES (?, ?, ?, ?)",
			h.Domain, h.Prefix, siteID, i == 0); err != nil {
			return err
		}
	}
	return nil
}

// CreateSite makes the folder, site.db, Home page, main menu and first admin, then reloads.
func (r *Registry) CreateSite(in SiteInput) (*Site, error) {
	if !ValidKey(in.Key) {
		return nil, errors.New("site key: use lowercase letters, digits and dashes")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("site name is empty")
	}
	if len(in.Hosts) == 0 {
		return nil, errors.New("at least one host is needed")
	}
	if r.ByKey(in.Key) != nil {
		return nil, errors.New("site key already exists")
	}
	if _, err := os.Stat(r.SiteDir(in.Key)); err == nil {
		return nil, errors.New("folder sites/" + in.Key + " already exists")
	}
	if err := r.checkHosts(0, in.Hosts); err != nil {
		return nil, err
	}
	if in.Lang == "" {
		in.Lang = "en"
	}
	now := time.Now().Unix()
	tx, err := r.Main.Begin()
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO sites (key, name, theme, lang, status, admin_enabled, admin_sections, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?)`, in.Key, in.Name, in.Theme, in.Lang, in.AdminEnabled, in.AdminSections, now, now)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := writeHosts(tx, id, in.Hosts); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	s := r.ByKey(in.Key)
	d, err := r.DB(s)
	if err != nil {
		return nil, err
	}
	home := &store.Page{Slug: "", Title: "Home", Status: "published", InSitemap: true,
		BodyHTML: "<p>Welcome to " + template_escape(in.Name) + ".</p>"}
	if err := store.SavePage(d, home); err != nil {
		return nil, err
	}
	if _, err := store.EnsureMenu(d, "main"); err != nil {
		return nil, err
	}
	if err := store.SetSettings(d, map[string]string{"site_title": in.Name}); err != nil {
		return nil, err
	}
	if in.AdminEmail != "" && in.AdminPasswordHash != "" {
		if err := store.CreateUser(d, strings.ToLower(in.AdminEmail), in.AdminPasswordHash, "admin"); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func template_escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

// UpdateSite changes name, theme, language, hosts and admin options, then reloads.
func (r *Registry) UpdateSite(id int64, in SiteInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("site name is empty")
	}
	if err := r.checkHosts(id, in.Hosts); err != nil {
		return err
	}
	tx, err := r.Main.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sites SET name=?, theme=?, lang=?, admin_enabled=?, admin_sections=?, updated_at=? WHERE id=?`,
		in.Name, in.Theme, in.Lang, in.AdminEnabled, in.AdminSections, time.Now().Unix(), id); err != nil {
		tx.Rollback()
		return err
	}
	if err := writeHosts(tx, id, in.Hosts); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return r.Reload()
}

// SetStatus enables or disables a site.
func (r *Registry) SetStatus(id int64, status string) error {
	if status != "active" && status != "disabled" {
		return errors.New("bad status")
	}
	if _, err := r.Main.Exec("UPDATE sites SET status = ?, updated_at = ? WHERE id = ?", status, time.Now().Unix(), id); err != nil {
		return err
	}
	return r.Reload()
}

// Touch updates updated_at of a site (called after admin saves).
func (r *Registry) Touch(id int64) {
	_, _ = r.Main.Exec("UPDATE sites SET updated_at = ? WHERE id = ?", time.Now().Unix(), id)
	_ = r.Reload() // a few rows; Site values are never changed in place
}

// DeleteSite closes the DB, moves the folder to sites/.trash/ and removes the rows.
func (r *Registry) DeleteSite(id int64) error {
	s := r.ByID(id)
	if s == nil {
		return errors.New("site not found")
	}
	r.CloseDB(s.Key)
	trash := filepath.Join(r.SitesDir, ".trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(r.SiteDir(s.Key)); err == nil {
		dest := filepath.Join(trash, s.Key+"-"+time.Now().Format("20060102-150405"))
		if err := os.Rename(r.SiteDir(s.Key), dest); err != nil {
			return err
		}
	}
	if _, err := r.Main.Exec("DELETE FROM sites WHERE id = ?", id); err != nil {
		return err
	}
	_, _ = r.Main.Exec("DELETE FROM sessions WHERE scope = ?", s.Key)
	return r.Reload()
}
