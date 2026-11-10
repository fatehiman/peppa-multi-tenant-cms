# hostkit — Build Plan (one phase)

Source: [project-description.md](project-description.md).
This plan builds the whole version 1 in one phase. The steps are in build order. Each step depends on the steps before it.

## Fixed decisions

- Language: Go 1.22+ (needs `http.ServeMux` method and path patterns). In practice Go 1.26, because the current `modernc.org/sqlite` needs it.
- Dependencies: standard library + `modernc.org/sqlite` + `golang.org/x/crypto`. Nothing else.
- Build: `CGO_ENABLED=0`. One static binary.
- Module name: `hostkit`. Binary name: `hostkit`.
- Data root: `/srv/hostkit` (flag `-root`). Listen address: `127.0.0.1:8080` (flag `-listen`). Main admin host: flag `-panel-host`.
- Page body editor: plain textarea only. No WYSIWYG in version 1 (keeps the "no JS build" rule simple).
- Admin UI buttons: icon + tooltip, no caption (house rule). Placeholder text light gray (house rule).
- Components belong to one theme: `themes/<name>/components/*.html`, each defines `c/<name>`. Used with `dict` from templates and as `[[name attr="v"]]` shortcodes from page bodies. No shared component library across themes.
- The site admin shows only the sections listed in `theme.json` `"admin"` (Pages and SEO always on). The main admin can override the list per site.
- A host is `domain` + optional `path_prefix` (`/de`). Each language of a website is its own site with its own DB, theme and admin panel. No translation logic in the core. Lookup is longest prefix per segment; the prefix is stripped before routing and added back to every generated URL via `Site.BasePath`.
- SEO lives in the core (`internal/seo`): `{{seo}}` and `{{bodyEnd}}` helpers, `/robots.txt`, `/sitemap.xml`, redirects table, canonical 301s. A theme without `{{seo}}` fails `theme check`.

## Source layout

```
hostkit/
  go.mod
  cmd/hostkit/main.go          # flags, subcommands: serve, admin create, theme check, backup
  internal/
    config/                    # flags → Config struct
    db/                        # open SQLite with pragmas, run migrations (user_version)
    tenant/                    # domain map, site registry, lazy open / idle close of site.db
    store/                     # SQL for pages, menus, settings, media, users, module_data
    auth/                      # bcrypt, sessions, CSRF, login rate limit
    render/                    # theme loading, components, shortcodes, helper funcs, page cache (LRU 8 MB)
    seo/                       # head block builder, sitemap.xml, robots.txt, redirects, canonical rules
    module/                    # Module interface, Ctx, registry, ModuleStore
    mainadmin/                 # handlers + embedded templates
    siteadmin/                 # handlers + embedded templates
    web/                       # host router, public page handler, 404/503, middleware
  modules/
    contact/  list/  search/  redirect/
  migrations/main/0001_init.sql
  migrations/site/0001_init.sql
  themes/default/              # example theme
  deploy/                      # hostkit.service, nginx.conf.example, backup.timer
  README.md                    # install + HTML-to-theme guide
  reference-docs/              # owner files, not deployed (house rule)
```

## Steps

### 1. Project skeleton and config
- `go mod init hostkit`, add the two dependencies.
- `internal/config`: parse flags (`-root`, `-listen`, `-panel-host`, `-log-level`). Use `log/slog`.
- `cmd/hostkit/main.go`: subcommand switch: `serve` (default), `admin create <email>`, `theme check <folder>`, `backup`.
- `.gitignore`: `.claude/settings.local.json`, build output, `sites/`, `main.db`.

### 2. Database layer
- `internal/db`: `Open(path)` sets `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`, `cache_size=-1000`. `SetMaxOpenConns(1)` for writes safety.
- Migrations: embed `migrations/main/*.sql` and `migrations/site/*.sql` with `embed.FS`. Apply files with number greater than `PRAGMA user_version`, each in one transaction, then set `user_version`.
- `migrations/main/0001_init.sql`: tables `sites` (with `lang`), `domains` (primary key `(domain, path_prefix)`, `path_prefix` default `''`), `main_admins`, `sessions`.
- `migrations/site/0001_init.sql`: tables `pages` (content columns + SEO columns: `seo_title`, `meta_description`, `canonical`, `robots`, `og_title`, `og_description`, `og_image`, `json_ld`, `sitemap_priority`, `sitemap_changefreq`, `in_sitemap`), `menus`, `menu_items`, `settings`, `media`, `users`, `module_data`, `redirects` (`from_path` unique, `to_url`, `code`, `hits`), plus FTS5 virtual table `pages_fts` with triggers (for the search module).

