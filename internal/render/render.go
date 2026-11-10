package render

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"hostkit/internal/seo"
	"hostkit/internal/store"
	"hostkit/internal/tenant"
)

// RC is the request context the helpers read. One RC per rendered page.
type RC struct {
	Site     *tenant.Site
	DB       *sql.DB
	Theme    *Theme
	Settings map[string]string
	Scheme   string
	Path     string // site-relative request path ("/about")
	Query    url.Values
	Page     *store.Page // nil on 404 or prefix routes
	Title    string      // title when Page is nil
	Data     any         // module data
	CSRF     func() string
}

// SiteView is .Site in templates.
type SiteView struct {
	Name     string
	Key      string
	Domain   string
	BasePath string
	Lang     string
	Dir      string // "rtl" for right-to-left languages, else "ltr"
	URL      string // absolute home URL
	Settings map[string]string
}

// RequestView is .Request in templates.
type RequestView struct {
	Path  string
	Query url.Values
}

// Data is the root value every template receives.
type Data struct {
	Site    SiteView
	Page    *store.PageView
	Request RequestView
	Data    any
}

var rtlLangs = map[string]bool{"fa": true, "ar": true, "he": true, "ur": true, "ps": true, "ku": true, "dv": true, "yi": true}

// Dir returns "rtl" or "ltr" for a language code.
func Dir(lang string) string {
	l, _, _ := strings.Cut(strings.ToLower(lang), "-")
	l, _, _ = strings.Cut(l, "_")
	if rtlLangs[l] {
		return "rtl"
	}
	return "ltr"
}

func (rc *RC) data(body template.HTML) Data {
	d := Data{
		Site: SiteView{Name: rc.Site.Name, Key: rc.Site.Key, Domain: rc.Site.Domain, BasePath: rc.Site.BasePath,
			Lang: rc.Site.Lang, Dir: Dir(rc.Site.Lang), URL: seo.AbsURL(rc.Scheme, rc.Site, "/"), Settings: rc.Settings},
		Request: RequestView{Path: rc.Path, Query: rc.Query},
		Data:    rc.Data,
	}
	if rc.Page != nil {
		d.Page = rc.Page.View(rc.Site.BasePath)
		d.Page.Body = body
	} else {
		d.Page = &store.PageView{Title: rc.Title, URL: rc.Site.BasePath + rc.Path}
	}
	return d
}

// RenderPage renders rc.Page with its template (or pages/default.html).
func (t *Theme) RenderPage(w io.Writer, rc *RC) error {
	ts, err := t.get()
	if err != nil {
		return err
	}
	defer t.put(ts)
	ts.h.rc = rc
	name := "default"
	if rc.Page != nil && rc.Page.Template != "" {
		name = rc.Page.Template
	}
	tpl := ts.pages[name]
	if tpl == nil {
		tpl = ts.pages["default"]
	}
	var body template.HTML
	if rc.Page != nil {
		body = expandShortcodes(tpl, rc, rc.Page.BodyHTML)
	}
	return tpl.ExecuteTemplate(w, "layout.html", rc.data(body))
}

// Render404 renders the theme's 404.html.
func (t *Theme) Render404(w io.Writer, rc *RC) error {
	ts, err := t.get()
	if err != nil {
		return err
	}
	defer t.put(ts)
	ts.h.rc = rc
	return ts.notFound.ExecuteTemplate(w, "layout.html", rc.data(""))
}

// RenderUC renders the theme's under-construction.html (a full page, no layout).
func (t *Theme) RenderUC(w io.Writer, rc *RC) error {
	ts, err := t.get()
	if err != nil {
		return err
	}
	defer t.put(ts)
	if ts.uc == nil {
		return fmt.Errorf("theme %s has no under-construction.html", t.Name)
	}
	ts.h.rc = rc
	return ts.uc.ExecuteTemplate(w, "under-construction.html", rc.data(""))
}

// ---------- helpers ----------

// holder is the per-tset pointer the helper closures read; set before Execute.
type holder struct{ rc *RC }

// MenuLink is one item returned by the menu helper.
type MenuLink struct {
	Label    string
	URL      string
	Active   bool
	Children []MenuLink
}

func (h *holder) funcMap() template.FuncMap {
	return template.FuncMap{
		"menu":     h.menu,
		"setting":  func(k string) string { return h.rc.Settings[k] },
		"asset":    func(p string) string { return h.rc.Theme.AssetURL(p) },
		"media":    h.media,
		"img":      h.img,
		"page":     h.page,
		"pages":    h.pages,
		"date":     formatDate,
		"jdate":    JalaliDate,
		"fadigits": FaDigits,
		"excerpt":  Excerpt,
		"t":        h.t,
		"csrf":     h.csrf,
		"dict":     dict,
		"seo":      h.seo,
		"bodyEnd":  func() template.HTML { return seo.BodyEnd(h.rc.Settings) },
		"url":      func(p string) string { return h.rc.Site.BasePath + p },
		"absurl":   func(p string) string { return h.rc.Scheme + "://" + h.rc.Site.Domain + p },
		"now":      time.Now,
		"add":      func(a, b int) int { return a + b },
		"lines":    lines,
	}
}

