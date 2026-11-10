// Package adminui holds the look shared by the main admin and the site admin:
// one CSS block, the icon set and a base layout. No JavaScript build step.
package adminui

import (
	_ "embed"
	"fmt"
	"html/template"
	"strings"
	"time"
)

//go:embed admin.css
var css string

//go:embed base.html
var baseLayout string

// Base is the shared layout template text. It defines "base" and expects "main".
func Base() string { return baseLayout }

var icons = map[string]string{
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"pencil":   `<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>`,
	"trash":    `<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6M14 11v6"/>`,
	"refresh":  `<path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/>`,
	"copy":     `<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`,
	"download": `<path d="M12 3v12"/><path d="M7 10l5 5 5-5"/><path d="M5 21h14"/>`,
	"logout":   `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/>`,
	"up":       `<path d="M12 19V5"/><path d="M5 12l7-7 7 7"/>`,
	"down":     `<path d="M12 5v14"/><path d="M19 12l-7 7-7-7"/>`,
	"save":     `<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8"/><path d="M7 3v5h8"/>`,
	"external": `<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6"/><path d="M10 14 21 3"/>`,
	"power":    `<path d="M18.4 6.6a9 9 0 1 1-12.8 0"/><path d="M12 2v10"/>`,
	"play":     `<path d="M6 4l14 8-14 8z"/>`,
	"key":      `<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/>`,
	"eye":      `<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>`,
	"upload":   `<path d="M12 21V9"/><path d="M7 14l5-5 5 5"/><path d="M5 3h14"/>`,
	"check":    `<path d="M20 6 9 17l-5-5"/>`,
	"x":        `<path d="M18 6 6 18M6 6l12 12"/>`,
	"file":     `<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/>`,
	"map":      `<path d="M3 6l6-3 6 3 6-3v15l-6 3-6-3-6 3z"/><path d="M9 3v15M15 6v15"/>`,
	"menu":     `<path d="M4 6h16M4 12h16M4 18h16"/>`,
	"globe":    `<circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20"/>`,
	"image":    `<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-5-5L5 21"/>`,
	"sliders":  `<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>`,
	"users":    `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8"/>`,
	"box":      `<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="M3 8l9 5 9-5M12 13v8"/>`,
	"search":   `<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`,
	"layers":   `<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>`,
	"home":     `<path d="M3 10.5 12 3l9 7.5V21H3z"/><path d="M9 21v-7h6v7"/>`,
}

// Icon returns an inline SVG icon.
func Icon(name string) template.HTML {
	p, ok := icons[name]
	if !ok {
		p = icons["file"]
	}
	return template.HTML(`<svg class="ic" viewBox="0 0 24 24" aria-hidden="true">` + p + `</svg>`)
}

// IconButton renders an icon-only button with a tooltip.
// tone: blue, green, amber, red, purple, teal, gray. attrs are extra raw attributes.
func IconButton(icon, tip, tone string, attrs ...string) template.HTML {
	t := template.HTMLEscapeString(tip)
	return template.HTML(fmt.Sprintf(`<button class="btn icon tone-%s" title="%s" aria-label="%s" %s>%s</button>`,
		template.HTMLEscapeString(tone), t, t, strings.Join(attrs, " "), Icon(icon)))
}

// IconLink renders an icon-only link styled as a button.
func IconLink(href, icon, tip, tone string, attrs ...string) template.HTML {
	t := template.HTMLEscapeString(tip)
	return template.HTML(fmt.Sprintf(`<a class="btn icon tone-%s" href="%s" title="%s" aria-label="%s" %s>%s</a>`,
		template.HTMLEscapeString(tone), template.HTMLEscapeString(href), t, t, strings.Join(attrs, " "), Icon(icon)))
}

// Funcs are the template helpers of both admin panels.
func Funcs() template.FuncMap {
	return template.FuncMap{
		"css":   func() template.CSS { return template.CSS(css) },
		"icon":  Icon,
		"ibtn":  func(icon, tip, tone string) template.HTML { return IconButton(icon, tip, tone, `type="submit"`) },
		"ilink": func(href, icon, tip, tone string) template.HTML { return IconLink(href, icon, tip, tone) },
		"ilinkNew": func(href, icon, tip, tone string) template.HTML {
			return IconLink(href, icon, tip, tone, `target="_blank" rel="noopener"`)
		},
		"time": func(unix int64) string {
			if unix == 0 {
				return ""
			}
			return time.Unix(unix, 0).Format("2006-01-02 15:04")
		},
		"has": func(list []string, s string) bool {
			for _, x := range list {
				if x == s {
					return true
				}
			}
			return false
		},
		"join":  strings.Join,
		"list":  func(s ...string) []string { return s },
		"bytes": humanBytes,
		"add":   func(a, b int) int { return a + b },
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// NavItem is one link of the admin top bar.
type NavItem struct {
	Label, URL, Icon string
	Active           bool
}

// Page is the data of every admin screen; D holds the screen's own data.
type Page struct {
	Title, Brand, ViewURL, LogoutURL, CSRF, User, Flash, Error string
	Notice                                                     string // small badge next to the brand
	A                                                          string // base URL of the admin ("/admin", "/de/admin" or "" for the main admin)
	Nav                                                        []NavItem
	D                                                          any
}

// Parse builds one template per screen file: base layout + that file.
// files maps a screen name to its template text.
func Parse(files map[string]string, extra template.FuncMap) (map[string]*template.Template, error) {
	f := Funcs()
	for k, v := range extra {
		f[k] = v
	}
	base, err := template.New("base").Funcs(f).Parse(baseLayout)
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for name, text := range files {
		c, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := c.New(name).Parse(text); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = c
	}
	return out, nil
}
