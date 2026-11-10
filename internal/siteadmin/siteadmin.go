// Package siteadmin is the admin panel every site gets at /mainadmin (core.AdminPath). Its templates are
// embedded and shared by all sites; a theme never changes its look.
package siteadmin

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hostkit/internal/adminui"
	"hostkit/internal/auth"
	"hostkit/internal/captcha"
	"hostkit/internal/core"
	"hostkit/internal/store"
)

//go:embed tmpl/*.html
var tmplFS embed.FS

// Handler serves /mainadmin/* for the site in the request context.
type Handler struct {
	App  *core.App
	tmpl map[string]*template.Template
	mux  *http.ServeMux
}

// user is the logged-in person of a request.
type user struct {
	Email string
	Role  string // admin / editor
	Main  bool   // a main admin opened this site
	Sess  *auth.Session
	ID    int64
}

// req bundles everything a handler needs.
type req struct {
	*core.SiteReq
	W        http.ResponseWriter
	R        *http.Request
	U        *user
	A        string // admin base URL, like "/de/mainadmin"
	Sections []string
}

// New builds the site admin handler.
func New(app *core.App) (*Handler, error) {
	files := map[string]string{}
	entries, _ := fs.ReadDir(tmplFS, "tmpl")
	for _, e := range entries {
		b, err := tmplFS.ReadFile("tmpl/" + e.Name())
		if err != nil {
			return nil, err
		}
		files[strings.TrimSuffix(e.Name(), ".html")] = string(b)
	}
	t, err := adminui.Parse(files, template.FuncMap{
		"rowdata": func(a, csrf, cur string, it *store.MenuItem) map[string]any {
			return map[string]any{"A": a, "CSRF": csrf, "Cur": cur, "It": it}
		},
	})
	if err != nil {
		return nil, err
	}
	h := &Handler{App: app, tmpl: t, mux: http.NewServeMux()}
	h.routes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

type handlerFunc func(q *req)

// open wraps a handler that needs no login.
func (h *Handler) open(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sr := core.Site(r)
		fn(&req{SiteReq: sr, W: w, R: r, A: sr.Site.BasePath + core.AdminPath})
	}
}

// need wraps a handler that needs a login, an enabled section and the right role.
// section "" means any logged-in user.
func (h *Handler) need(section string, fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sr := core.Site(r)
		q := &req{SiteReq: sr, W: w, R: r, A: sr.Site.BasePath + core.AdminPath}
		q.U = h.currentUser(r, sr)
		if q.U == nil {
			if !sr.Site.AdminEnabled {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, q.A+"/login", http.StatusSeeOther)
			return
		}
		q.Sections = h.App.Sections(sr.Site)
		if section != "" && !q.can(section) {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && !auth.CheckCSRF(r, q.U.Sess) {
			http.Error(w, "Bad CSRF token. Go back, reload the page and try again.", http.StatusForbidden)
			return
		}
		fn(q)
	}
}

// can reports whether the user may open a section (enabled for the site + role).
func (q *req) can(section string) bool {
	on := false
	for _, s := range q.Sections {
		if s == section {
			on = true
		}
	}
	if !on {
		return false
	}
	if q.U.Role == "admin" {
		return true
	}
	switch section {
	case "pages", "menus", "media", "modules":
		return true
	}
	return false
}

func (h *Handler) currentUser(r *http.Request, sr *core.SiteReq) *user {
	s := h.App.Auth.Get(r, sr.Site.Key)
	if s == nil {
		return nil
	}
	switch s.UserKind() {
	case "main":
		var email string
		if err := h.App.Reg.Main.QueryRow("SELECT email FROM main_admins WHERE id = ?", s.UserID()).Scan(&email); err != nil {
			return nil
		}
		return &user{Email: email, Role: "admin", Main: true, Sess: s, ID: s.UserID()}
	case "site":
		if !sr.Site.AdminEnabled {
			return nil
		}
		u, err := store.UserByID(sr.DB, s.UserID())
		if err != nil {
			return nil
		}
		return &user{Email: u.Email, Role: u.Role, Sess: s, ID: u.ID}
	}
	return nil
}

var sectionInfo = map[string][2]string{
	"pages": {"Pages", "file"}, "menus": {"Menus", "menu"}, "seo": {"SEO", "globe"}, "settings": {"Settings", "sliders"},
	"media": {"Media", "image"}, "modules": {"Modules", "box"}, "users": {"Users", "users"},
}