// HelperNames lists the helper functions (for docs and theme check).
func HelperNames() []string {
	m := (&holder{}).funcMap()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (h *holder) linkURL(it *store.MenuItem) string {
	rc := h.rc
	base := rc.Site.BasePath
	switch {
	case it.PageID != 0:
		return store.PagePath(base, it.PageSlug)
	case strings.HasPrefix(it.URL, "#"):
		if rc.Path == "/" {
			return it.URL
		}
		return store.PagePath(base, "") + it.URL
	case strings.HasPrefix(it.URL, "/"):
		return base + it.URL
	default:
		return it.URL
	}
}

func (h *holder) menu(key string) []MenuLink {
	rc := h.rc
	items, err := store.MenuTree(rc.DB, key)
	if err != nil {
		return nil
	}
	cur := store.PagePath(rc.Site.BasePath, strings.TrimPrefix(rc.Path, "/"))
	var conv func(list []*store.MenuItem) []MenuLink
	conv = func(list []*store.MenuItem) []MenuLink {
		out := make([]MenuLink, 0, len(list))
		for _, it := range list {
			l := MenuLink{Label: it.Label, URL: h.linkURL(it), Children: conv(it.Children)}
			l.Active = l.URL == cur
			for _, c := range l.Children {
				if c.Active {
					l.Active = true
				}
			}
			out = append(out, l)
		}
		return out
	}
	return conv(items)
}

// MediaURL returns the public URL of an upload (full URLs and absolute paths stay as they are).
func MediaURL(siteKey, v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "http://"), strings.HasPrefix(v, "https://"), strings.HasPrefix(v, "/"):
		return v
	default:
		return "/_m/" + siteKey + "/" + v
	}
}

func (h *holder) media(v string) string { return MediaURL(h.rc.Site.Key, v) }

// img prints an <img> tag for an upload with its alt text; extra args: class.
func (h *holder) img(v string, class ...string) template.HTML {
	if v == "" {
		return ""
	}
	alt := ""
	if m := store.MediaByName(h.rc.DB, v); m != nil {
		alt = m.Alt
	}
	c := ""
	if len(class) > 0 {
		c = ` class="` + template.HTMLEscapeString(class[0]) + `"`
	}
	return template.HTML(`<img src="` + template.HTMLEscapeString(h.media(v)) + `" alt="` +
		template.HTMLEscapeString(alt) + `"` + c + ` loading="lazy">`)
}

func (h *holder) page(slug string) *store.PageView {
	p, err := store.PageBySlug(h.rc.DB, strings.Trim(slug, "/"))
	if err != nil || p.Status != "published" {
		return nil
	}
	return p.View(h.rc.Site.BasePath)
}

func (h *holder) pages(prefix string, n int) []*store.PageView {
	list, err := store.PagesByPrefix(h.rc.DB, strings.TrimPrefix(prefix, "/"), n, 0)
	if err != nil {
		return nil
	}
	out := make([]*store.PageView, len(list))
	for i, p := range list {
		out[i] = p.View(h.rc.Site.BasePath)
	}
	return out
}

func (h *holder) t(key string) string {
	if v := h.rc.Settings["t_"+key]; v != "" {
		return v
	}
	if v, ok := h.rc.Theme.Meta.Strings[key]; ok {
		return v
	}
	return key
}

func (h *holder) csrf() template.HTML {
	if h.rc.CSRF == nil {
		return ""
	}
	return template.HTML(`<input type="hidden" name="_csrf" value="` + template.HTMLEscapeString(h.rc.CSRF()) + `">`)
}

func (h *holder) seo() template.HTML {
	rc := h.rc
	return seo.HeadBlock(seo.HeadInput{Scheme: rc.Scheme, Site: rc.Site, Settings: rc.Settings, Page: rc.Page,
		Path: rc.Path, Data: rc.Data, Title: rc.Title})
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict needs key/value pairs")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func formatDate(v any, layout string) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format(layout)
	case int64:
		return time.Unix(t, 0).Format(layout)
	}
	return ""
}

// Excerpt returns plain text (tags and [[shortcodes]] removed) cut to n characters.
func Excerpt(v any, n int) string {
	var s string
	switch x := v.(type) {
	case template.HTML:
		s = string(x)
	case string:
		s = x
	default:
		s = fmt.Sprint(x)
	}
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<':
			depth++
		case c == '>' && depth > 0:
			depth--
			b.WriteByte(' ')
		case c == '[' && i+1 < len(s) && s[i+1] == '[':
			if end := strings.Index(s[i:], "]]"); end > 0 {
				i += end + 1
				continue
			}
			b.WriteByte(c)
		case depth == 0:
			b.WriteByte(c)
		}
	}
	text := strings.Join(strings.Fields(htmlUnescape(b.String())), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)[:n]
	cut := string(r)
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&zwnj;", "‌").Replace(s)
}