### 3. Tenant registry
- `internal/tenant`: `Registry` holds `main.db` handle, `map[domain]*Site` and `map[key]*Site` under a `sync.RWMutex`.
- `Reload()` reads `sites` + `domains` from `main.db` and rebuilds the maps. Called at start and after every change in the main admin.
- `Site.DB()` opens `sites/<key>/site.db` on first use (runs site migrations), records last-use time. A goroutine every minute closes handles idle for 10 minutes.
- `Lookup(host, path)`: strip port, lower-case; try `domain + first path segment` in the map, then `domain + ""`. Return site, primary flag, status and the matched prefix. The map key is `domain + path_prefix`. `Site` carries `BasePath` and `Lang`.
- `SisterSites(domain)`: all sites on the same domain (used for the root `robots.txt` and the hreflang helper in the admin).
- `CreateSite(...)`: make folder, `site.db`, Home page, empty `main` menu, first admin user, then `Reload()`.
- `DeleteSite(key)`: close DB, move folder to `sites/.trash/<key>-<timestamp>/`, delete rows, `Reload()`.

### 4. Store (SQL queries)
- `internal/store`: plain functions taking `*sql.DB`. Pages (CRUD, list, by slug, by prefix, reorder), menus and items (CRUD, tree one level deep, reorder), settings (get all, set many), media (insert, list, delete), users (CRUD, by email), module_data (get, set, list, delete).
- No reflection. Scan into small structs by hand.

### 5. Auth
- `internal/auth`: bcrypt cost 12. Session: 32 random bytes, store SHA-256 hash in `sessions` with `scope` (`main` or site key), 30-day expiry. Cookie `HttpOnly`, `Secure`, `SameSite=Lax`, cookie name differs per scope.
- CSRF: random token kept in the session row; helper `csrf` outputs a hidden input; middleware checks it on every POST in admin and module forms.
- Rate limit: in-memory map IP → failures with timestamps; 5 failures per 15 minutes blocks login.
- Tests: session for site A rejected on site B.

### 6. Theme loading and rendering
- `internal/render`: `LoadTheme(dir)` reads `theme.json` (name, menus, settings fields, strings, admin sections, component field hints), parses each `pages/*.html` together with `layout.html`, `partials/*.html` and `components/*.html` into its own `*template.Template`, and `404.html`. Stores the result in `Theme{Pages map[string]*template.Template, Components map[string]bool, ...}`.
- `ThemeSet` holds all themes in a map; `Reload()` re-parses all folders in `themes/`. Shared by every site using the same theme.
- Helper funcs (closures over the request context): `menu`, `setting`, `asset` (path + `?v=<hash of file>`, hashes computed once at load), `media`, `page`, `pages`, `date`, `excerpt`, `t`, `csrf`, `dict`, `seo`, `bodyEnd`.
- Components: each `components/<name>.html` must define `c/<name>`; `theme check` reports a file whose define name does not match. Components are only reachable through the theme they live in.
- Shortcodes in page body: before rendering, scan `body_html` for `[[name attr="value" ...]]` with a small hand-written parser (no regex backtracking). For each match, execute `c/<name>` with a `map[string]any` of the attributes plus `.Site` and `.Page`, and splice the output. Unknown names stay as text. Result is cached with the page.
- Template data struct: `.Site`, `.Page`, `.Request`, `.Data`.
- `theme check` also fails if `layout.html` has no `{{seo}}` call and warns if it has no `{{bodyEnd}}`.
- Page cache: LRU keyed by `siteKey + path`, max 8 MB total, stores HTML bytes + ETag (SHA-256 prefix). `Invalidate(siteKey)` removes all entries of one site. Only pages without a module are cached.
- `theme check` command: load the theme, report missing `layout.html`, missing `content` block, unknown functions, and `asset` paths that do not exist on disk.

