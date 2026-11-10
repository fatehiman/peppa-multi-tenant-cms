// Package mainadmin is the panel on the dedicated panel host: create, edit and disable sites.
package mainadmin

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hostkit/internal/adminui"
	"hostkit/internal/auth"
	"hostkit/internal/backup"
	"hostkit/internal/captcha"
	"hostkit/internal/core"
	"hostkit/internal/tenant"
)

//go:embed tmpl/*.html
var tmplFS embed.FS

// Handler serves the main admin panel.
type Handler struct {
	App  *core.App
	tmpl map[string]*template.Template
	mux  *http.ServeMux
}

type req struct {
	W     http.ResponseWriter
	R     *http.Request
	Sess  *auth.Session
	Email string
}

// New builds the main admin handler.
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
	t, err := adminui.Parse(files, nil)
	if err != nil {
		return nil, err
	}
	h := &Handler{App: app, tmpl: t, mux: http.NewServeMux()}
	m := h.mux
	m.HandleFunc("GET /login", h.loginForm)
	m.HandleFunc("POST /login", h.login)
	m.HandleFunc("GET /_captcha", func(w http.ResponseWriter, r *http.Request) { app.ServeCaptcha(w, r, "main") })
	m.HandleFunc("POST /logout", h.need(h.logout))
	m.HandleFunc("GET /{$}", h.need(h.sites))
	m.HandleFunc("GET /sites/new", h.need(h.siteForm))
	m.HandleFunc("GET /sites/{id}", h.need(h.siteForm))
	m.HandleFunc("POST /sites/save", h.need(h.siteSave))
	m.HandleFunc("POST /sites/{id}/status", h.need(h.siteStatus))
	m.HandleFunc("POST /sites/{id}/open", h.need(h.siteOpen))
	m.HandleFunc("GET /sites/{id}/backup", h.need(h.siteBackup))
	m.HandleFunc("POST /sites/{id}/delete", h.need(h.siteDelete))
	m.HandleFunc("GET /themes", h.need(h.themes))
	m.HandleFunc("POST /themes/reload", h.need(h.reload))
	m.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) need(fn func(q *req)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := h.App.Auth.Get(r, "main")
		if s == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		q := &req{W: w, R: r, Sess: s}
		if err := h.App.Reg.Main.QueryRow("SELECT email FROM main_admins WHERE id = ?", s.UserID()).Scan(&q.Email); err != nil {
			h.App.Auth.Destroy(w, r, "main", "/")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !auth.CheckCSRF(r, s) {
			http.Error(w, "Bad CSRF token. Go back, reload the page and try again.", http.StatusForbidden)
			return
		}
		fn(q)
	}
}

func (h *Handler) render(q *req, screen, title, active string, data any) {
	p := adminui.Page{Title: title, Brand: "hostkit", D: data, Flash: q.R.URL.Query().Get("ok"), Error: q.R.URL.Query().Get("err"),
		LogoutURL: "/logout"}
	if q.Sess != nil {
		p.User, p.CSRF = q.Email, q.Sess.CSRF
		p.Nav = []adminui.NavItem{{Label: "Sites", URL: "/", Icon: "globe", Active: active == "sites"},
			{Label: "Themes", URL: "/themes", Icon: "layers", Active: active == "themes"}}
	}
	t := h.tmpl[screen]
	q.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(q.W, "base", p); err != nil {
		slog.Error("main admin template", "screen", screen, "err", err)
	}
}

func (q *req) back(path, okMsg, errMsg string) {
	v := url.Values{}
	if okMsg != "" {
		v.Set("ok", okMsg)
	}
	if errMsg != "" {
		v.Set("err", errMsg)
	}
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	http.Redirect(q.W, q.R, path, http.StatusSeeOther)
}

func (q *req) id() int64 {
	n, _ := strconv.ParseInt(q.R.PathValue("id"), 10, 64)
	return n
}

// ---------- login ----------

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	h.renderLogin(&req{W: w, R: r}, "", "")
}

