// Package core holds the shared application state used by the router and both admin panels.
package core

import (
	"context"
	"database/sql"
	"net/http"

	"hostkit/internal/auth"
	"hostkit/internal/config"
	"hostkit/internal/render"
	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// App is the whole running hostkit.
type App struct {
	Cfg        *config.Config
	Reg        *tenant.Registry
	Auth       *auth.Manager
	Themes     *render.Set
	Cache      *render.Cache
	LoginLimit *auth.Limiter // 5 failures per IP per 15 minutes
	FormLimit  *auth.Limiter // public module forms
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

// AllSections are the site admin sections in menu order.
var AllSections = []string{"pages", "menus", "seo", "settings", "media", "modules", "users"}
