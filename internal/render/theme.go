// Package render loads themes, runs html/template with the hostkit helpers,
// expands [[component]] shortcodes and keeps the page cache.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Field is one settings field of theme.json.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"` // text, textarea, url, image, email
	Help  string `json:"help"`
}

// Meta is the content of theme.json.
type Meta struct {
	Name       string                       `json:"name"`
	Menus      []string                     `json:"menus"`
	Settings   []Field                      `json:"settings"`
	Strings    map[string]string            `json:"strings"`
	Admin      []string                     `json:"admin"`
	Components map[string]map[string]string `json:"components"`
}

// Theme is one parsed theme folder. Templates are parsed once; a small pool of
// parsed copies lets concurrent requests each bind their own helper context.
type Theme struct {
	Name       string
	Dir        string
	Meta       Meta
	PageNames  []string // files in pages/ without .html
	Components []string // component names (file names)
	Has404     bool
	assetVer   map[string]string
	src        sources
	pool       chan *tset
}

type sources struct {
	layout     string
	partials   map[string]string
	components map[string]string
	pages      map[string]string
	notFound   string
}

// tset is one parsed copy of all templates of a theme, bound to its own holder.
type tset struct {
	h        *holder
	pages    map[string]*template.Template
	notFound *template.Template
}

// AssetURL returns the public URL of a theme asset with a cache-busting version.
func (t *Theme) AssetURL(p string) string {
	p = strings.TrimPrefix(p, "/")
	u := "/_t/" + t.Name + "/" + p
	if v := t.assetVer[p]; v != "" {
		u += "?v=" + v
	}
	return u
}

// HasAsset reports whether a file exists in assets/.
func (t *Theme) HasAsset(p string) bool { _, ok := t.assetVer[strings.TrimPrefix(p, "/")]; return ok }

// HasComponent reports whether the theme has components/<name>.html.
func (t *Theme) HasComponent(name string) bool {
	_, ok := t.src.components[name]
	return ok
}

// HasPage reports whether pages/<name>.html exists.
func (t *Theme) HasPage(name string) bool {
	_, ok := t.src.pages[name]
	return ok
}

func readDirHTML(dir string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[strings.TrimSuffix(e.Name(), ".html")] = string(b)
	}
	return out, nil
}

// LoadTheme reads and parses a theme folder.
func LoadTheme(dir string) (*Theme, error) {
	t := &Theme{Name: filepath.Base(dir), Dir: dir, assetVer: map[string]string{}, pool: make(chan *tset, 8)}
	if b, err := os.ReadFile(filepath.Join(dir, "theme.json")); err == nil {
		if err := json.Unmarshal(b, &t.Meta); err != nil {
			return nil, fmt.Errorf("theme.json: %w", err)
		}
	}
	if t.Meta.Name == "" {
		t.Meta.Name = t.Name
	}
	b, err := os.ReadFile(filepath.Join(dir, "layout.html"))
	if err != nil {
		return nil, errors.New("layout.html is missing")
	}
	t.src.layout = string(b)
	if t.src.partials, err = readDirHTML(filepath.Join(dir, "partials")); err != nil {
		return nil, err
	}
	if t.src.components, err = readDirHTML(filepath.Join(dir, "components")); err != nil {
		return nil, err
	}
	if t.src.pages, err = readDirHTML(filepath.Join(dir, "pages")); err != nil {
		return nil, err
	}
	if _, ok := t.src.pages["default"]; !ok {
		return nil, errors.New("pages/default.html is missing")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "404.html")); err == nil {
		t.src.notFound, t.Has404 = string(b), true
	}
	for n := range t.src.pages {
		t.PageNames = append(t.PageNames, n)
	}
	sort.Strings(t.PageNames)
	for n := range t.src.components {
		t.Components = append(t.Components, n)
	}
	sort.Strings(t.Components)
	// asset versions: first 8 hex chars of the SHA-256 of each file, computed once.
	assets := filepath.Join(dir, "assets")
	_ = filepath.WalkDir(assets, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		f.Close()
		rel, _ := filepath.Rel(assets, p)
		t.assetVer[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))[:8]
		return nil
	})
	ts, err := t.build()
	if err != nil {
		return nil, err
	}
	for name := range t.src.components {
		if ts.pages["default"].Lookup("c/"+name) == nil {
			return nil, fmt.Errorf("components/%s.html must define \"c/%s\"", name, name)
		}
	}
	t.pool <- ts
	return t, nil
}