// renderLogin shows the login form; the CAPTCHA is added after core.CaptchaAfter failures.
func (h *Handler) renderLogin(q *req, email, errMsg string) {
	data := map[string]any{"Email": email}
	if h.App.LoginNeedsCaptcha("main", auth.ClientIP(q.R), email) {
		ph, title := captcha.Texts("en")
		data["Captcha"] = captcha.Widget("/_captcha", "", ph, title)
	}
	if errMsg != "" {
		v := q.R.URL.Query()
		v.Set("err", errMsg)
		q.R.URL.RawQuery = v.Encode()
	}
	h.render(q, "login", "Log in", "", data)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	q := &req{W: w, R: r}
	ip := auth.ClientIP(r)
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if msg := h.App.LoginGate(r, "main", ip, email); msg != "" {
		h.renderLogin(q, email, msg)
		return
	}
	var id int64
	var hash string
	err := h.App.Reg.Main.QueryRow("SELECT id, password_hash FROM main_admins WHERE email = ?", email).Scan(&id, &hash)
	if err != nil {
		auth.BurnTime()
	}
	if err != nil || !auth.CheckPassword(hash, r.FormValue("password")) {
		h.App.LoginFailed("main", ip, email)
		h.renderLogin(q, email, "Wrong email or password.")
		return
	}
	h.App.LoginOK("main", ip, email)
	if _, err := h.App.Auth.Create(w, "main", "main:"+strconv.FormatInt(id, 10), "/"); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(q *req) {
	h.App.Auth.Destroy(q.W, q.R, "main", "/")
	http.Redirect(q.W, q.R, "/login", http.StatusSeeOther)
}

// ---------- sites ----------

type siteRow struct {
	*tenant.Site
	Primary string
	URL     string
	Hosts   string
}

func (h *Handler) siteURL(s *tenant.Site) string {
	return h.App.Cfg.Scheme + "://" + s.Domain + s.BasePath + "/"
}

func (h *Handler) sites(q *req) {
	var rows []siteRow
	for _, s := range h.App.Reg.All() {
		r := siteRow{Site: s, URL: h.siteURL(s)}
		var hs []string
		for _, x := range s.Hosts {
			hs = append(hs, x.String())
		}
		if len(hs) > 0 {
			r.Primary = hs[0]
		}
		r.Hosts = strings.Join(hs, ", ")
		rows = append(rows, r)
	}
	h.render(q, "sites", "Sites", "sites", map[string]any{"Sites": rows})
}

type siteForm struct {
	S         *tenant.Site
	IsNew     bool
	HostsText string
	Themes    []string
	Sections  []string
	All       []string
	URL       string
	Email     string
}

func (h *Handler) formData(s *tenant.Site, hostsText string) siteForm {
	f := siteForm{S: s, IsNew: s.ID == 0, HostsText: hostsText, Themes: h.App.Themes.Names(), Sections: s.AdminSections,
		All: core.AllSections}
	if f.HostsText == "" {
		var hs []string
		for _, x := range s.Hosts {
			hs = append(hs, x.String())
		}
		f.HostsText = strings.Join(hs, "\n")
	}
	if s.ID != 0 {
		f.URL = h.siteURL(s)
	}
	return f
}

func (h *Handler) siteForm(q *req) {
	s := &tenant.Site{Theme: "default", Lang: "en", AdminEnabled: true}
	title := "Add site"
	if id := q.id(); id != 0 {
		if s = h.App.Reg.ByID(id); s == nil {
			q.back("/", "", "Site not found.")
			return
		}
		title = s.Name
	}
	h.render(q, "site_edit", title, "sites", h.formData(s, ""))
}

func (h *Handler) siteSave(q *req) {
	r := q.R
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	in := tenant.SiteInput{
		Key:          strings.ToLower(strings.TrimSpace(r.FormValue("key"))),
		Name:         strings.TrimSpace(r.FormValue("name")),
		Theme:        r.FormValue("theme"),
		Lang:         strings.TrimSpace(r.FormValue("lang")),
		AdminEnabled: r.FormValue("admin_enabled") == "1",
	}
	in.AdminSections = strings.Join(r.Form["sections"], ",")
	draft := &tenant.Site{ID: id, Key: in.Key, Name: in.Name, Theme: in.Theme, Lang: in.Lang, AdminEnabled: in.AdminEnabled,
		AdminSections: tenant.SplitList(in.AdminSections)}
	if id != 0 {
		if old := h.App.Reg.ByID(id); old != nil {
			draft.Key, draft.Status = old.Key, old.Status
		}
	}
	hostsText := r.FormValue("hosts")
	fail := func(msg string) {
		v := r.URL.Query()
		v.Set("err", msg)
		r.URL.RawQuery = v.Encode()
		f := h.formData(draft, hostsText)
		f.Email = r.FormValue("admin_email")
		h.render(q, "site_edit", "Site", "sites", f)
	}
	hosts, err := tenant.ParseHosts(hostsText)
	if err != nil {
		fail(err.Error())
		return
	}
	in.Hosts = hosts
	if h.App.Themes.Get(in.Theme) == nil {
		fail("Choose a theme.")
		return
	}
	if id == 0 {
		email := strings.ToLower(strings.TrimSpace(r.FormValue("admin_email")))
		if email != "" {
			hash, err := auth.HashPassword(r.FormValue("admin_password"))
			if err != nil {
				fail("First site admin: " + err.Error())
				return
			}
			in.AdminEmail, in.AdminPasswordHash = email, hash
		}
		s, err := h.App.Reg.CreateSite(in)
		if err != nil {
			fail(err.Error())
			return
		}
		q.back("/sites/"+strconv.FormatInt(s.ID, 10), "Site created. It is live now.", "")
		return
	}
	if err := h.App.Reg.UpdateSite(id, in); err != nil {
		fail(err.Error())
		return
	}
	if s := h.App.Reg.ByID(id); s != nil {
		h.App.Cache.Invalidate(s.Key)
	}
	q.back("/sites/"+strconv.FormatInt(id, 10), "Saved.", "")
}

func (h *Handler) siteStatus(q *req) {
	s := h.App.Reg.ByID(q.id())
	if s == nil {
		q.back("/", "", "Site not found.")
		return
	}
	status := "disabled"
	if q.R.FormValue("status") == "active" {
		status = "active"
	}
	if err := h.App.Reg.SetStatus(s.ID, status); err != nil {
		q.back("/", "", err.Error())
		return
	}
	h.App.Cache.Invalidate(s.Key)
	q.back("/sites/"+strconv.FormatInt(s.ID, 10), "Site is "+status+" now.", "")
}

// siteOpen hands the main admin over to the site admin on the site's own domain.
func (h *Handler) siteOpen(q *req) {
	s := h.App.Reg.ByID(q.id())
	if s == nil {
		q.back("/", "", "Site not found.")
		return
	}
	token, err := h.App.Auth.HandOver(s.Key, "main:"+strconv.FormatInt(q.Sess.UserID(), 10))
	if err != nil {
		q.back("/", "", err.Error())
		return
	}
	http.Redirect(q.W, q.R, h.App.Cfg.Scheme+"://"+s.Domain+s.BasePath+core.AdminPath+"/open?t="+url.QueryEscape(token), http.StatusSeeOther)
}

func (h *Handler) siteBackup(q *req) {
	s := h.App.Reg.ByID(q.id())
	if s == nil {
		http.NotFound(q.W, q.R)
		return
	}
	name := s.Key + "-" + time.Now().Format("20060102-1504") + ".zip"
	q.W.Header().Set("Content-Type", "application/zip")
	q.W.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := backup.WriteZip(q.W, h.App.Reg, s); err != nil {
		slog.Error("backup zip", "site", s.Key, "err", err)
	}
}

func (h *Handler) siteDelete(q *req) {
	s := h.App.Reg.ByID(q.id())
	if s == nil {
		q.back("/", "", "Site not found.")
		return
	}
	if q.R.FormValue("confirm") != s.Key {
		q.back("/sites/"+strconv.FormatInt(s.ID, 10), "", "Type the site key to delete the site.")
		return
	}
	if err := h.App.Reg.DeleteSite(s.ID); err != nil {
		q.back("/", "", err.Error())
		return
	}
	h.App.Cache.Invalidate(s.Key)
	q.back("/", "Site "+s.Key+" deleted (folder moved to sites/.trash/).", "")
}

// ---------- themes ----------

func (h *Handler) themes(q *req) {
	type row struct {
		Name, Error, Title string
		Pages, Components  []string
		Used               []string
	}
	var rows []row
	for _, t := range h.App.Themes.List() {
		r := row{Name: t.Name, Error: t.Error}
		if t.Theme != nil {
			r.Title, r.Pages, r.Components = t.Theme.Meta.Name, t.Theme.PageNames, t.Theme.Components
		}
		for _, s := range h.App.Reg.All() {
			if s.Theme == t.Name {
				r.Used = append(r.Used, s.Key)
			}
		}
		rows = append(rows, r)
	}
	h.render(q, "themes", "Themes", "themes", map[string]any{"Themes": rows, "Dir": h.App.Cfg.ThemesDir()})
}

// reload re-parses themes, reloads the domain map, closes site databases (so a restored
// site.db is opened fresh) and clears the page cache.
func (h *Handler) reload(q *req) {
	Reload(h.App)
	q.back("/themes", "Themes and sites reloaded.", "")
}

// Reload is also used for SIGHUP.
func Reload(app *core.App) {
	app.Themes.Reload()
	_ = app.Reg.Reload()
	app.Reg.CloseAll()
	app.Cache.Clear()
	// make sure every site folder has an uploads dir (restored archives may lack one)
	for _, s := range app.Reg.All() {
		_ = os.MkdirAll(filepath.Join(app.Reg.SiteDir(s.Key), "uploads"), 0o755)
	}
}