// FaDigits replaces Latin digits with Persian digits.
func FaDigits(v any) string {
	s := fmt.Sprint(v)
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			r = '۰' + (r - '0')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var jMonths = []string{"فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور", "مهر", "آبان", "آذر", "دی", "بهمن", "اسفند"}

// JalaliDate formats a time in the Persian (Jalali) calendar.
// style "long" gives "19 آبان 1405", "year" gives "1405", anything else "1405/08/19".
func JalaliDate(v any, style string) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case int64:
		t = time.Unix(x, 0)
	default:
		return ""
	}
	jy, jm, jd := ToJalali(t.Year(), int(t.Month()), t.Day())
	if style == "year" {
		return fmt.Sprintf("%d", jy)
	}
	if style == "long" {
		return fmt.Sprintf("%d %s %d", jd, jMonths[jm-1], jy)
	}
	return fmt.Sprintf("%04d/%02d/%02d", jy, jm, jd)
}

// ToJalali converts a Gregorian date to Jalali (algorithm by Roozbeh Pournader / jdf).
func ToJalali(gy, gm, gd int) (int, int, int) {
	gdm := []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2 = gy + 1
	}
	days := 355666 + (365 * gy) + ((gy2 + 3) / 4) - ((gy2 + 99) / 100) + ((gy2 + 399) / 400) + gd + gdm[gm-1]
	jy := -1595 + (33 * (days / 12053))
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	var jm, jd int
	if days < 186 {
		jm = 1 + days/31
		jd = 1 + days%31
	} else {
		jm = 7 + (days-186)/30
		jd = 1 + (days-186)%30
	}
	return jy, jm, jd
}

// ---------- shortcodes ----------

// Shortcode is one parsed [[name attr="value"]].
type Shortcode struct {
	Start, End int // byte range in the source
	Name       string
	Attrs      map[string]string
}

// ParseShortcodes finds every [[name key="value" key2='v' key3=v]] in s.
// A small hand-written scanner: no regular expressions, no backtracking.
func ParseShortcodes(s string) []Shortcode {
	var out []Shortcode
	i := 0
	for {
		j := strings.Index(s[i:], "[[")
		if j < 0 {
			return out
		}
		start := i + j
		p := start + 2
		k := p
		for k < len(s) && isNameChar(s[k]) {
			k++
		}
		if k == p {
			i = start + 2
			continue
		}
		sc := Shortcode{Start: start, Name: s[p:k], Attrs: map[string]string{}}
		p = k
		ok := false
		for p < len(s) {
			for p < len(s) && (s[p] == ' ' || s[p] == '\t' || s[p] == '\n' || s[p] == '\r') {
				p++
			}
			if strings.HasPrefix(s[p:], "]]") {
				ok = true
				p += 2
				break
			}
			ks := p
			for p < len(s) && isNameChar(s[p]) {
				p++
			}
			if p == ks {
				break // bad character: not a shortcode
			}
			key := s[ks:p]
			if p < len(s) && s[p] == '=' {
				p++
				var val string
				if p < len(s) && (s[p] == '"' || s[p] == '\'') {
					q := s[p]
					e := strings.IndexByte(s[p+1:], q)
					if e < 0 {
						break
					}
					val = s[p+1 : p+1+e]
					p += e + 2
				} else {
					vs := p
					for p < len(s) && s[p] != ' ' && s[p] != ']' && s[p] != '\n' {
						p++
					}
					val = s[vs:p]
				}
				sc.Attrs[key] = htmlUnescape(val)
			} else {
				sc.Attrs[key] = "true"
			}
		}
		if !ok {
			i = start + 2
			continue
		}
		sc.End = p
		out = append(out, sc)
		i = p
	}
}

func isNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// expandShortcodes replaces known components in a page body with their output.
func expandShortcodes(tpl *template.Template, rc *RC, body string) template.HTML {
	codes := ParseShortcodes(body)
	if len(codes) == 0 {
		return template.HTML(body)
	}
	var b strings.Builder
	last := 0
	root := rc.data("")
	for _, sc := range codes {
		b.WriteString(body[last:sc.Start])
		last = sc.End
		if !rc.Theme.HasComponent(sc.Name) {
			b.WriteString(body[sc.Start:sc.End]) // unknown: leave the text
			continue
		}
		args := map[string]any{}
		for k, v := range sc.Attrs {
			args[k] = v
			args[strings.ToUpper(k[:1])+k[1:]] = v // title= is also .Title
		}
		args["Site"] = root.Site
		args["Page"] = root.Page
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "c/"+sc.Name, args); err != nil {
			b.WriteString("<!-- component " + template.HTMLEscapeString(sc.Name) + ": " + template.HTMLEscapeString(err.Error()) + " -->")
			continue
		}
		b.Write(out.Bytes())
	}
	b.WriteString(body[last:])
	return template.HTML(b.String())
}
