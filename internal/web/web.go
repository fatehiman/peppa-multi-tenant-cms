// Package web is the host router and the public page handler.
package web

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hostkit/internal/auth"
	"hostkit/internal/core"
	"hostkit/internal/module"
	"hostkit/internal/render"
	"hostkit/internal/seo"
	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// Router sends each request to the main admin, a site admin or a public page.
type Router struct {
	App       *core.App
	MainAdmin http.Handler
	SiteAdmin http.Handler
}

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		if v := recover(); v != nil {
			slog.Error("panic", "err", v, "host", r.Host, "path", r.URL.Path)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	rt.route(w, r)
	slog.Debug("request", "host", r.Host, "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
}

func (rt *Router) route(w http.ResponseWriter, r *http.Request) {
	app := rt.App
	if app.Cfg.Static && (strings.HasPrefix(r.URL.Path, "/_t/") || strings.HasPrefix(r.URL.Path, "/_m/")) {
		rt.serveStatic(w, r)
		return
	}
	host := tenant.NormalizeHost(r.Host)
	if app.Cfg.PanelHost != "" && host == app.Cfg.PanelHost {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		rt.MainAdmin.ServeHTTP(w, r)
		return
	}
	m := app.Reg.Lookup(r.Host, r.URL.Path)
	if m.Unknown {
		d := strings.TrimPrefix(m.Domain, "www.")
		if app.Reg.KnownDomain(m.Domain) || app.Reg.KnownDomain(d) {
			if r.URL.Path == "/robots.txt" {
				sisters := app.Reg.SisterSites(m.Domain)
				if len(sisters) == 0 {
					sisters = app.Reg.SisterSites(d)
				}
				writeText(w, "text/plain; charset=utf-8", seo.DefaultRobots(app.Cfg.Scheme, sisters))
				return
			}
		}
		if app.Cfg.Fallback != "" && !app.Reg.KnownDomain(m.Domain) && !app.Reg.KnownDomain(d) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, app.Cfg.Fallback)
			return
		}
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	s := m.Site
	if !m.Host.Primary || m.WWW {
		target := s.BasePath + m.Rest
		if s.BasePath != "" && m.Rest == "/" {
			target = s.BasePath
		}
		u := url.URL{Scheme: app.Cfg.Scheme, Host: s.Domain, Path: target, RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
		return
	}
	rest := m.Rest
	if rest == "/_captcha" { // CAPTCHA pictures: public forms and the admin login
		app.ServeCaptcha(w, r, core.SiteScope(s.Key))
		return
	}
	isAdmin := rest == core.AdminPath || strings.HasPrefix(rest, core.AdminPath+"/")
	if s.Disabled() && !isAdmin {
		if rest == "/robots.txt" {
			writeText(w, "text/plain; charset=utf-8", "User-agent: *\nDisallow: /\n")
			return
		}
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta name="robots" content="noindex"><title>Maintenance</title></head>`+
			`<body style="font-family:system-ui,sans-serif;text-align:center;padding:12vh 16px;color:#444"><h1>We will be back soon</h1><p>This website is under maintenance.</p></body></html>`)
		return
	}
	d, err := app.Reg.DB(s)
	if err != nil {
		slog.Error("open site db", "site", s.Key, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = rest
	r2.URL.RawPath = ""
	r2 = core.WithSite(r2, &core.SiteReq{Site: s, DB: d, Path: rest})
	if isAdmin {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Cache-Control", "no-store")
		rt.SiteAdmin.ServeHTTP(w, r2)
		return
	}
	rt.public(w, r2, r.URL.Path)
}

func writeText(w http.ResponseWriter, ctype, body string) {
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write([]byte(body))
}

// serveStatic serves theme assets and uploads (only with -static / -dev).
func (rt *Router) serveStatic(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 3 || strings.Contains(r.URL.Path, "..") || !tenant.ValidKey(parts[1]) {
		http.NotFound(w, r)
		return
	}
	var base string
	if parts[0] == "_t" {
		base = filepath.Join(rt.App.Cfg.ThemesDir(), parts[1], "assets")
	} else {
		base = filepath.Join(rt.App.Cfg.SitesDir(), parts[1], "uploads")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	http.ServeFile(w, r, filepath.Join(base, filepath.FromSlash(parts[2])))
}

func hasUpper(s string) bool {
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

// public serves robots.txt, sitemap.xml, redirects, module routes and pages.
func (rt *Router) public(w http.ResponseWriter, r *http.Request, fullPath string) {
	app := rt.App
	sr := core.Site(r)
	s, d, path := sr.Site, sr.DB, sr.Path
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	settings := core.Settings(d)

	// Under construction: only people logged in to this site's admin see the site.
	if core.UnderConstruction(settings) {
		if app.Auth.Get(r, s.Key) == nil {
			rt.underConstruction(w, r, s, d, settings)
			return
		}
		// a logged-in viewer: the CDN and shared caches must never keep this response
		w = privateWriter{w}
	}

	switch path {
	case "/robots.txt":
		writeText(w, "text/plain; charset=utf-8", seo.Robots(app.Cfg.Scheme, s, settings, app.Reg.SisterSites(s.Domain)))
		return
	case "/sitemap.xml":
		if e := app.Cache.Get(s.Key, "/sitemap.xml"); e != nil {
			writeEntry(w, r, e)
			return
		}
		pages, err := store.SitemapPages(d)
		if err != nil {
			rt.fail(w, err)
			return
		}
		urls := seo.PageURLs(pages)
		mctx := &module.Ctx{Site: s, DB: d, Settings: settings}
		for _, m := range module.ForSite(s.Key) {
			if sm, ok := m.(module.Sitemapper); ok {
				mctx.Store = module.Store{DB: d, Module: m.Name()}
				for _, u := range sm.Sitemap(mctx) {
					urls = append(urls, seo.URL(u))
				}
			}
		}
		e := app.Cache.Put(s.Key, "/sitemap.xml", "application/xml; charset=utf-8", 200, seo.Sitemap(app.Cfg.Scheme, s, urls))
		writeEntry(w, r, e)
		return
	}

	// Canonical URL rules: no trailing slash (except "/"), lower-case path.
	if get {
		target := fullPath
		if len(target) > 1 && strings.HasSuffix(target, "/") {
			target = strings.TrimRight(target, "/")
			if target == "" {
				target = "/"
			}
		}
		if hasUpper(target) {
			target = strings.ToLower(target)
		}
		if target != fullPath {
			u := url.URL{Path: target, RawQuery: r.URL.RawQuery}
			http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
			return
		}
	}

	cacheKey := path
	if r.URL.RawQuery != "" {
		cacheKey += "?" + r.URL.RawQuery
	}
	if get {
		if e := app.Cache.Get(s.Key, cacheKey); e != nil {
			writeEntry(w, r, e)
			return
		}
	}

	if red, err := store.FindRedirect(d, path); err == nil && red != nil {
		store.HitRedirect(d, red.ID)
		if red.Code == 410 || red.ToURL == "" {
			w.WriteHeader(http.StatusGone)
			fmt.Fprint(w, "Gone")
			return
		}
		to := red.ToURL
		if strings.HasPrefix(to, "/") {
			to = s.BasePath + to
		}
		code := red.Code
		if code != 302 {
			code = 301
		}
		http.Redirect(w, r, to, code)
		return
	}

	theme := app.Theme(s)
	if theme == nil {
		rt.fail(w, fmt.Errorf("theme %q is not loaded", s.Theme))
		return
	}
	rc := &render.RC{Site: s, DB: d, Theme: theme, Settings: settings, Scheme: app.Cfg.Scheme, Path: path,
		Query: r.URL.Query(), CSRF: func() string { return app.Auth.PublicCSRF(s.Key) }}

	mctx := func(m module.Module, p *store.Page) *module.Ctx {
		return &module.Ctx{Site: s, Page: p, DB: d, W: w, R: r, Store: module.Store{DB: d, Module: m.Name()},
			Settings: settings, Path: path, ClientIP: auth.ClientIP(r),
			CSRFValid:    func() bool { return app.Auth.CheckPublicCSRF(s.Key, r.FormValue("_csrf")) },
			CaptchaValid: func() bool { return app.Captcha.FromRequest(r, core.SiteScope(s.Key)) },
			Notify: func(title string, body ...string) {
				app.Notify.Send(title, s.Domain+s.BasePath+"-hostkit-"+m.Name(), body...)
			},
			Invalidate: func() { app.Cache.Invalidate(s.Key) }}
	}

	// Module routes that claim a URL prefix.
	for _, m := range module.ForSite(s.Key) {
		pr, ok := m.(module.PrefixRouter)
		if !ok || !strings.HasPrefix(path, pr.Prefix()) {
			continue
		}
		data, handled, err := m.Handle(mctx(m, nil))
		if err != nil {
			rt.fail(w, err)
			return
		}
		if handled {
			return
		}
		rc.Data = data
		rt.notFound(w, theme, rc)
		return
	}

	page, err := store.PageBySlug(d, strings.Trim(path, "/"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && page.Status != "published" && !rt.isEditor(r, s)) {
		rt.notFound(w, theme, rc)
		return
	}
	if err != nil {
		rt.fail(w, err)
		return
	}
	rc.Page = page
	cacheable := page.Status == "published"
	if page.Module != "" {
		m := module.Get(page.Module)
		if m != nil && module.Allowed(m, s.Key) {
			c, ok := m.(module.Cacheable)
			cacheable = cacheable && ok && c.Cacheable()
			data, handled, err := m.Handle(mctx(m, page))
			if err != nil {
				rt.fail(w, err)
				return
			}
			if handled {
				return
			}
			rc.Data = data
		}
	}
	t0 := time.Now()
	var buf bytes.Buffer
	if err := theme.RenderPage(&buf, rc); err != nil {
		rt.fail(w, err)
		return
	}
	slog.Debug("render", "site", s.Key, "path", path, "dur", time.Since(t0))
	if cacheable && get {
		e := app.Cache.Put(s.Key, cacheKey, "text/html; charset=utf-8", 200, buf.Bytes())
		writeEntry(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(buf.Bytes())
}

// isEditor reports whether the visitor is logged in to this site's admin (for draft preview).
func (rt *Router) isEditor(r *http.Request, s *tenant.Site) bool {
	return rt.App.Auth.Get(r, s.Key) != nil
}

func (rt *Router) notFound(w http.ResponseWriter, theme *render.Theme, rc *render.RC) {
	rc.Page = nil
	rc.Title = "404"
	if v := rc.Settings["t_not_found"]; v != "" {
		rc.Title = v
	}
	var buf bytes.Buffer
	if err := theme.Render404(&buf, rc); err != nil {
		slog.Error("render 404", "err", err)
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(buf.Bytes())
}

func (rt *Router) fail(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// writeEntry writes a cached response with ETag / Last-Modified and answers 304 when possible.
func writeEntry(w http.ResponseWriter, r *http.Request, e *render.Entry) {
	h := w.Header()
	h.Set("ETag", e.ETag)
	h.Set("Last-Modified", e.Modified.Format(http.TimeFormat))
	h.Set("Cache-Control", "no-cache")
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, t := range strings.Split(inm, ",") {
			if strings.TrimSpace(t) == e.ETag || strings.TrimSpace(t) == "W/"+e.ETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	} else if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !e.Modified.After(t) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Type", e.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(e.Body)))
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.Body)
	}
}

// privateWriter forces "Cache-Control: private, no-store" (pages shown to a logged-in
// admin while the site is under construction).
type privateWriter struct{ http.ResponseWriter }

func (p privateWriter) WriteHeader(code int) {
	p.Header().Set("Cache-Control", "private, no-store")
	p.ResponseWriter.WriteHeader(code)
}

func (p privateWriter) Write(b []byte) (int, error) {
	p.Header().Set("Cache-Control", "private, no-store")
	return p.ResponseWriter.Write(b)
}

var ucPage = template.Must(template.New("uc").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex,nofollow">
<title>{{.Name}}</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#f5f6f9;color:#2b3140;
font:17px/1.7 system-ui,-apple-system,"Segoe UI",Tahoma,sans-serif;text-align:center;padding:16px}
.box{max-width:560px}h1{font-size:30px;margin:0 0 10px}p{color:#6b7385;margin:0}</style></head>
<body><div class="box"><h1>{{.Name}}</h1><p>{{.Message}}</p></div></body></html>`))

// underConstruction answers a visitor of a site that is not public yet: 503 + Retry-After
// (search engines come back later and do not index the page), robots.txt disallows all.
func (rt *Router) underConstruction(w http.ResponseWriter, r *http.Request, s *tenant.Site, d *sql.DB, settings map[string]string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	if core.Site(r).Path == "/robots.txt" {
		writeText(w, "text/plain; charset=utf-8", "User-agent: *\nDisallow: /\n")
		return
	}
	h.Set("Retry-After", "86400")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	if theme := rt.App.Theme(s); theme != nil && theme.HasUC() {
		rc := &render.RC{Site: s, DB: d, Theme: theme, Settings: settings, Scheme: rt.App.Cfg.Scheme, Path: core.Site(r).Path,
			Query: r.URL.Query(), Title: s.Name}
		if err := theme.RenderUC(&buf, rc); err != nil {
			slog.Error("render under-construction", "site", s.Key, "err", err)
			buf.Reset()
		}
	}
	if buf.Len() == 0 {
		msg := settings["uc_message"]
		if msg == "" {
			msg = "This website is under construction. Please come back soon."
			if render.Dir(s.Lang) == "rtl" {
				msg = "این وب‌سایت در حال ساخت است. به زودی برمی‌گردیم."
			}
		}
		name := settings["site_title"]
		if name == "" {
			name = s.Name
		}
		_ = ucPage.Execute(&buf, map[string]string{"Lang": s.Lang, "Dir": render.Dir(s.Lang), "Name": name, "Message": msg})
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}