### 7. Module system
- `internal/module`: `Module` interface, `Ctx` struct, `AdminScreen` struct, `Register()`, `Get(name)`, `All()`. Optional interface `PrefixRouter` with `Prefix() string` for modules that claim a URL prefix. Optional `SiteFilter` to restrict a module to site keys.
- `ModuleStore`: thin helper over `module_data` (Get, Put, List, Delete) scoped to the module name.
- Modules in `modules/`: `contact` (honeypot field, per-IP rate limit, save to `module_data`, optional SMTP via `net/smtp` using settings `smtp_host`, `smtp_from`, `contact_to`; admin screen lists submissions), `list` (child pages by slug prefix, 10 per page, `?p=`), `search` (`?q=` over `pages_fts`), `redirect` (301 to page setting `redirect_url`).
- `cmd/hostkit/main.go` imports all four with blank imports so `init()` registers them.

### 8. Host router and public pages
- `internal/web`: `Router` implements `http.Handler`. Order: panel host → main admin mux; lookup by domain + first segment (longest prefix); unknown host → 404; alias or `www` → 301 to primary host (prefix kept); strip the prefix from `r.URL.Path` and store `BasePath` in the request context; disabled → 503 maintenance page with `Retry-After` (and `/robots.txt` → `Disallow: /`); `/admin/` → site admin mux (or 404 if `admin_enabled` is false); `/robots.txt` and `/sitemap.xml` → seo handlers; canonical URL fix (trailing slash, upper-case) → 301; redirects table → 301 / 302 / 410 and `hits+1`; module prefix routes; page by slug; `404.html`.
- Public page handler: cache lookup (serve 304 on matching `If-None-Match`), else load page, run module if set, render template, store in cache if no module.
- Middleware: recover from panics, request log, security headers not set by Nginx.

### 8b. SEO core
- `internal/seo`: `HeadBlock(site, page, data)` returns `template.HTML` for `{{seo}}`: `<title>` (seo_title or title + suffix), meta description, canonical (page value or primary-domain URL), robots meta, Open Graph + Twitter tags with fallbacks (default og image from settings), page JSON-LD, Organization JSON-LD from site settings, any JSON-LD the module put in `.Data["JSONLD"]`, then the site's `head_html` as-is.
- `BodyEnd(site)` returns `body_end_html` as-is.
- `Sitemap(site)`: published pages with `in_sitemap = 1`, `lastmod` from `updated_at`, priority and changefreq when set; plus URLs from modules that implement `Sitemap(ctx) []SitemapURL`. Written with `encoding/xml`. Stored in the page cache under key `/sitemap.xml`.
- `Robots(site)`: `robots_txt` setting or the default text with the sitemap line. For a root site (empty prefix), append `Sitemap:` lines for every sister site with a prefix on the same domain. If a domain has only prefixed sites, the router serves a default `robots.txt` with those lines.
- Hreflang: parse the page's `alternates` field (`lang=URL` per line) and print `<link rel="alternate" hreflang>` for each, plus the page's own `Site.Lang`. `og:locale` from `Site.Lang`.
- All URLs built by `seo` (canonical, og:url, sitemap `loc`) and by `menu` start with `Site.BasePath`.
- Redirect lookup: exact match on `from_path`, then code 301 / 302 / 410. Hit counter updated with a single `UPDATE`.
- Canonical rules: lower-case path, strip trailing slash (not `/`), redirect once with 301.
- Slug change hook: when a page's slug changes and the "add redirect" checkbox is on, insert a 301 row from the old slug.

### 9. Main admin panel
- `internal/mainadmin`: embedded templates (`embed.FS`), one base layout.
- Add/edit site form: hosts one per line as `domain.com` or `domain.com/de` (validated: one optional segment, lower-case; reject if another site already has it), language code, theme, first admin. Sites list shows the full host including prefix.
- Screens: login, sites list, add/edit site, site actions (disable, enable, open site admin, download backup zip via `archive/zip` streaming, delete with typed key), themes list with Reload button.
- `hostkit admin create <email>` prompts for password on stdin and inserts the first main admin.
- "Open site admin" creates a site-scoped session for the main admin so sites with `admin_enabled = false` can still be edited.

