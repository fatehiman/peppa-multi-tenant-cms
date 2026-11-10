package web_test

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"hostkit/internal/auth"
	"hostkit/internal/config"
	"hostkit/internal/core"
	"hostkit/internal/db"
	"hostkit/internal/mainadmin"
	"hostkit/internal/module"
	"hostkit/internal/render"
	"hostkit/internal/siteadmin"
	"hostkit/internal/store"
	"hostkit/internal/tenant"
	"hostkit/internal/web"
	"hostkit/migrations"

	_ "hostkit/modules/contact"
	_ "hostkit/modules/list"
	_ "hostkit/modules/redirect"
	_ "hostkit/modules/search"
)

type env struct {
	t   *testing.T
	app *core.App
	srv *httptest.Server
}

func writeTheme(t *testing.T, dir, name, listbox string, admin string) {
	base := filepath.Join(dir, name)
	for _, d := range []string{"pages", "partials", "components", "assets/css"} {
		os.MkdirAll(filepath.Join(base, d), 0o755)
	}
	files := map[string]string{
		"theme.json":              `{"name":"` + name + `","menus":["main"],"admin":` + admin + `}`,
		"layout.html":             `<!doctype html><html lang="{{.Site.Lang}}"><head>{{seo}}<link href="{{asset "css/s.css"}}"></head><body><nav>{{range menu "main"}}<a href="{{.URL}}">{{.Label}}</a>{{end}}</nav>{{block "content" .}}{{end}}{{bodyEnd}}</body></html>`,
		"pages/default.html":      `{{define "content"}}<h1>{{.Page.Title}}</h1>{{.Page.Body}}{{template "c/listbox" (dict "Title" "FromDict")}}{{end}}`,
		"pages/contact.html":      `{{define "content"}}{{if .Data.Sent}}SENT{{end}}{{with .Data.Error}}ERR:{{.}}{{end}}<form method="post">{{csrf}}</form>{{end}}`,
		"components/listbox.html": `{{define "c/listbox"}}` + listbox + `{{end}}`,
		"404.html":                `{{define "content"}}NOTFOUND{{end}}`,
		"assets/css/s.css":        `body{}`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(base, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newEnv(t *testing.T) *env {
	auth.BcryptCost = 4
	root := t.TempDir()
	cfg := &config.Config{Root: root, PanelHost: "panel.test", Scheme: "https"}
	os.MkdirAll(cfg.ThemesDir(), 0o755)
	writeTheme(t, cfg.ThemesDir(), "alpha", `<div class="alpha-box">{{.Title}}</div>`, `["pages","menus","seo","settings","media","modules","users"]`)
	writeTheme(t, cfg.ThemesDir(), "beta", `<section class="beta-box">{{.Title}}</section>`, `["pages","seo"]`)
	mainDB, err := db.Open(cfg.MainDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(mainDB, migrations.FS, "main"); err != nil {
		t.Fatal(err)
	}
	reg, err := tenant.New(mainDB, cfg.SitesDir())
	if err != nil {
		t.Fatal(err)
	}
	reg.OnOpen = module.RunMigrations
	am, _ := auth.NewManager(mainDB, false, filepath.Join(root, "secret.key"))
	app := &core.App{Cfg: cfg, Reg: reg, Auth: am, Themes: render.NewSet(cfg.ThemesDir()), Cache: render.NewCache(8 << 20)}
	app.NewLimits()
	ma, err := mainadmin.New(app)
	if err != nil {
		t.Fatal(err)
	}
	sa, err := siteadmin.New(app)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("password1")
	mk := func(key, theme, lang, hosts string) {
		hl, err := tenant.ParseHosts(hosts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.CreateSite(tenant.SiteInput{Key: key, Name: strings.ToUpper(key), Theme: theme, Lang: lang, Hosts: hl,
			AdminEnabled: true, AdminEmail: "ed@" + key + ".test", AdminPasswordHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "alpha", "en", "a.test\nalias-a.test")
	mk("sub", "beta", "en", "sub.a.test")
	mk("ade", "alpha", "de", "a.test/de")
	mk("b", "beta", "en", "b.test")
	mk("conly", "alpha", "de", "c.test/de")
	e := &env{t: t, app: app, srv: httptest.NewServer(&web.Router{App: app, MainAdmin: ma, SiteAdmin: sa})}
	t.Cleanup(func() { e.srv.Close(); reg.CloseAll(); mainDB.Close() })
	return e
}

func (e *env) db(key string) *sql.DB {
	d, err := e.app.Reg.DB(e.app.Reg.ByKey(key))
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// do sends a request with a Host header and does not follow redirects.
func (e *env) do(c *http.Client, method, host, path string, form url.Values) (int, string, http.Header) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	req.Host = host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c == nil {
		c = client()
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) get(host, path string) (int, string, http.Header) {
	return e.do(nil, "GET", host, path, nil)
}

func TestPrefixHostsAndCanonical(t *testing.T) {
	e := newEnv(t)
	store.SavePage(e.db("ade"), &store.Page{Slug: "kontakt", Title: "Kontakt DE", Status: "published", InSitemap: true})
	store.SavePage(e.db("a"), &store.Page{Slug: "design", Title: "Design Root", Status: "published", InSitemap: true})

	if code, body, _ := e.get("a.test", "/de/kontakt"); code != 200 || !strings.Contains(body, "Kontakt DE") {
		t.Fatalf("/de/kontakt: %d", code)
	}
	if code, body, _ := e.get("a.test", "/design"); code != 200 || !strings.Contains(body, "Design Root") {
		t.Fatalf("/design must reach the root site: %d", code)
	}
	cases := []struct{ host, path, loc string }{
		{"www.a.test", "/design", "https://a.test/design"},
		{"alias-a.test", "/x?y=1", "https://a.test/x?y=1"},
		{"a.test", "/design/", "/design"},
		{"a.test", "/Design", "/design"},
		{"a.test", "/de/", "/de"},
	}
	for _, c := range cases {
		code, _, h := e.get(c.host, c.path)
		if code != 301 || h.Get("Location") != c.loc {
			t.Errorf("%s%s: got %d %q, want 301 %q", c.host, c.path, code, h.Get("Location"), c.loc)
		}
	}
	if code, _, _ := e.get("unknown.test", "/"); code != 404 {
		t.Errorf("unknown host: %d", code)
	}
	// domain with only a prefixed site
	if code, _, _ := e.get("c.test", "/"); code != 404 {
		t.Errorf("c.test/: %d", code)
	}
	if code, body, _ := e.get("c.test", "/robots.txt"); code != 200 || !strings.Contains(body, "Sitemap: https://c.test/de/sitemap.xml") {
		t.Errorf("c.test robots: %d %s", code, body)
	}
	// root robots lists the /de sitemap
	if _, body, _ := e.get("a.test", "/robots.txt"); !strings.Contains(body, "Sitemap: https://a.test/de/sitemap.xml") || !strings.Contains(body, "Sitemap: https://a.test/sitemap.xml") {
		t.Errorf("root robots: %s", body)
	}
	// URLs of the /de site start with /de
	d := e.db("ade")
	mid, _ := store.EnsureMenu(d, "main")
	p, _ := store.PageBySlug(d, "kontakt")
	store.SaveMenuItem(d, &store.MenuItem{MenuID: mid, Label: "K", PageID: p.ID})
	e.app.Cache.Invalidate("ade")
	_, body, _ := e.get("a.test", "/de/kontakt")
	if !strings.Contains(body, `href="/de/kontakt"`) || !strings.Contains(body, `rel="canonical" href="https://a.test/de/kontakt"`) {
		t.Errorf("menu or canonical without /de: %s", body)
	}
	if _, sm, _ := e.get("a.test", "/de/sitemap.xml"); !strings.Contains(sm, "<loc>https://a.test/de/kontakt</loc>") || !strings.Contains(sm, "<lastmod>") {
		t.Errorf("de sitemap: %s", sm)
	}
}

func TestSEOHeadAndSitemap(t *testing.T) {
	e := newEnv(t)
	d := e.db("a")
	store.SetSettings(d, map[string]string{"seo_title_suffix": " – A", "default_og_image": "share.jpg", "org_name": "Acme", "head_html": "<meta name=verify>", "body_end_html": "<script>x()</script>"})
	store.SavePage(d, &store.Page{Slug: "about", Title: "About", MetaDescription: "Desc here", Status: "published", InSitemap: true,
		JSONLD: `{"@type":"Thing","name":"</script>"}`, Alternates: "de=https://a.test/de/ueber"})
	store.SavePage(d, &store.Page{Slug: "draft", Title: "Draft", Status: "draft", InSitemap: true})
	store.SavePage(d, &store.Page{Slug: "hidden", Title: "Hidden", Status: "published", InSitemap: false})
	_, body, _ := e.get("a.test", "/about")
	for _, want := range []string{"<title>About – A</title>", `name="description" content="Desc here"`, `rel="canonical" href="https://a.test/about"`,
		`name="robots" content="index,follow"`, `property="og:title" content="About"`, `property="og:image" content="https://a.test/_m/a/share.jpg"`,
		`name="twitter:card" content="summary_large_image"`, `application/ld+json`, `<\/script>`, `"@type":"Organization"`,
		`hreflang="de" href="https://a.test/de/ueber"`, `hreflang="en"`, `og:locale" content="en_US"`, "<meta name=verify>", "<script>x()</script>"} {
		if !strings.Contains(body, want) {
			t.Errorf("head misses %q", want)
		}
	}
	_, sm, _ := e.get("a.test", "/sitemap.xml")
	if !strings.Contains(sm, "https://a.test/about") || strings.Contains(sm, "/draft") || strings.Contains(sm, "/hidden") {
		t.Errorf("sitemap: %s", sm)
	}
	if code, _, _ := e.get("a.test", "/draft"); code != 404 {
		t.Errorf("draft page must be 404 for visitors: %d", code)
	}
	store.SetSettings(d, map[string]string{"robots_txt": "User-agent: *\nDisallow: /x"})
	if _, r, _ := e.get("a.test", "/robots.txt"); !strings.HasPrefix(r, "User-agent: *\nDisallow: /x") {
		t.Errorf("custom robots: %s", r)
	}
}

func TestRedirectsCacheAndETag(t *testing.T) {
	e := newEnv(t)
	d := e.db("a")
	store.SavePage(d, &store.Page{Slug: "new", Title: "New", Status: "published"})
	store.SaveRedirect(d, "/old", "/new", 301)
	store.SaveRedirect(d, "/temp", "https://x.test/", 302)
	store.SaveRedirect(d, "/gone", "", 410)
	if code, _, h := e.get("a.test", "/old"); code != 301 || h.Get("Location") != "/new" {
		t.Errorf("301: %d %s", code, h.Get("Location"))
	}
	if code, _, _ := e.get("a.test", "/temp"); code != 302 {
		t.Errorf("302: %d", code)
	}
	if code, _, _ := e.get("a.test", "/gone"); code != 410 {
		t.Errorf("410: %d", code)
	}
	r, _ := store.FindRedirect(d, "/old")
	if r.Hits != 1 {
		t.Errorf("hits: %d", r.Hits)
	}
	// redirect on a prefixed site keeps the prefix
	store.SaveRedirect(e.db("ade"), "/alt", "/neu", 301)
	if _, _, h := e.get("a.test", "/de/alt"); h.Get("Location") != "/de/neu" {
		t.Errorf("prefixed redirect: %s", h.Get("Location"))
	}
	// cache + ETag + 304
	_, _, h := e.get("a.test", "/new")
	etag := h.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a cached page")
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/new", nil)
	req.Host = "a.test"
	req.Header.Set("If-None-Match", etag)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 304 {
		t.Errorf("If-None-Match: %d", res.StatusCode)
	}
}

func TestComponentsInTwoThemes(t *testing.T) {
	e := newEnv(t)
	body := `<p>[[listbox title="Short &amp; code"]] [[unknown x="1"]] [[listbox title='single']]</p>`
	store.SavePage(e.db("a"), &store.Page{Slug: "c", Title: "C", BodyHTML: body, Status: "published"})
	store.SavePage(e.db("b"), &store.Page{Slug: "c", Title: "C", BodyHTML: body, Status: "published"})
	_, ba, _ := e.get("a.test", "/c")
	_, bb, _ := e.get("b.test", "/c")
	if !strings.Contains(ba, `<div class="alpha-box">Short &amp; code</div>`) || !strings.Contains(ba, `<div class="alpha-box">FromDict</div>`) ||
		!strings.Contains(ba, `[[unknown x="1"]]`) || !strings.Contains(ba, `alpha-box">single`) {
		t.Errorf("alpha: %s", ba)
	}
	if !strings.Contains(bb, `<section class="beta-box">Short &amp; code</section>`) || strings.Contains(bb, "alpha-box") {
		t.Errorf("beta: %s", bb)
	}
}

func login(e *env, host, prefix, email string) (*http.Client, string) {
	c := client()
	code, _, h := e.do(c, "POST", host, prefix+"/mainadmin/login", url.Values{"email": {email}, "password": {"password1"}})
	if code != 303 || !strings.HasSuffix(h.Get("Location"), "/mainadmin/pages") {
		e.t.Fatalf("login %s%s: %d %s", host, prefix, code, h.Get("Location"))
	}
	_, page, _ := e.do(c, "GET", host, prefix+"/mainadmin/pages/new", nil)
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		e.t.Fatal("no csrf")
	}
	return c, m[1]
}

func TestSessionsAreScopedPerSite(t *testing.T) {
	e := newEnv(t)
	c, _ := login(e, "a.test", "", "ed@a.test")
	if code, _, _ := e.do(c, "GET", "a.test", "/mainadmin/pages", nil); code != 200 {
		t.Fatalf("own admin: %d", code)
	}
	// same browser on /de: must not be logged in
	if code, _, _ := e.do(c, "GET", "a.test", "/de/mainadmin/pages", nil); code != 303 {
		t.Errorf("/de/mainadmin with root session: %d", code)
	}
	// the cookie value of site A replayed for site B
	u, _ := url.Parse(e.srv.URL)
	var tok string
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == auth.CookieName("a") {
			tok = ck.Value
		}
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/mainadmin/pages", nil)
	req.Host = "b.test"
	req.AddCookie(&http.Cookie{Name: auth.CookieName("b"), Value: tok})
	res, _ := client().Do(req)
	res.Body.Close()
	if res.StatusCode != 303 {
		t.Errorf("site A token accepted on site B: %d", res.StatusCode)
	}
	// wrong user of another site cannot log in here
	if code, _, _ := e.do(client(), "POST", "b.test", "/mainadmin/login", url.Values{"email": {"ed@a.test"}, "password": {"password1"}}); code == 303 {
		t.Error("user of site a logged in on site b")
	}
}

func TestAdminSaveCSRFAndSlugRedirect(t *testing.T) {
	e := newEnv(t)
	c, csrf := login(e, "a.test", "", "ed@a.test")
	form := url.Values{"title": {"Page"}, "slug": {"first"}, "status": {"published"}, "in_sitemap": {"1"}}
	if code, _, _ := e.do(c, "POST", "a.test", "/mainadmin/pages/save", form); code != 403 {
		t.Errorf("save without csrf: %d", code)
	}
	form.Set("_csrf", csrf)
	if code, _, _ := e.do(c, "POST", "a.test", "/mainadmin/pages/save", form); code != 303 {
		t.Fatalf("save: %d", code)
	}
	p, err := store.PageBySlug(e.db("a"), "first")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get("a.test", "/first"); code != 200 {
		t.Fatalf("new page: %d", code)
	}
	form.Set("id", strconv.FormatInt(p.ID, 10))
	form.Set("slug", "second")
	form.Set("add_redirect", "1")
	e.do(c, "POST", "a.test", "/mainadmin/pages/save", form)
	if code, _, h := e.get("a.test", "/first"); code != 301 || h.Get("Location") != "/second" {
		t.Errorf("slug change redirect: %d %s", code, h.Get("Location"))
	}
	form.Set("json_ld", "{bad")
	if _, body, _ := e.do(c, "POST", "a.test", "/mainadmin/pages/save", form); !strings.Contains(body, "not valid JSON") {
		t.Error("invalid JSON-LD accepted")
	}
}

func TestSectionsFromTheme(t *testing.T) {
	e := newEnv(t)
	c, _ := login(e, "b.test", "", "ed@b.test")
	_, page, _ := e.do(c, "GET", "b.test", "/mainadmin/pages", nil)
	if strings.Contains(page, "/mainadmin/menus") || strings.Contains(page, "/mainadmin/media") || !strings.Contains(page, "/mainadmin/seo") {
		t.Error("beta (pages, seo) shows other sections")
	}
	for _, p := range []string{"/mainadmin/menus", "/mainadmin/media", "/mainadmin/settings", "/mainadmin/users"} {
		if code, _, _ := e.do(c, "GET", "b.test", p, nil); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	if code, _, _ := e.do(c, "GET", "b.test", "/mainadmin/seo", nil); code != 200 {
		t.Errorf("seo: %d", code)
	}
}

func TestContactModuleAndDataIsolation(t *testing.T) {
	e := newEnv(t)
	store.SavePage(e.db("a"), &store.Page{Slug: "contact", Title: "Contact", Template: "contact", Module: "contact", Status: "published"})
	store.SavePage(e.db("b"), &store.Page{Slug: "contact", Title: "Contact", Template: "contact", Module: "contact", Status: "published"})
	store.SetSettings(e.db("a"), map[string]string{"contact_captcha": "0"})
	store.SetSettings(e.db("b"), map[string]string{"contact_captcha": "0"})
	_, page, _ := e.get("a.test", "/contact")
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no public csrf")
	}
	form := url.Values{"_csrf": {m[1]}, "name": {"N"}, "email": {"n@x.test"}, "message": {"Hello A"}}
	if code, _, h := e.do(nil, "POST", "a.test", "/contact", form); code != 303 || !strings.Contains(h.Get("Location"), "sent=1") {
		t.Fatalf("post: %d %s", code, h.Get("Location"))
	}
	// token of site a is not valid on site b
	if _, body, _ := e.do(nil, "POST", "b.test", "/contact", form); !strings.Contains(body, "ERR:") {
		t.Error("site a token accepted on site b")
	}
	// honeypot: no record
	form.Set("website", "spam")
	e.do(nil, "POST", "a.test", "/contact", form)
	ra, _ := store.ModList(e.db("a"), "contact", "", 10, 0)
	rb, _ := store.ModList(e.db("b"), "contact", "", 10, 0)
	if len(ra) != 1 || len(rb) != 0 {
		t.Errorf("records a=%d b=%d", len(ra), len(rb))
	}
}

func TestMainAdminCreatesSiteLive(t *testing.T) {
	e := newEnv(t)
	hash, _ := auth.HashPassword("password1")
	e.app.Reg.Main.Exec("INSERT INTO main_admins (email, password_hash) VALUES ('me@x.test', ?)", hash)
	c := client()
	if code, _, _ := e.do(c, "POST", "panel.test", "/login", url.Values{"email": {"me@x.test"}, "password": {"password1"}}); code != 303 {
		t.Fatal("main login failed")
	}
	_, page, _ := e.do(c, "GET", "panel.test", "/sites/new", nil)
	csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)[1]
	form := url.Values{"_csrf": {csrf}, "name": {"New"}, "key": {"newsite"}, "hosts": {"new.test"}, "lang": {"en"}, "theme": {"alpha"}, "admin_enabled": {"1"}}
	if code, _, _ := e.do(c, "POST", "panel.test", "/sites/save", form); code != 303 {
		t.Fatalf("create site: %d", code)
	}
	if code, _, _ := e.get("new.test", "/"); code != 200 {
		t.Errorf("new site not live: %d", code)
	}
	// host already used
	form.Set("key", "other")
	if _, body, _ := e.do(c, "POST", "panel.test", "/sites/save", form); !strings.Contains(body, "already used") {
		t.Error("duplicate host accepted")
	}
	// disable → 503
	s := e.app.Reg.ByKey("newsite")
	e.do(c, "POST", "panel.test", "/sites/"+strconv.FormatInt(s.ID, 10)+"/status", url.Values{"_csrf": {csrf}, "status": {"disabled"}})
	if code, _, h := e.get("new.test", "/"); code != 503 || h.Get("Retry-After") == "" {
		t.Errorf("disabled: %d", code)
	}
	if _, body, _ := e.get("new.test", "/robots.txt"); !strings.Contains(body, "Disallow: /") {
		t.Error("disabled robots")
	}
}

func TestUnderConstruction(t *testing.T) {
	e := newEnv(t)
	d := e.db("a")
	store.SavePage(d, &store.Page{Slug: "about", Title: "About Real", Status: "published", InSitemap: true})
	// visitor fills the page cache before UC is switched on
	if _, body, _ := e.get("a.test", "/about"); !strings.Contains(body, "About Real") {
		t.Fatal("page not served")
	}
	store.SetSettings(d, map[string]string{"under_construction": "1", "uc_message": "Coming soon!"})
	e.app.Cache.Invalidate("a")
	code, body, h := e.get("a.test", "/about")
	if code != 503 || strings.Contains(body, "About Real") || !strings.Contains(body, "Coming soon!") || h.Get("Retry-After") == "" {
		t.Errorf("visitor: %d %q", code, body)
	}
	if _, r, _ := e.get("a.test", "/robots.txt"); !strings.Contains(r, "Disallow: /") {
		t.Errorf("robots under construction: %s", r)
	}
	if code, _, _ := e.get("a.test", "/sitemap.xml"); code != 503 {
		t.Errorf("sitemap: %d", code)
	}
	// the admin of this site sees the real site, never stored by shared caches
	c, _ := login(e, "a.test", "", "ed@a.test")
	code, body, h = e.do(c, "GET", "a.test", "/about", nil)
	if code != 200 || !strings.Contains(body, "About Real") || h.Get("Cache-Control") != "private, no-store" {
		t.Errorf("admin: %d cache=%q", code, h.Get("Cache-Control"))
	}
	// after the admin's visit, a visitor still gets the UC page (no cache leak)
	if code, body, _ := e.get("a.test", "/about"); code != 503 || strings.Contains(body, "About Real") {
		t.Errorf("visitor after admin: %d", code)
	}
	// an admin of another site (same domain, /de) does not see it
	cde, _ := login(e, "a.test", "/de", "ed@ade.test")
	if code, _, _ := e.do(cde, "GET", "a.test", "/about", nil); code != 503 {
		t.Errorf("admin of /de sees the root site: %d", code)
	}
	// other sites are not affected
	if code, _, _ := e.get("a.test", "/de"); code != 200 {
		t.Errorf("/de site: %d", code)
	}
	// the admin panel itself still works for visitors (login page)
	if code, _, _ := e.get("a.test", "/mainadmin/login"); code != 200 {
		t.Errorf("admin login page: %d", code)
	}
	// switch off from the admin SEO form
	_, page, _ := e.do(c, "GET", "a.test", "/mainadmin/seo", nil)
	if !strings.Contains(page, "Under construction") {
		t.Error("no under construction notice in the admin")
	}
	csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)[1]
	e.do(c, "POST", "a.test", "/mainadmin/seo", url.Values{"_csrf": {csrf}})
	if code, _, _ := e.get("a.test", "/about"); code != 200 {
		t.Errorf("after switch off: %d", code)
	}
}

func TestSubdomainAndWWW(t *testing.T) {
	e := newEnv(t)
	store.SavePage(e.db("sub"), &store.Page{Slug: "x", Title: "Sub X", Status: "published", InSitemap: true})
	if code, body, _ := e.get("sub.a.test", "/x"); code != 200 || !strings.Contains(body, "Sub X") {
		t.Errorf("subdomain page: %d", code)
	}
	if code, _, _ := e.get("a.test", "/x"); code != 404 {
		t.Errorf("subdomain page visible on the parent domain: %d", code)
	}
	// www of the subdomain goes to the subdomain, www of the domain to the domain
	if code, _, h := e.get("www.sub.a.test", "/x?y=1"); code != 301 || h.Get("Location") != "https://sub.a.test/x?y=1" {
		t.Errorf("www.sub: %d %s", code, h.Get("Location"))
	}
	if code, _, h := e.get("www.a.test", "/"); code != 301 || h.Get("Location") != "https://a.test/" {
		t.Errorf("www: %d %s", code, h.Get("Location"))
	}
	// own robots.txt and sitemap: no lines of the parent domain's sites
	if _, r, _ := e.get("sub.a.test", "/robots.txt"); !strings.Contains(r, "https://sub.a.test/sitemap.xml") || strings.Contains(r, "https://a.test/") {
		t.Errorf("sub robots: %s", r)
	}
	// session of the parent domain is not valid on the subdomain
	c, _ := login(e, "a.test", "", "ed@a.test")
	if code, _, _ := e.do(c, "GET", "sub.a.test", "/mainadmin/pages", nil); code != 303 {
		t.Errorf("parent session accepted on subdomain: %d", code)
	}
	// a www host can not be added
	if _, err := tenant.ParseHosts("www.new.test"); err == nil {
		t.Error("www host accepted")
	}
}

func TestContactCaptchaAndOpenRedirect(t *testing.T) {
	e := newEnv(t)
	store.SavePage(e.db("a"), &store.Page{Slug: "contact", Title: "Contact", Template: "contact", Module: "contact", Status: "published"})
	_, page, _ := e.get("a.test", "/contact")
	csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)[1]
	code, svg, h := e.get("a.test", "/_captcha?k=testid12345")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "image/svg+xml") || strings.Contains(h.Get("Cache-Control"), "public") || !strings.Contains(svg, "<svg") {
		t.Fatalf("captcha image: %d %q", code, h.Get("Content-Type"))
	}
	answer := e.app.Captcha.Code("site:a", "testid12345")
	form := url.Values{"_csrf": {csrf}, "name": {"N"}, "email": {"n@x.test"}, "message": {"Hi"}}
	if _, body, _ := e.do(nil, "POST", "a.test", "/contact", form); !strings.Contains(body, "ERR:") {
		t.Fatal("contact without captcha accepted")
	}
	form.Set("captcha_id", "testid12345")
	form.Set("captcha", "9"+answer[1:]) // wrong first digit
	if answer[0] == '9' {
		form.Set("captcha", "8"+answer[1:])
	}
	if _, body, _ := e.do(nil, "POST", "a.test", "/contact", form); !strings.Contains(body, "ERR:") {
		t.Fatal("wrong captcha accepted")
	}
	// a wrong answer uses up the picture: the right answer now fails too
	form.Set("captcha", answer)
	if _, body, _ := e.do(nil, "POST", "a.test", "/contact", form); !strings.Contains(body, "ERR:") {
		t.Fatal("used captcha accepted")
	}
	e.get("a.test", "/_captcha?k=testid67890")
	form.Set("captcha_id", "testid67890")
	form.Set("captcha", e.app.Captcha.Code("site:a", "testid67890"))
	if code, _, h := e.do(nil, "POST", "a.test", "/contact", form); code != 303 || !strings.Contains(h.Get("Location"), "sent=1") {
		t.Fatalf("good captcha: %d", code)
	}
	if code, _, _ := e.get("a.test", "/_captcha?k=bad"); code != 400 {
		t.Errorf("bad captcha id: %d", code)
	}
	// open redirect: a tab (browsers delete it) or a backslash must not lead to another host
	for _, ret := range []string{"/\t/evil.com", "/\\evil.com", "//evil.com", "/%09/evil.com", "/ /evil.com"} {
		_, _, h := e.do(nil, "POST", "a.test", "/contact", url.Values{"website": {"bot"}, "_return": {ret}})
		loc := h.Get("Location")
		if strings.ContainsAny(loc, "\t\\ ") || strings.HasPrefix(loc, "//") || !strings.HasPrefix(loc, "/") {
			t.Errorf("_return %q gave Location %q", ret, loc)
		}
	}
}

func TestOldAdminPathIsGone(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.get("a.test", "/admin/login"); code != 404 {
		t.Errorf("/admin/login: %d", code)
	}
	if code, _, _ := e.get("a.test", "/mainadmin/login"); code != 200 {
		t.Errorf("/mainadmin/login: %d", code)
	}
}

func TestLoginCaptchaAndUserLimit(t *testing.T) {
	e := newEnv(t)
	c := client()
	login := func(pw string, extra url.Values) (int, string) {
		f := url.Values{"email": {"ed@a.test"}, "password": {pw}}
		for k, v := range extra {
			f[k] = v
		}
		code, body, _ := e.do(c, "POST", "a.test", "/mainadmin/login", f)
		return code, body
	}
	if _, page, _ := e.do(c, "GET", "a.test", "/mainadmin/login", nil); strings.Contains(page, `name="captcha_id"`) {
		t.Fatal("captcha shown before any failure")
	}
	login("wrong-pass", nil)
	login("wrong-pass", nil)
	if _, page, _ := e.do(c, "GET", "a.test", "/mainadmin/login", nil); !strings.Contains(page, `name="captcha_id"`) {
		t.Fatal("no captcha after 2 failures")
	}
	if code, body := login("password1", nil); code == 303 || !strings.Contains(body, "numbers in the picture") {
		t.Fatalf("login without captcha after failures: %d", code)
	}
	e.do(c, "GET", "a.test", "/_captcha?k=loginid12345", nil)
	if code, _ := login("password1", url.Values{"captcha_id": {"loginid12345"}, "captcha": {e.app.Captcha.Code("site:a", "loginid12345")}}); code != 303 {
		t.Fatalf("login with captcha: %d", code)
	}
	// user name limit: failures from many IPs block that user name
	for i := 0; i < 10; i++ {
		e.app.LoginFailed("site:a", "10.0.0."+strconv.Itoa(i), "ed@a.test")
	}
	r := httptest.NewRequest("POST", "/", nil)
	if msg := e.app.LoginGate(r, "site:a", "10.9.9.9", "ED@a.test"); !strings.Contains(msg, "Too many") {
		t.Errorf("user limit not applied: %q", msg)
	}
	if msg := e.app.LoginGate(r, "site:a", "10.9.9.9", "other@a.test"); msg != "" {
		t.Errorf("other user blocked: %q", msg)
	}
}
