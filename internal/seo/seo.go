// Package seo builds the SEO head block, sitemap.xml and robots.txt for every host.
package seo

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"html/template"
	"strings"
	"time"

	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// AbsURL makes an absolute URL on the site's primary host; path is site-relative ("/about").
func AbsURL(scheme string, s *tenant.Site, path string) string {
	if path == "" {
		path = "/"
	}
	full := s.BasePath + path
	if s.BasePath != "" && path == "/" {
		full = s.BasePath
	}
	return scheme + "://" + s.Domain + full
}

// AbsMedia turns an image value into an absolute URL: full URL as is, "/x" on the
// primary domain, anything else as a file name in the site's uploads.
func AbsMedia(scheme string, s *tenant.Site, v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://"):
		return v
	case strings.HasPrefix(v, "/"):
		return scheme + "://" + s.Domain + v
	default:
		return scheme + "://" + s.Domain + "/_m/" + s.Key + "/" + v
	}
}

// Locale maps a language code to an og:locale value.
func Locale(lang string) string {
	lang = strings.ReplaceAll(lang, "-", "_")
	if strings.Contains(lang, "_") {
		return lang
	}
	m := map[string]string{"en": "en_US", "de": "de_DE", "fa": "fa_IR", "fr": "fr_FR", "ar": "ar_AR", "es": "es_ES",
		"it": "it_IT", "tr": "tr_TR", "ru": "ru_RU", "nl": "nl_NL", "pt": "pt_PT", "ps": "ps_AF", "ur": "ur_PK"}
	if v, ok := m[lang]; ok {
		return v
	}
	return lang
}

// HeadInput is what HeadBlock needs.
type HeadInput struct {
	Scheme   string
	Site     *tenant.Site
	Settings map[string]string
	Page     *store.Page // nil for 404 or module prefix pages
	Path     string      // site-relative request path (used when Page is nil)
	Data     any         // module data; JSON-LD is taken from Data["JSONLD"]
	Title    string      // used when Page is nil
}

// Alternate is one hreflang line.
type Alternate struct{ Lang, URL string }

// ParseAlternates reads "lang=URL" lines.
func ParseAlternates(s string) []Alternate {
	var out []Alternate
	for _, line := range strings.Split(s, "\n") {
		l, u, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(l) != "" && strings.TrimSpace(u) != "" {
			out = append(out, Alternate{strings.TrimSpace(l), strings.TrimSpace(u)})
		}
	}
	return out
}

// Title builds the <title> text: SEO title or page title, plus the suffix.
func Title(p *store.Page, fallback string, settings map[string]string) string {
	t := fallback
	if p != nil {
		t = p.Title
		if p.SEOTitle != "" {
			t = p.SEOTitle
		}
	}
	return t + settings["seo_title_suffix"]
}

// CanonicalURL is the page's canonical value or its own URL on the primary host.
func CanonicalURL(scheme string, s *tenant.Site, p *store.Page, path string) string {
	if p != nil {
		if p.Canonical != "" {
			if strings.HasPrefix(p.Canonical, "/") {
				return scheme + "://" + s.Domain + p.Canonical
			}
			return p.Canonical
		}
		path = "/" + p.Slug
		if p.Slug == "" {
			path = "/"
		}
	}
	return AbsURL(scheme, s, path)
}