// build parses one full copy of the theme's templates.
func (t *Theme) build() (*tset, error) {
	h := &holder{}
	funcs := h.funcMap()
	base := template.New("layout.html").Funcs(funcs)
	if _, err := base.Parse(t.src.layout); err != nil {
		return nil, fmt.Errorf("layout.html: %w", err)
	}
	add := func(tpl *template.Template, kind string, files map[string]string) error {
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if _, err := tpl.New(kind + "/" + n + ".html").Parse(files[n]); err != nil {
				return fmt.Errorf("%s/%s.html: %w", kind, n, err)
			}
		}
		return nil
	}
	if err := add(base, "partials", t.src.partials); err != nil {
		return nil, err
	}
	if err := add(base, "components", t.src.components); err != nil {
		return nil, err
	}
	ts := &tset{h: h, pages: map[string]*template.Template{}}
	one := func(name, src string) (*template.Template, error) {
		c, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := c.New("page:" + name).Parse(src); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return c, nil
	}
	for name, src := range t.src.pages {
		c, err := one("pages/"+name+".html", src)
		if err != nil {
			return nil, err
		}
		ts.pages[name] = c
	}
	nf := t.src.notFound
	if !t.Has404 {
		nf = `{{define "content"}}<h1>404</h1><p>Page not found.</p>{{end}}`
	}
	c, err := one("404.html", nf)
	if err != nil {
		return nil, err
	}
	ts.notFound = c
	return ts, nil
}

func (t *Theme) get() (*tset, error) {
	select {
	case ts := <-t.pool:
		return ts, nil
	default:
		return t.build()
	}
}

func (t *Theme) put(ts *tset) {
	ts.h.rc = nil
	select {
	case t.pool <- ts:
	default: // pool full: drop it
	}
}

// ---------- theme set ----------

// Set holds every theme in themes/, shared by all sites.
type Set struct {
	Dir    string
	mu     sync.RWMutex
	themes map[string]*Theme
	errs   map[string]string
}

// NewSet loads all themes of a folder.
func NewSet(dir string) *Set {
	s := &Set{Dir: dir}
	s.Reload()
	return s
}

// Reload re-parses every theme folder.
func (s *Set) Reload() {
	themes, errs := map[string]*Theme{}, map[string]string{}
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		t, err := LoadTheme(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			errs[e.Name()] = err.Error()
			continue
		}
		themes[e.Name()] = t
	}
	s.mu.Lock()
	s.themes, s.errs = themes, errs
	s.mu.Unlock()
}

// Get returns a theme or nil.
func (s *Set) Get(name string) *Theme {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.themes[name]
}

// ThemeInfo is a row of the main admin Themes list.
type ThemeInfo struct {
	Name  string
	Theme *Theme
	Error string
}

// List returns all theme folders with their load error, if any.
func (s *Set) List() []ThemeInfo {
	s.mu.RLock()
	var out []ThemeInfo
	for n, t := range s.themes {
		out = append(out, ThemeInfo{Name: n, Theme: t})
	}
	for n, e := range s.errs {
		out = append(out, ThemeInfo{Name: n, Error: e})
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns the names of all loaded themes.
func (s *Set) Names() []string {
	var out []string
	for _, i := range s.List() {
		if i.Theme != nil {
			out = append(out, i.Name)
		}
	}
	return out
}

// ---------- theme check ----------

var (
	seoCallRe     = regexp.MustCompile(`\{\{-?\s*seo\s*-?\}\}`)
	bodyEndCallRe = regexp.MustCompile(`\{\{-?\s*bodyEnd\s*-?\}\}`)
	contentRe     = regexp.MustCompile(`\{\{-?\s*(block|template)\s+"content"`)
	assetRe       = regexp.MustCompile(`asset\s+"([^"]+)"`)
)

// Check loads a theme folder and returns problems (errors) and warnings.
func Check(dir string) (problems, warnings []string) {
	t, err := LoadTheme(dir)
	if err != nil {
		return []string{err.Error()}, nil
	}
	if !seoCallRe.MatchString(t.src.layout) {
		problems = append(problems, "layout.html has no {{seo}} call in <head>")
	}
	if !bodyEndCallRe.MatchString(t.src.layout) {
		warnings = append(warnings, "layout.html has no {{bodyEnd}} call before </body>")
	}
	if !contentRe.MatchString(t.src.layout) {
		problems = append(problems, `layout.html has no {{block "content" .}} (or {{template "content" .}})`)
	}
	if !t.Has404 {
		warnings = append(warnings, "404.html is missing (a plain default is used)")
	}
	all := map[string]string{"layout.html": t.src.layout, "404.html": t.src.notFound}
	for n, s := range t.src.partials {
		all["partials/"+n+".html"] = s
	}
	for n, s := range t.src.components {
		all["components/"+n+".html"] = s
	}
	for n, s := range t.src.pages {
		all["pages/"+n+".html"] = s
	}
	files := make([]string, 0, len(all))
	for f := range all {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		for _, m := range assetRe.FindAllStringSubmatch(all[f], -1) {
			if !t.HasAsset(m[1]) {
				problems = append(problems, fmt.Sprintf("%s: asset %q not found in assets/", f, m[1]))
			}
		}
	}
	for name := range t.Meta.Components {
		if !t.HasComponent(name) {
			warnings = append(warnings, fmt.Sprintf("theme.json lists component %q but components/%s.html is missing", name, name))
		}
	}
	for _, sec := range t.Meta.Admin {
		switch sec {
		case "pages", "menus", "seo", "settings", "media", "modules", "users":
		default:
			warnings = append(warnings, fmt.Sprintf("theme.json admin: unknown section %q", sec))
		}
	}
	return problems, warnings
}