### 10. Site admin panel
- `internal/siteadmin`: embedded templates shared by all sites.
- Section list per site: read `theme.json` `"admin"` array, apply the main admin's override if set. Pages and SEO are always shown. The nav only renders enabled sections and their routes return 404 when disabled.
- Pages (routes): list, add, edit, delete, reorder up/down. Edit form with two tabs (plain HTML, tabs switched by a few lines of vanilla JS). Content tab: title, slug, template, module, body (may be empty), status, "Insert component" dropdown that pastes a `[[name …]]` shortcode from `theme.json` component hints. SEO tab: seo_title, meta_description, canonical, robots (two selects), og_title, og_description, og_image (pick from media), json_ld (textarea, validated with `json.Valid` on save), in_sitemap, priority, changefreq, alternates textarea (`lang=URL` per line, with the sister sites on the same domain listed next to it), plus a Google-result and share-card preview drawn with plain HTML/CSS from the current values.
- Session cookie for a prefixed site uses `Path=<BasePath>/`; all admin links are built with `BasePath`.
- Menus: pick menu from theme.json, items with page or URL, one nesting level, up/down.
- SEO section: site defaults form (title suffix, default og image, Organization fields), `robots.txt` textarea, redirects table (add / delete, shows hits), `head_html` and `body_end_html` textareas, link to `/sitemap.xml`.
- Settings: form built from theme.json fields; unknown keys kept.
- Media: upload with extension and MIME allow-list, random file name, reject images over 2,000 px wide using `image.DecodeConfig`, list, delete, copy URL, edit alt.
- Modules: screens from registered modules used by this site. Users: admins only.
- Roles: `editor` → pages (both tabs), menus, media. `admin` → everything including the SEO section.
- Every save calls `cache.Invalidate(siteKey)`.

### 11. Example theme
- `themes/default/`: `theme.json` (menus, settings, `"admin"` list, component hints), `layout.html` with `{{seo}}` and `{{bodyEnd}}`, `partials/header.html`, `partials/footer.html`, `components/listbox.html` and `components/card.html` (examples that use `dict` and work as shortcodes), `pages/default.html`, `pages/contact.html`, `pages/list.html`, `pages/search.html`, `404.html`, `assets/css/style.css`.
- Small, clean CSS. No JS.

### 12. Backup command
- `hostkit backup`: for `main.db` and each `site.db` run `VACUUM INTO` to a temp file, then write `/srv/backups/<date>/<site>.tar.gz` (folder with db copy + uploads). Delete folders older than 14 days.
- Restore: unpack into `sites/`, add the site row in main admin (or it already exists), press Reload.

### 13. Deployment files and README
- `deploy/hostkit.service`: user `hostkit`, `Restart=on-failure`, `MemoryMax=80M`, `Environment=GOMEMLIMIT=40MiB`.
- `deploy/hostkit-backup.service` + `.timer` (nightly).
- `deploy/nginx.conf.example`: catch-all server, proxy to `127.0.0.1:8080`, regex locations for `/_t/<theme>/` → `themes/<theme>/assets/` and `/_m/<site>/` → `sites/<site>/uploads/` with `nosniff`, long cache headers, HSTS and `X-Frame-Options`.
- `README.md`: install steps, create first admin, add a site, certbot command, the 7-step HTML-to-theme guide, helper function table, module how-to.

### 14. Tests and acceptance
- Unit tests: migrations apply cleanly; domain lookup (port strip, alias, disabled); session scope isolation (site A vs site B); module gets only its own site DB; page cache eviction at 8 MB and invalidation; `theme check` on the default theme and on a theme without `{{seo}}`; CSRF rejection; upload allow-list.
- SEO tests: `{{seo}}` output contains title with suffix, description, canonical, robots, og and twitter tags, JSON-LD; `/sitemap.xml` lists only published `in_sitemap` pages with `lastmod`; `/robots.txt` default and custom text; redirects 301 / 302 / 410 and hit count; canonical 301s for alias, `www`, trailing slash and upper-case slug; slug change creates a redirect row.
- Component tests: `dict` passes values; shortcode parser handles quotes, missing attrs and unknown names; the same `c/listbox` name in two themes renders different HTML; a theme cannot reach another theme's component.
- Admin section tests: a theme listing `["pages", "seo"]` hides the other sections and their routes return 404.
- Prefix host tests: `domain.com/de/kontakt` reaches the `/de` site with path `/kontakt`; `domain.com/design` reaches the root site; `/de/admin` and `/admin` have separate sessions; menu, canonical and sitemap URLs of the `/de` site start with `/de`; root `robots.txt` lists `/de/sitemap.xml`; a domain with only a prefixed site returns 404 at `/` and a default `robots.txt`.
- Load check script (`go test -run Bench` or a small tool): create 20 sites × 50 pages, send 1,000 mixed requests, print RSS; then wait 10 minutes idle and print RSS. Targets: < 50 MB and < 30 MB.
- Timing: log render time per request at debug level; cached < 1 ms, uncached < 5 ms.