func first(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// jsonLDScript writes a JSON-LD script tag; "</" is escaped so the text cannot close the tag.
func jsonLDScript(b *bytes.Buffer, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !json.Valid([]byte(raw)) {
		return
	}
	var c bytes.Buffer
	if json.Compact(&c, []byte(raw)) == nil {
		raw = c.String()
	}
	b.WriteString(`<script type="application/ld+json">`)
	b.WriteString(strings.ReplaceAll(raw, "</", `<\/`))
	b.WriteString("</script>\n")
}

// HeadBlock returns the whole SEO head block for {{seo}}.
func HeadBlock(in HeadInput) template.HTML {
	esc := template.HTMLEscapeString
	s, set, p := in.Site, in.Settings, in.Page
	var b bytes.Buffer
	title := Title(p, in.Title, set)
	desc, robots, ogTitle, ogDesc, ogImage := "", "index,follow", title, "", ""
	if p != nil {
		desc = p.MetaDescription
		robots = first(p.Robots, robots)
		ogTitle = first(p.OGTitle, p.SEOTitle, p.Title)
		ogDesc = first(p.OGDescription, p.MetaDescription)
		ogImage = p.OGImage
	}
	if p == nil && in.Title == "" {
		robots = "noindex,follow"
	}
	desc = first(desc, set["meta_description"])
	ogDesc = first(ogDesc, desc)
	ogImage = AbsMedia(in.Scheme, s, first(ogImage, set["default_og_image"]))
	canonical := CanonicalURL(in.Scheme, s, p, in.Path)
	siteName := first(set["site_title"], s.Name)

	b.WriteString("<title>" + esc(title) + "</title>\n")
	if desc != "" {
		b.WriteString(`<meta name="description" content="` + esc(desc) + "\">\n")
	}
	b.WriteString(`<meta name="robots" content="` + esc(robots) + "\">\n")
	if p != nil {
		b.WriteString(`<link rel="canonical" href="` + esc(canonical) + "\">\n")
		if alts := ParseAlternates(p.Alternates); len(alts) > 0 {
			b.WriteString(`<link rel="alternate" hreflang="` + esc(s.Lang) + `" href="` + esc(canonical) + "\">\n")
			for _, a := range alts {
				b.WriteString(`<link rel="alternate" hreflang="` + esc(a.Lang) + `" href="` + esc(a.URL) + "\">\n")
			}
		}
	}
	ogType := "website"
	if p != nil && p.Slug != "" {
		ogType = "article"
	}
	meta := func(attr, key, val string) {
		if val != "" {
			b.WriteString(`<meta ` + attr + `="` + key + `" content="` + esc(val) + "\">\n")
		}
	}
	meta("property", "og:title", ogTitle)
	meta("property", "og:description", ogDesc)
	meta("property", "og:url", canonical)
	meta("property", "og:type", ogType)
	meta("property", "og:site_name", siteName)
	meta("property", "og:locale", Locale(s.Lang))
	meta("property", "og:image", ogImage)
	meta("name", "twitter:card", "summary_large_image")
	meta("name", "twitter:title", ogTitle)
	meta("name", "twitter:description", ogDesc)
	meta("name", "twitter:image", ogImage)

	if p != nil {
		jsonLDScript(&b, p.JSONLD)
	}
	if org := OrganizationJSONLD(in.Scheme, s, set); org != "" {
		jsonLDScript(&b, org)
	}
	if m, ok := in.Data.(map[string]any); ok {
		switch v := m["JSONLD"].(type) {
		case string:
			jsonLDScript(&b, v)
		case nil:
		default:
			if j, err := json.Marshal(v); err == nil {
				jsonLDScript(&b, string(j))
			}
		}
	}
	if h := set["head_html"]; h != "" {
		b.WriteString(h) // admin-entered HTML is trusted
		b.WriteString("\n")
	}
	return template.HTML(b.String())
}

// OrganizationJSONLD builds the Organization block from the site settings
// org_name, org_logo, org_phone, org_social (one URL per line).
func OrganizationJSONLD(scheme string, s *tenant.Site, set map[string]string) string {
	if set["org_name"] == "" {
		return ""
	}
	o := map[string]any{"@context": "https://schema.org", "@type": "Organization", "name": set["org_name"],
		"url": AbsURL(scheme, s, "/")}
	if v := set["org_logo"]; v != "" {
		o["logo"] = AbsMedia(scheme, s, v)
	}
	if v := set["org_phone"]; v != "" {
		o["telephone"] = v
	}
	var same []string
	for _, l := range strings.Split(set["org_social"], "\n") {
		if l = strings.TrimSpace(l); l != "" {
			same = append(same, l)
		}
	}
	if len(same) > 0 {
		o["sameAs"] = same
	}
	j, _ := json.Marshal(o)
	return string(j)
}

// BodyEnd returns the site's custom end-of-body HTML.
func BodyEnd(set map[string]string) template.HTML { return template.HTML(set["body_end_html"]) }

// ---------- sitemap ----------

// URL is one sitemap entry with a site-relative path.
type URL struct {
	Path       string
	LastMod    int64
	Priority   string
	ChangeFreq string
}

type xmlURL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type xmlURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	NS      string   `xml:"xmlns,attr"`
	URLs    []xmlURL `xml:"url"`
}

// PageURLs turns published sitemap pages into entries.
func PageURLs(pages []*store.Page) []URL {
	out := make([]URL, 0, len(pages))
	for _, p := range pages {
		if p.Module == "redirect" || strings.Contains(p.Robots, "noindex") || (p.Canonical != "" && !strings.HasSuffix(p.Canonical, "/"+p.Slug)) {
			continue
		}
		path := "/" + p.Slug
		if p.Slug == "" {
			path = "/"
		}
		out = append(out, URL{Path: path, LastMod: p.UpdatedAt, Priority: p.SitemapPriority, ChangeFreq: p.SitemapChangefreq})
	}
	return out
}

// Sitemap writes sitemap.xml for a site.
func Sitemap(scheme string, s *tenant.Site, urls []URL) []byte {
	set := xmlURLSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, u := range urls {
		x := xmlURL{Loc: AbsURL(scheme, s, u.Path), Priority: u.Priority, ChangeFreq: u.ChangeFreq}
		if u.LastMod > 0 {
			x.LastMod = time.Unix(u.LastMod, 0).UTC().Format("2006-01-02")
		}
		set.URLs = append(set.URLs, x)
	}
	out, _ := xml.MarshalIndent(set, "", "  ")
	return append([]byte(xml.Header), out...)
}

// ---------- robots.txt ----------

// Robots returns robots.txt for a site. For a root site (no prefix) the sitemap
// lines of all prefixed sister sites on the same domain are added.
func Robots(scheme string, s *tenant.Site, set map[string]string, sisters []*tenant.Site) string {
	if s.Disabled() {
		return "User-agent: *\nDisallow: /\n"
	}
	text := strings.TrimSpace(set["robots_txt"])
	own := "Sitemap: " + AbsURL(scheme, s, "/sitemap.xml")
	if text == "" {
		text = "User-agent: *\nAllow: /\n\n" + own
	}
	if s.BasePath == "" {
		for _, o := range sisters {
			if o.BasePath == "" || o.ID == s.ID || o.Disabled() {
				continue
			}
			line := "Sitemap: " + AbsURL(scheme, o, "/sitemap.xml")
			if !strings.Contains(text, line) {
				text += "\n" + line
			}
		}
	}
	return text + "\n"
}

// DefaultRobots is served on a domain that has only prefixed sites.
func DefaultRobots(scheme string, sisters []*tenant.Site) string {
	text := "User-agent: *\nAllow: /\n"
	sep := "\n"
	for _, o := range sisters {
		if o.Disabled() {
			continue
		}
		text += sep + "Sitemap: " + AbsURL(scheme, o, "/sitemap.xml") + "\n"
		sep = ""
	}
	return text
}
