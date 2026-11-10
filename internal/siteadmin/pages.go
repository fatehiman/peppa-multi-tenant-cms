package siteadmin

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"hostkit/internal/module"
	"hostkit/internal/seo"
	"hostkit/internal/store"
)

type pageRow struct {
	*store.Page
	Path string
	URL  string
}

func (h *Handler) pagesList(q *req) {
	list, err := store.ListPages(q.DB)
	if err != nil {
		q.fail(err)
		return
	}
	rows := make([]pageRow, len(list))
	for i, p := range list {
		rows[i] = pageRow{Page: p, Path: "/" + p.Slug, URL: store.PagePath(q.Site.BasePath, p.Slug)}
	}
	q.render(h, "pages", "Pages", "pages", map[string]any{"Pages": rows})
}

type componentHint struct{ Name, Shortcode string }
type sister struct{ Name, Lang, URL string }

type pageForm struct {
	P            *store.Page
	IsNew        bool
	Templates    []string
	Modules      []string
	Components   []componentHint
	Sisters      []sister
	BaseURL      string // absolute URL of the site home, for the preview
	TitleSuffix  string
	DefaultImage string
	MediaBase    string    // "/_m/<site key>/"
	Robots       [2]string // index / follow parts
}

func (h *Handler) pageFormData(q *req, p *store.Page) pageForm {
	f := pageForm{P: p, IsNew: p.ID == 0, MediaBase: "/_m/" + q.Site.Key + "/"}
	settings := settingsOf(q)
	f.TitleSuffix = settings["seo_title_suffix"]
	f.DefaultImage = seo.AbsMedia(h.App.Cfg.Scheme, q.Site, settings["default_og_image"])
	f.BaseURL = seo.AbsURL(h.App.Cfg.Scheme, q.Site, "/")
	if t := h.App.Theme(q.Site); t != nil {
		f.Templates = t.PageNames
		names := make([]string, 0, len(t.Components))
		names = append(names, t.Components...)
		sort.Strings(names)
		for _, n := range names {
			sc := "[[" + n
			fields := t.Meta.Components[n]
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				sc += " " + k + `=""`
			}
			f.Components = append(f.Components, componentHint{Name: n, Shortcode: sc + "]]"})
		}
	}
	for _, m := range module.ForSite(q.Site.Key) {
		f.Modules = append(f.Modules, m.Name())
	}
	for _, s := range h.App.Reg.SisterSites(q.Site.Domain) {
		if s.ID != q.Site.ID {
			f.Sisters = append(f.Sisters, sister{Name: s.Name, Lang: s.Lang, URL: seo.AbsURL(h.App.Cfg.Scheme, s, "/")})
		}
	}
	idx, fol := "index", "follow"
	if strings.Contains(p.Robots, "noindex") {
		idx = "noindex"
	}
	if strings.Contains(p.Robots, "nofollow") {
		fol = "nofollow"
	}
	f.Robots = [2]string{idx, fol}
	return f
}

func (h *Handler) pageEdit(q *req) {
	p := &store.Page{Status: "draft", InSitemap: true}
	if id := q.id(); id != 0 {
		var err error
		if p, err = store.PageByID(q.DB, id); err != nil {
			q.back("/pages", "", "Page not found.")
			return
		}
	}
	title := "New page"
	if p.ID != 0 {
		title = "Edit page"
	}
	q.render(h, "page_edit", title, "pages", h.pageFormData(q, p))
}

// CleanSlug normalizes a slug: trims slashes and spaces, lower-cases,
// spaces become dashes. Unicode letters are allowed (for Persian, German, ...).
func CleanSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "/")
	s = strings.Join(strings.Fields(s), "-")
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '/' || r == '.' || r == '‌') {
			return "", errors.New("the slug may only have letters, digits, - _ . and /")
		}
	}
	if strings.Contains(s, "//") || strings.Contains(s, "..") {
		return "", errors.New("the slug has an empty or bad part")
	}
	first, _, _ := strings.Cut(s, "/")
	switch first {
	case "admin", "_t", "_m", "robots.txt", "sitemap.xml":
		return "", errors.New("this slug is reserved")
	}
	return s, nil
}

func (h *Handler) pageSave(q *req) {
	r := q.R
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	p := &store.Page{Status: "draft"}
	if id != 0 {
		var err error
		if p, err = store.PageByID(q.DB, id); err != nil {
			q.back("/pages", "", "Page not found.")
			return
		}
	}
	oldSlug := p.Slug
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	p.Title = v("title")
	p.BodyHTML = r.FormValue("body_html")
	p.Template = v("template")
	if p.Template == "default" {
		p.Template = ""
	}
	p.Module = v("module")
	p.ModuleSettings = r.FormValue("module_settings")
	p.Status = "draft"
	if v("status") == "published" {
		p.Status = "published"
	}
	p.SEOTitle = v("seo_title")
	p.MetaDescription = v("meta_description")
	p.Canonical = v("canonical")
	p.Robots = ""
	if ro := v("robots_index") + "," + v("robots_follow"); ro != "index,follow" && ro != "," {
		p.Robots = ro
	}
	p.OGTitle = v("og_title")
	p.OGDescription = v("og_description")
	p.OGImage = v("og_image")
	p.JSONLD = v("json_ld")
	p.Alternates = r.FormValue("alternates")
	p.SitemapPriority = v("sitemap_priority")
	p.SitemapChangefreq = v("sitemap_changefreq")
	p.InSitemap = r.FormValue("in_sitemap") == "1"

	fail := func(msg string) {
		q.renderErr(h, "page_edit", "Edit page", "pages", msg, h.pageFormData(q, p))
	}
	slug, err := CleanSlug(r.FormValue("slug"))
	if err != nil {
		fail(err.Error())
		return
	}
	p.Slug = slug
	if p.Title == "" {
		fail("The title is empty.")
		return
	}
	if p.JSONLD != "" && !json.Valid([]byte(p.JSONLD)) {
		fail("The JSON-LD block is not valid JSON.")
		return
	}
	if p.SitemapPriority != "" {
		f, err := strconv.ParseFloat(p.SitemapPriority, 64)
		if err != nil || f < 0 || f > 1 {
			fail("Sitemap priority must be a number from 0.0 to 1.0.")
			return
		}
	}
	if other, err := store.PageBySlug(q.DB, p.Slug); err == nil && other.ID != p.ID {
		fail("Another page already uses this slug.")
		return
	}
	if err := store.SavePage(q.DB, p); err != nil {
		q.fail(err)
		return
	}
	if id != 0 && oldSlug != p.Slug && r.FormValue("add_redirect") == "1" {
		_ = store.SaveRedirect(q.DB, "/"+oldSlug, "/"+p.Slug, 301)
	}
	h.saved(q)
	q.back("/pages/"+strconv.FormatInt(p.ID, 10), "Saved.", "")
}

func (h *Handler) pageDelete(q *req) {
	if err := store.DeletePage(q.DB, q.id()); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/pages", "Page deleted.", "")
}

func (h *Handler) pageMove(q *req) {
	dir := 1
	if q.R.FormValue("dir") == "up" {
		dir = -1
	}
	if err := store.MovePage(q.DB, q.id(), dir); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/pages", "", "")
}
