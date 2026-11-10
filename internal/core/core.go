// Package core holds the shared application state used by the router and both admin panels.
package core

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"hostkit/internal/auth"
	"hostkit/internal/captcha"
	"hostkit/internal/config"
	"hostkit/internal/notify"
	"hostkit/internal/render"
	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// App is the whole running hostkit.
type App struct {
	Cfg          *config.Config
	Reg          *tenant.Registry
	Auth         *auth.Manager
	Themes       *render.Set
	Cache        *render.Cache
	LoginLimit   *auth.Limiter // failed logins per IP (5 per 15 minutes)
	AccountLimit *auth.Limiter // failed logins per user name, from all IPs (10 per 15 minutes)
	FormLimit    *auth.Limiter // public module forms
	CaptchaLimit *auth.Limiter // CAPTCHA pictures per IP
	Captcha      *captcha.Store
	Notify       *notify.Telegram // nil or empty URL = no Telegram messages
}

// AdminPath is the URL path of the site admin panels.
const AdminPath = tenant.AdminPath

// CaptchaAfter is the number of failed logins (per IP or per user name) after which
// the login form asks for the CAPTCHA.
const CaptchaAfter = 2

func loginKeys(scope, ip, user string) (string, string) {
	return scope + "|ip|" + ip, scope + "|user|" + strings.ToLower(strings.TrimSpace(user))
}

// LoginNeedsCaptcha reports whether the next login try of ip (and user, when known)
// must solve the CAPTCHA. scope is "main" or "site:<key>".
func (a *App) LoginNeedsCaptcha(scope, ip, user string) bool {
	ipKey, userKey := loginKeys(scope, ip, user)
	if a.LoginLimit.Count(ipKey) >= CaptchaAfter {
		return true
	}
	return user != "" && a.AccountLimit.Count(userKey) >= CaptchaAfter
}

// LoginGate runs before the password check. It returns an error text when the try
// must stop: too many failures for the IP or for the user name, or a wrong CAPTCHA.
func (a *App) LoginGate(r *http.Request, scope, ip, user string) string {
	ipKey, userKey := loginKeys(scope, ip, user)
	if !a.LoginLimit.Allow(ipKey) || !a.AccountLimit.Allow(userKey) {
		return "Too many failed logins. Wait 15 minutes."
	}
	if a.LoginNeedsCaptcha(scope, ip, user) && !a.Captcha.FromRequest(r, scope) {
		return "Type the numbers in the picture."
	}
	return ""
}

// LoginFailed counts a wrong password for the IP and for the user name.
func (a *App) LoginFailed(scope, ip, user string) {
	ipKey, userKey := loginKeys(scope, ip, user)
	a.LoginLimit.Add(ipKey)
	a.AccountLimit.Add(userKey)
}

// LoginOK clears the counters after a good login.
func (a *App) LoginOK(scope, ip, user string) {
	ipKey, userKey := loginKeys(scope, ip, user)
	a.LoginLimit.Reset(ipKey)
	a.AccountLimit.Reset(userKey)
}

// ServeCaptcha answers GET <base>/_captcha?k=<id> (rate limited per IP).
func (a *App) ServeCaptcha(w http.ResponseWriter, r *http.Request, scope string) {
	ip := auth.ClientIP(r)
	if !a.CaptchaLimit.Allow(ip) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	a.CaptchaLimit.Add(ip)
	a.Captcha.ServeImage(w, r, scope)
}

// SiteScope is the CAPTCHA and login-limit scope of a site.
func SiteScope(key string) string { return "site:" + key }

// NewLimits fills the limiters, the CAPTCHA store with the standard values.
func (a *App) NewLimits() {
	a.LoginLimit = auth.NewLimiter(5, 15*time.Minute)
	a.AccountLimit = auth.NewLimiter(10, 15*time.Minute)
	a.FormLimit = auth.NewLimiter(5, 10*time.Minute)
	a.CaptchaLimit = auth.NewLimiter(60, 10*time.Minute)
	a.Captcha = captcha.New()
}

// SiteReq is the site context of one request (after host lookup).
type SiteReq struct {
	Site *tenant.Site
	DB   *sql.DB
	Path string // path without the site prefix
}

type ctxKey struct{}

// WithSite stores the site context in the request.
func WithSite(r *http.Request, s *SiteReq) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, s))
}

// Site returns the site context of a request (nil outside a site).
func Site(r *http.Request) *SiteReq {
	s, _ := r.Context().Value(ctxKey{}).(*SiteReq)
	return s
}

// Theme returns the theme of a site, or nil when it is not loaded.
func (a *App) Theme(s *tenant.Site) *render.Theme { return a.Themes.Get(s.Theme) }

// Settings loads the settings map of a site (empty map on error).
func Settings(db *sql.DB) map[string]string {
	m, err := store.AllSettings(db)
	if err != nil || m == nil {
		return map[string]string{}
	}
	return m
}

// Sections returns the admin sections a site shows: the main admin override, else
// theme.json "admin", else all. "pages" and "seo" are always on.
func (a *App) Sections(s *tenant.Site) []string {
	list := s.AdminSections
	if len(list) == 0 {
		if t := a.Theme(s); t != nil && len(t.Meta.Admin) > 0 {
			list = t.Meta.Admin
		}
	}
	if len(list) == 0 {
		list = AllSections
	}
	out := []string{}
	for _, sec := range AllSections {
		if sec == "pages" || sec == "seo" {
			out = append(out, sec)
			continue
		}
		for _, x := range list {
			if x == sec {
				out = append(out, sec)
			}
		}
	}
	return out
}

// UnderConstruction reports whether a site is switched to "under construction"
// (site setting under_construction = 1): only its logged-in admins see it.
func UnderConstruction(settings map[string]string) bool { return settings["under_construction"] == "1" }

// AllSections are the site admin sections in menu order.
var AllSections = []string{"pages", "menus", "seo", "settings", "media", "modules", "users"}