// render writes a screen with the base layout.
func (q *req) render(h *Handler, screen, title, active string, data any) {
	p := adminui.Page{Title: title, Brand: q.Site.Name, A: q.A, D: data,
		Flash: q.R.URL.Query().Get("ok"), Error: q.R.URL.Query().Get("err"),
		ViewURL: q.Site.BasePath + "/", LogoutURL: q.A + "/logout"}
	if q.U != nil {
		p.User, p.CSRF = q.U.Email, q.U.Sess.CSRF
		if core.UnderConstruction(settingsOf(q)) {
			p.Notice = "Under construction"
		}
		for _, sec := range q.Sections {
			if sec == "modules" && len(moduleScreens(q.Site.Key)) == 0 {
				continue
			}
			if q.can(sec) {
				info := sectionInfo[sec]
				p.Nav = append(p.Nav, adminui.NavItem{Label: info[0], URL: q.A + "/" + sec, Icon: info[1], Active: sec == active})
			}
		}
	}
	h.exec(q.W, screen, p)
}

func (h *Handler) exec(w http.ResponseWriter, screen string, p adminui.Page) {
	t := h.tmpl[screen]
	if t == nil {
		http.Error(w, "unknown screen "+screen, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", p); err != nil {
		slog.Error("site admin template", "screen", screen, "err", err)
	}
}

// renderErr shows a screen with an error message (used when a form fails validation).
func (q *req) renderErr(h *Handler, screen, title, active, msg string, data any) {
	vals := q.R.URL.Query()
	vals.Set("err", msg)
	q.R.URL.RawQuery = vals.Encode()
	q.render(h, screen, title, active, data)
}

// back redirects to an admin path with an ok or err message.
func (q *req) back(path, okMsg, errMsg string) {
	v := url.Values{}
	if okMsg != "" {
		v.Set("ok", okMsg)
	}
	if errMsg != "" {
		v.Set("err", errMsg)
	}
	u := q.A + path
	if len(v) > 0 {
		if strings.Contains(u, "?") {
			u += "&" + v.Encode()
		} else {
			u += "?" + v.Encode()
		}
	}
	http.Redirect(q.W, q.R, u, http.StatusSeeOther)
}

// saved clears the page cache of the site and marks it changed.
func (h *Handler) saved(q *req) {
	h.App.Cache.Invalidate(q.Site.Key)
	h.App.Reg.Touch(q.Site.ID)
}

func (q *req) fail(err error) {
	slog.Error("site admin", "site", q.Site.Key, "path", q.R.URL.Path, "err", err)
	http.Error(q.W, "Internal error: "+err.Error(), http.StatusInternalServerError)
}

func (q *req) id() int64 {
	n, _ := strconv.ParseInt(q.R.PathValue("id"), 10, 64)
	return n
}

func (q *req) cookiePath() string { return q.Site.BasePath + "/" }

// ---------- routes ----------

func (h *Handler) routes() {
	m := h.mux
	ap := core.AdminPath
	m.HandleFunc("GET "+ap, h.need("", func(q *req) { http.Redirect(q.W, q.R, q.A+"/pages", http.StatusSeeOther) }))
	m.HandleFunc("GET "+ap+"/{$}", h.need("", func(q *req) { http.Redirect(q.W, q.R, q.A+"/pages", http.StatusSeeOther) }))
	m.HandleFunc("GET "+ap+"/login", h.open(h.loginForm))
	m.HandleFunc("POST "+ap+"/login", h.open(h.login))
	m.HandleFunc("POST "+ap+"/logout", h.need("", h.logout))
	m.HandleFunc("GET "+ap+"/open", h.open(h.openHandOver))

	m.HandleFunc("GET "+ap+"/pages", h.need("pages", h.pagesList))
	m.HandleFunc("GET "+ap+"/pages/new", h.need("pages", h.pageEdit))
	m.HandleFunc("GET "+ap+"/pages/{id}", h.need("pages", h.pageEdit))
	m.HandleFunc("POST "+ap+"/pages/save", h.need("pages", h.pageSave))
	m.HandleFunc("POST "+ap+"/pages/{id}/delete", h.need("pages", h.pageDelete))
	m.HandleFunc("POST "+ap+"/pages/{id}/move", h.need("pages", h.pageMove))

	m.HandleFunc("GET "+ap+"/menus", h.need("menus", h.menus))
	m.HandleFunc("POST "+ap+"/menus/items/save", h.need("menus", h.menuItemSave))
	m.HandleFunc("POST "+ap+"/menus/items/{id}/delete", h.need("menus", h.menuItemDelete))
	m.HandleFunc("POST "+ap+"/menus/items/{id}/move", h.need("menus", h.menuItemMove))

	m.HandleFunc("GET "+ap+"/seo", h.need("seo", h.seoForm))
	m.HandleFunc("POST "+ap+"/seo", h.need("seo", h.seoSave))
	m.HandleFunc("POST "+ap+"/seo/redirects", h.need("seo", h.redirectAdd))
	m.HandleFunc("POST "+ap+"/seo/redirects/{id}/delete", h.need("seo", h.redirectDelete))

	m.HandleFunc("GET "+ap+"/settings", h.need("settings", h.settingsForm))
	m.HandleFunc("POST "+ap+"/settings", h.need("settings", h.settingsSave))

	m.HandleFunc("GET "+ap+"/media", h.need("media", h.mediaList))
	m.HandleFunc("POST "+ap+"/media/upload", h.need("media", h.mediaUpload))
	m.HandleFunc("POST "+ap+"/media/{id}/alt", h.need("media", h.mediaAlt))
	m.HandleFunc("POST "+ap+"/media/{id}/delete", h.need("media", h.mediaDelete))

	m.HandleFunc("GET "+ap+"/modules", h.need("modules", h.modulesList))
	m.HandleFunc(ap+"/modules/{module}/{screen}", h.need("modules", h.moduleScreen))

	m.HandleFunc("GET "+ap+"/users", h.need("users", h.usersList))
	m.HandleFunc("POST "+ap+"/users/save", h.need("users", h.userSave))
	m.HandleFunc("POST "+ap+"/users/{id}/delete", h.need("users", h.userDelete))
}

// ---------- login ----------

func (h *Handler) loginForm(q *req) {
	if !q.Site.AdminEnabled {
		http.NotFound(q.W, q.R)
		return
	}
	h.renderLogin(q, "", "")
}

// renderLogin shows the login form; the CAPTCHA is added after CaptchaAfter failures.
func (h *Handler) renderLogin(q *req, email, errMsg string) {
	data := map[string]any{"Email": email}
	if h.App.LoginNeedsCaptcha(core.SiteScope(q.Site.Key), auth.ClientIP(q.R), email) {
		ph, title := captcha.Texts("en")
		data["Captcha"] = captcha.Widget(q.Site.BasePath+"/_captcha", "", ph, title)
	}
	if errMsg != "" {
		q.renderErr(h, "login", "Log in", "", errMsg, data)
		return
	}
	q.render(h, "login", "Log in", "", data)
}

func (h *Handler) login(q *req) {
	if !q.Site.AdminEnabled {
		http.NotFound(q.W, q.R)
		return
	}
	ip := auth.ClientIP(q.R)
	scope := core.SiteScope(q.Site.Key)
	email := strings.ToLower(strings.TrimSpace(q.R.FormValue("email")))
	if msg := h.App.LoginGate(q.R, scope, ip, email); msg != "" {
		h.renderLogin(q, email, msg)
		return
	}
	u, err := store.UserByEmail(q.DB, email)
	if err != nil {
		auth.BurnTime()
	}
	if err != nil || !auth.CheckPassword(u.PasswordHash, q.R.FormValue("password")) {
		h.App.LoginFailed(scope, ip, email)
		h.renderLogin(q, email, "Wrong email or password.")
		return
	}
	h.App.LoginOK(scope, ip, email)
	if _, err := h.App.Auth.Create(q.W, q.Site.Key, "site:"+strconv.FormatInt(u.ID, 10), q.cookiePath()); err != nil {
		q.fail(err)
		return
	}
	http.Redirect(q.W, q.R, q.A+"/pages", http.StatusSeeOther)
}

func (h *Handler) logout(q *req) {
	h.App.Auth.Destroy(q.W, q.R, q.Site.Key, q.cookiePath())
	if q.U.Main || !q.Site.AdminEnabled {
		http.Redirect(q.W, q.R, q.Site.BasePath+"/", http.StatusSeeOther)
		return
	}
	http.Redirect(q.W, q.R, q.A+"/login", http.StatusSeeOther)
}

// openHandOver turns a one-time token from the main admin into a site session.
func (h *Handler) openHandOver(q *req) {
	ref, ok := h.App.Auth.TakeHandOver(q.Site.Key, q.R.URL.Query().Get("t"))
	if !ok || !strings.HasPrefix(ref, "main:") {
		http.Error(q.W, "This link is expired. Open the site admin again from the main admin.", http.StatusForbidden)
		return
	}
	if _, err := h.App.Auth.Create(q.W, q.Site.Key, ref, q.cookiePath()); err != nil {
		q.fail(err)
		return
	}
	http.Redirect(q.W, q.R, q.A+"/pages", http.StatusSeeOther)
}