## Acceptance checklist (copy of version 1 criteria)

- [ ] 20 sites × 50 pages: RSS under 50 MB after 1,000 requests, under 30 MB after 10 idle minutes.
- [ ] Cached page under 1 ms, uncached under 5 ms.
- [ ] New site live with no restart and no config edit (except TLS).
- [ ] Static HTML converts to a theme in under 30 minutes with the README.
- [ ] `CGO_ENABLED=0 go build` works with only the two external modules.
- [ ] Session from site A rejected on site B; module cannot read another site's data (tests).
- [ ] Restore from nightly archive works by unpacking and pressing Reload.
- [ ] Every host serves `/robots.txt` and `/sitemap.xml`; every page prints the full `{{seo}}` head block (tests).
- [ ] A redirect added in the site admin works on the next request; alias, `www`, trailing slash and upper-case slug all 301 to one canonical URL.
- [ ] A theme component works from a template with `dict` and from a page body as a `[[name …]]` shortcode; the same name in two themes gives two different outputs.
- [ ] A theme listing only `["pages", "seo"]` shows only those sections in its site admin.
- [ ] `domain.com` and `domain.com/de` run as two fully separate sites; `/design` is not caught by `/de`; root `robots.txt` lists `/de/sitemap.xml` (tests).

## Status (2026-11-10)

Version 1 is built: all 14 steps above are done, `go test ./...` passes, and the first site
(jalasenegar.com, private repo `jalasenegar-web`) runs on the waybill7 server.

Changes from the plan, and why:

- **Apache instead of Nginx on waybill7.** The server already runs Apache on 80/443 for other apps.
  [deploy/apache-hostkit.conf.example](deploy/apache-hostkit.conf.example) does the same job as the
  Nginx example: static `/_t/` and `/_m/`, everything else proxied. It is named `zz-hostkit.conf` and
  uses `ServerAlias *`, so it is the catch-all and new sites need no Apache change.
- **`-fallback-html` flag.** Because hostkit is now the catch-all, unknown hosts get the server's old
  "under construction" page instead of a plain 404.
- **No certbot.** Both domains are behind ArvanCloud CDN; the origin uses a self-signed certificate.
- **Extra helpers:** `jdate` (Persian calendar), `fadigits`, `img`, `url`, `absurl`, `now`, `add`, `lines`.
  Persian sites need Jalali dates; the others keep templates short.
- **Extra CLI:** `site create`, `site sql` (seed a site from a SQL file), `site list`, `admin password`, `version`.
  `systemctl reload hostkit` (SIGHUP) does the same as the Reload button.
- **Sessions of both panels in `main.db`** (as in the description); site sessions are bound to the site key
  and the cookie name and path differ per site. The main admin opens a site admin on another domain with a
  one-time hand-over token (60 s).
- **Public form CSRF** is a signed token (HMAC with `secret.key`), because visitors have no session.
- **Page cache entries also expire after 1 hour**, so cached pages that show time-based content stay fresh.
- **Templates are parsed once per theme into a small pool** of parsed copies; each request borrows one copy and
  binds its helpers to it. No parsing per request (only when more requests run at once than the pool holds).
- **Media:** SVG uploads are not allowed (they can carry scripts). WebP width is not checked (no stdlib decoder).

Not done yet / to check on the real server:

- The RAM and timing targets (20 sites × 50 pages) are not measured yet; one site runs now.
