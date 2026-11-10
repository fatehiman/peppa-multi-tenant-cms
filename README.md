# hostkit — one Go binary for many websites

hostkit serves every website on a server from **one process**. It picks the site, its theme,
its pages and its admin panel from the request's host name. Each site keeps its own SQLite
file. One small main admin panel defines which hosts exist.

- Go standard library + `modernc.org/sqlite` (pure Go) + `golang.org/x/crypto` (bcrypt). Nothing else.
- `CGO_ENABLED=0`: one static binary, no C compiler.
- Themes are plain Go `html/template` files. No JavaScript build step.
- SEO is in the core: `<title>`, meta description, canonical, robots, Open Graph, Twitter card,
  JSON-LD, hreflang, `sitemap.xml`, `robots.txt`, redirects and canonical 301s for every host.
- A host is a domain plus an optional path prefix: `example.com` and `example.com/de` are two
  separate sites (two databases, two admin panels).

Full design: [project-description.md](project-description.md). Build plan and status: [plan.md](plan.md).
Security notes and open items: [secure-project.md](secure-project.md).

## Contents

1. [Build](#build)
2. [Install on a Debian server](#install-on-a-debian-server)
3. [Daily use](#daily-use)
4. [Command line](#command-line)
5. [Convert an HTML/CSS design into a theme (30 minutes)](#convert-an-htmlcss-design-into-a-theme)
6. [Template reference](#template-reference)
7. [Modules](#modules)
8. [Backups and restore](#backups-and-restore)
9. [Development](#development)

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o hostkit ./cmd/hostkit
```

Go 1.26 or newer (the current `modernc.org/sqlite` needs it). The binary has the migrations, the admin panels and the four standard modules inside.

## Install on a Debian server

1. User and folders:

   ```bash
   useradd --system --home /srv/hostkit --shell /usr/sbin/nologin hostkit
   mkdir -p /srv/hostkit/themes /srv/backups/hostkit
   cp hostkit /srv/hostkit/ && cp -r themes/default /srv/hostkit/themes/
   chown -R hostkit:hostkit /srv/hostkit /srv/backups/hostkit
   chmod 755 /srv/hostkit /srv/hostkit/themes
   ```

2. The first main admin (the password is read from stdin):

   ```bash
   sudo -u hostkit /srv/hostkit/hostkit -root /srv/hostkit admin create you@example.com
   ```

3. systemd: copy [deploy/hostkit.service](deploy/hostkit.service), set `-listen` and `-panel-host`,
   then `systemctl enable --now hostkit`. For the nightly backup also copy
   [deploy/hostkit-backup.service](deploy/hostkit-backup.service) and
   [deploy/hostkit-backup.timer](deploy/hostkit-backup.timer) and run `systemctl enable --now hostkit-backup.timer`.

4. Web server in front (TLS, static files):
   - Nginx: [deploy/nginx.conf.example](deploy/nginx.conf.example).
   - Apache: [deploy/apache-hostkit.conf.example](deploy/apache-hostkit.conf.example).

   Both serve `/_t/<theme>/…` from `themes/<theme>/assets/` and `/_m/<site>/…` from
   `sites/<site>/uploads/` directly, and send everything else to hostkit. The web server
   config is a catch-all, so a new site needs no web server change.

5. TLS: with certbot, `certbot --nginx -d example.com -d www.example.com` for each new domain.
   Behind a CDN (Cloudflare, ArvanCloud), a self-signed origin certificate is enough.

6. Open `https://<panel-host>/` and log in.

Updates later: `SSH_HOST=<server> bash deploy/deploy.sh` (tests, builds, uploads, restarts).

**RAM:** run with `GOMEMLIMIT=40MiB` and `MemoryMax=80M` (already in the unit file).

## Daily use

**Add a site** (main admin → Sites → plus button): name, key (folder name), hosts (one per line,
first = primary; `domain.com`, `sub.domain.com` or `domain.com/de`), language, theme, first site
admin (a user name like `mainadmin` or an email). The site is live at once. Point its DNS to the
server and add the TLS certificate.

**Host rules (fixed for every host, site admins can not change them):**

- `www.<host>` always answers **301** to `<host>` (same path and query). A `www.` host can not be
  added. This works for subdomains too: `www.sub.domain.com` → `sub.domain.com`.
- A subdomain (`shop.domain.com`) is a separate host, exactly like another domain: its own site,
  database, theme, admin panel, session cookie, `robots.txt` and `sitemap.xml`.
- Other hosts of a site (aliases) answer 301 to its primary host.

**Under construction** (site admin → SEO → Site status, admins only): while it is on, visitors get
an "under construction" page with HTTP **503** + `Retry-After` + `noindex`, and `robots.txt` says
`Disallow: /`. People logged in to *this* site's admin (admins, editors, or the main admin through
"open site admin") see the real site; their pages are sent with `Cache-Control: private, no-store`
so a CDN never keeps them. The admin panel shows an "Under construction" badge. A theme can design
the page with an optional `under-construction.html` (a full page, all helpers work); without it
hostkit shows a simple page with the site title and the message from the same form.

**Site admin:** `https://<domain>/mainadmin` (or `https://domain.com/de/mainadmin`). The old
path `/admin` is a normal 404 page (fewer hits from bots that scan for `/admin`). Sections:
Pages (routes + SEO tab), Menus, SEO (title suffix, default image, Organization JSON-LD,
robots.txt, redirects, custom head/body HTML), Settings (fields from `theme.json`), Media,
Modules, Users. A theme decides which sections show with `"admin": [...]` in `theme.json`;
the main admin can override the list per site. Pages and SEO are always on.

Roles: **editor** = pages, menus, media, module screens. **admin** = everything.

**Open site admin** from the main admin (key button) logs you in to any site, also when the
site's own admin panel is switched off.

**Login protection** (main admin and every site admin):

- 5 wrong passwords per IP in 15 minutes, then that IP must wait.
- 10 wrong passwords per user name in 15 minutes (from all IPs together), then that user name
  must wait. This stops a botnet that tries one account from many IPs. An attacker can use it
  to lock a user for 15 minutes; the main admin can still use "open site admin", and
  `systemctl restart hostkit` clears all counters (they live in memory).
- After 2 wrong passwords (same IP or same user name) the login form also asks for the CAPTCHA.
- An unknown user name takes the same time as a wrong password, so the answer time does not
  show which user names exist.
- The real visitor IP comes from the web server (`X-Forwarded-For`, last entry). Behind a CDN the
  web server must take the IP from the CDN header (Apache `mod_remoteip`, see the Apache example).

**Reload** (main admin → Themes → refresh button, or `systemctl reload hostkit`): re-parses all
themes, reloads the site list, closes the site databases and clears the page cache.

## Command line

Flags come **before** the command.

| Command | What it does |
| --- | --- |
| `hostkit [flags] serve` | Run the web server (default command) |
| `hostkit [flags] admin create <user>` | Create a main admin (user name or email); password from stdin |
| `hostkit [flags] admin password <user>` | Set a new main admin password |
| `hostkit [flags] site create -key k -name n -hosts a.com,www.a.com [-theme t] [-lang l] [-admin-email e]` | Create a site (then `systemctl reload hostkit`) |
| `hostkit [flags] site sql <key> <file.sql>` | Run a SQL file on one site database (seed content) |
| `hostkit [flags] site list` | List sites and hosts |
| `hostkit theme check <folder>` | Check a theme before you use it |
| `hostkit [flags] backup` | Nightly backup into `-backup-dir` |
| `hostkit version` | Print the version |

Flags: `-root /srv/hostkit`, `-listen 127.0.0.1:8080`, `-panel-host panel.example.com`,
`-backup-dir /srv/backups`, `-log-level info`, `-fallback-html <file>` (page for unknown hosts),
`-dev` (http URLs, cookies without `Secure`, hostkit serves static files itself), `-static`,
`-notify-url <gateway URL without m=>` (Telegram messages, for example from the contact form;
empty = off), `-server-name <name>` (first part of the Telegram sender code; default: host name).

## Convert an HTML/CSS design into a theme

A theme is a folder in `themes/`. The folder name is the theme name (`a-z`, `0-9`, `-`).

```
themes/clean/
  theme.json          # name, menus, settings fields, strings, admin sections, component hints
  layout.html         # the HTML skeleton: head, header, footer
  partials/*.html     # used as {{template "partials/header.html" .}}
  components/*.html   # each defines {{define "c/<file name>"}}
  pages/default.html  # used when a page picks no template
  pages/*.html        # more page templates (shown in the page form)
  404.html
  under-construction.html  # optional: full page shown while the site is under construction
  assets/             # css, js, images, fonts (served by the web server at /_t/<theme>/)
```

Seven steps:

1. Copy the design's CSS, JS, images and fonts into `assets/`. In the HTML, replace each path
   with `{{asset "css/style.css"}}` (it adds `?v=<hash>` so browsers get new files after a change).
   Paths inside CSS files (`url(../fonts/x.woff)`) stay relative and keep working.
2. Take `index.html`. Everything that repeats on every page (head, header, nav, footer) becomes
   `layout.html`. Put `{{block "content" .}}{{end}}` where the page body was.
3. In `<head>` put `{{seo}}` (it prints `<title>`, description, canonical, Open Graph, JSON-LD …)
   and remove the old `<title>`. Before `</body>` put `{{bodyEnd}}`.
   Set `<html lang="{{.Site.Lang}}" dir="{{.Site.Dir}}">`.
4. Replace the hard-coded nav links with a loop:
   `{{range menu "main"}}<li><a href="{{.URL}}">{{.Label}}</a></li>{{end}}`
   (`.Active` is true for the current page, `.Children` holds the second level).
5. Create `pages/default.html`:
   `{{define "content"}}<h1>{{.Page.Title}}</h1>{{.Page.Body}}{{end}}`
6. For each special page in the design (landing, contact, news list), make one more file in `pages/`
   with its own `{{define "content"}}…{{end}}`. Pick it in the page form's Template list.
7. List the menus and editable texts (phone, address, logo) in `theme.json`, use them with
   `{{setting "phone"}}`, run `hostkit theme check themes/clean`, copy the folder to the server
   and press **Reload**.

Minimal `theme.json`:

```json
{
  "name": "Clean",
  "menus": ["main", "footer"],
  "settings": [
    {"key": "phone", "label": "Phone", "type": "text"},
    {"key": "logo", "label": "Logo", "type": "image", "help": "file name from Media"}
  ],
  "strings": {"read_more": "Read more"},
  "admin": ["pages", "menus", "seo", "settings", "media"],
  "components": {"card": {"title": "text", "text": "text"}}
}
```

Setting types: `text`, `textarea`, `email`, `url`, `image`. Unknown keys in the database are kept.

## Template reference

Every template receives:

| Value | Content |
| --- | --- |
| `.Site` | `Name`, `Key`, `Domain`, `BasePath` (`""` or `/de`), `Lang`, `Dir` (`rtl`/`ltr`), `URL` (absolute home), `Settings` (map) |
| `.Page` | `Title`, `Slug`, `URL`, `Body` (HTML), `MetaDescription`, `Template`, `Image` (OG image), `CreatedAt`, `UpdatedAt` |
| `.Request` | `Path` (without the site prefix), `Query` (`{{.Request.Query.Get "p"}}`) |
| `.Data` | whatever the page's module returned |

Helpers:

| Helper | Example | Returns |
| --- | --- | --- |
| `menu` | `{{range menu "main"}}` | Items with `Label`, `URL`, `Active`, `Children`. A menu URL `#about` becomes `/#about` on other pages |
| `setting` | `{{setting "phone"}}` | One setting value |
| `asset` | `{{asset "css/style.css"}}` | `/_t/<theme>/css/style.css?v=<hash>` |
| `media` | `{{media "abc.jpg"}}` | Public URL of an upload (full URLs stay as they are) |
| `img` | `{{img "abc.jpg" "class"}}` | An `<img>` tag with the alt text from Media |
| `page` | `{{with page "about"}}{{.Title}}{{end}}` | Another published page |
| `pages` | `{{range pages "news/" 5}}` | Up to 5 published pages under `news/`, newest first |
| `date` | `{{date .Page.UpdatedAt "2006-01-02"}}` | Formatted date (Go layout) |
| `jdate` | `{{jdate .Page.CreatedAt "long"}}` | Persian (Jalali) date: `long` = `19 آبان 1405`, `year`, or `1405/08/19` |
| `fadigits` | `{{fadigits 1405}}` | Persian digits |
| `excerpt` | `{{excerpt .Body 160}}` | Plain-text summary |
| `t` | `{{t "read_more"}}` | Text from `theme.json` `strings` (a setting `t_<key>` overrides it) |
| `csrf` | `{{csrf}}` | Hidden input for forms that post to a module |
| `captcha` | `{{captcha}}` or `{{captcha "form-control"}}` | CAPTCHA widget: picture of 5 digits (click = new picture) + text input. The second form adds a CSS class to the input. Texts: strings `captcha` (placeholder) and `captcha_title` (tooltip); defaults by site language. Style with `.hk-captcha`, `.hk-captcha-img`, `.hk-captcha-input`. Several on one page are fine |
| `dict` | `{{template "c/card" (dict "Title" "Hi")}}` | A map, to pass several values to a component |
| `url` | `{{url "/contact"}}` | Site path with the prefix (`/de/contact`) |
| `absurl` | `{{absurl .Page.URL}}` | Absolute URL on the primary domain |
| `now`, `add`, `lines` | | Current time, `a+b`, non-empty lines of a text |
| `seo` | `{{seo}}` in `<head>` | The whole SEO head block plus the site's custom head HTML |
| `bodyEnd` | `{{bodyEnd}}` before `</body>` | The site's custom end-of-body HTML |

**Components** live in `components/<name>.html` and define `c/<name>`. Use them:

- from a template: `{{template "c/card" (dict "Title" .Page.Title "Big" true)}}`
- from a page body (in the admin) as a shortcode: `[[card title="Hello" text="World"]]`.
  Attributes are passed both as written (`.title`) and capitalised (`.Title`), plus `.Site` and
  `.Page`. Unknown names stay as text.

Components belong to one theme. Two themes may each have a `card` that looks different.

## Modules

A module is Go code compiled into the binary and switched on per page (page form → Module).
Its result is `.Data` in the template.

| Module | Does | Template data / settings |
| --- | --- | --- |
| `contact` | Form with honeypot (`website` field), CSRF, CAPTCHA, 5 messages per IP per 10 minutes; saves to `module_data`, Telegram message to the monitoring group, optional SMTP | `.Data.Sent`, `.Data.Error`, `.Data.Values`. Form fields `name email phone subject message`, optional `_return` path (a local path only). **Every form that posts to the contact page needs `{{captcha}}`.** Settings `contact_captcha=0` (no CAPTCHA), `contact_telegram=0` (no Telegram), `contact_to`, `smtp_host` (`host:port`), `smtp_user`, `smtp_pass`, `smtp_from`. Admin screen: Modules → submissions |
| `list` | Child pages with `?p=2` pagination | Module settings `prefix=news/`, `per_page=10`. `.Data.Items`, `.Data.Page`, `.Data.Pages`, `.Data.PrevURL`, `.Data.NextURL` |
| `search` | Full-text search (SQLite FTS5) | `?q=`. `.Data.Query`, `.Data.Items`, `.Data.Count` |
| `redirect` | 301 to another URL | Module setting `redirect_url=https://…` |

**Shared tools for module code** (in `module.Ctx`): `ctx.CaptchaValid()` checks the
`{{captcha}}` fields (single use: call it once per request), `ctx.Notify(title, lines...)` sends a
Telegram message to the monitoring group (needs `-notify-url`; build lines with `notify.Field` /
`notify.Esc`), `ctx.CSRFValid()` checks `{{csrf}}`, `ctx.ClientIP` is the visitor IP.

Write a new module in `modules/<name>/`:

```go
package booking

import "hostkit/internal/module"

func init() { module.Register(&Booking{}) }

type Booking struct{}

func (*Booking) Name() string                 { return "booking" }
func (*Booking) Admin() []module.AdminScreen { return nil }
func (*Booking) Handle(ctx *module.Ctx) (any, bool, error) {
    // ctx.DB is this site's database only; ctx.Store is a key/value helper over module_data.
    return map[string]any{"Free": 3}, false, nil
}
```

Then add `_ "hostkit/modules/booking"` to `cmd/hostkit/main.go` and rebuild.
Optional interfaces: `Sites() []string` (only these site keys), `Prefix() string` (claim a URL
prefix like `/api/booking/`), `Cacheable() bool`, `Sitemap(ctx) []module.SitemapURL`,
`Migrations() []string` (own tables, applied once per site).

## Backups and restore

`hostkit backup` (nightly by the timer) writes `<backup-dir>/<YYYY-MM-DD>/main.tar.gz` and one
`<site>.tar.gz` per site (an online `VACUUM INTO` copy of `site.db` plus `uploads/`), and deletes
folders older than 14 days. Copy that folder off the server (rsync, rclone).

The main admin can also download one site as a zip (site page → download button).

**Restore a site:** unpack the archive into `sites/` (it holds `<key>/site.db` and
`<key>/uploads/`), `chown -R hostkit:hostkit`, then press **Reload**. If the site row is gone,
add the site again in the main admin with the same key first (the existing folder is refused,
so move it aside, create the site, then put the folder back and press Reload).

## Development

```bash
go test ./...
go run ./cmd/hostkit -root ./devdata -dev -panel-host panel.localhost -listen 127.0.0.1:8080
```

With `-dev`, hostkit serves theme assets and uploads itself, so no web server is needed.
Browsers resolve `*.localhost` to 127.0.0.1, so `http://panel.localhost:8080/` and a site with the host
`a.localhost` work without a hosts file.
