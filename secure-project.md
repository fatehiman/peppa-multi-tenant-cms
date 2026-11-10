# Security notes for hostkit

Result of the security check on 2026-11-10 (code review + tests on the live server waybill7,
`cms.kimiasoft.ir` and `jalasenegar.com`). This file lists what is done and what is still open,
so the open items are not forgotten.

## Done (2026-11-10)

| Item | What was done |
| --- | --- |
| Login brute force per user name | 10 failures per user name in 15 minutes (all IPs together), next to 5 per IP. |
| Login CAPTCHA | After 2 wrong passwords (same IP or same user name) the login form asks for the CAPTCHA. |
| Site admin path | `/mainadmin` instead of `/admin` (fewer bot hits; this alone is not a real protection). |
| User names found by timing | An unknown user name took ~2 ms, a real one ~250 ms (bcrypt). Now both take the bcrypt time. |
| Open redirect in the contact form | `_return=/<TAB>/evil.com` passed the check; browsers delete the tab and go to `//evil.com`. Now tabs, spaces, control characters and `\` are refused. |
| Contact form spam | CAPTCHA (`{{captcha}}`) is required, and every message goes to Telegram (monitoring). |

## Checked and fine

- Direct file access: `/.env`, `/.git/config`, `/main.db`, `/site.db`, `/secret.key` and `../` tricks
  (`%2e%2e`, `..%2f`) all give 404. Apache serves only `/_t/<theme>/…` (theme assets) and
  `/_m/<site>/<file>` (uploads).
- File rights on the server: `main.db`, `site.db` and `secret.key` are `600`, owner `hostkit`.
- Visitor IP behind ArvanCloud: Apache `mod_remoteip` reads `AR-Real-IP` only from Arvan IP ranges.
  Fake `AR-Real-IP` / `X-Forwarded-For` headers are ignored (tested through the CDN and directly).
- Cookies are `HttpOnly`, `Secure`, `SameSite=Lax`; every admin POST checks a CSRF token.
- SQL uses parameters everywhere; the search query is cleaned before FTS5.
- Uploads: the content must match the extension, file names are random, no SVG or HTML.
- HTTP server timeouts (slow-client attacks), systemd hardening (`ProtectSystem=strict`, …).

## Open items (for the future)

### 1. Page HTML can hold JavaScript (left as it is on purpose)

The page body is saved and shown as raw HTML. A site user with the role **editor** can put a
`<script>` in a page. When an **admin** of the same site opens that page, the script runs with the
admin's login: it can read the admin's CSRF token and, for example, create a new admin user.
So an editor can make themselves an admin.

Why it is left open now: the sites are managed by us (Amir and the team). Pages are mostly
designed as HTML files in the theme; site admins mostly change settings, prices, plans, menus,
phone numbers and later products. Only a few pages (About us, articles) are edited in the HTML
editor. There are no untrusted editors.

Fix when we give editor accounts to people we do not fully trust (pick one):

- **Clean the HTML that editors save**: an allow-list of tags and attributes (no `<script>`,
  no `on*` attributes, no `javascript:` links, no `<iframe>`/`<object>`/`<embed>`/`<form>`).
  Admin HTML stays as it is. Needs `golang.org/x/net/html` (Go team package) or a small parser.
- **Move all site admins to a separate origin** (for example `cms.kimiasoft.ir/s/<site>/`). Then
  JavaScript on a public site can never reach any admin. Strongest fix, but a big change:
  customers would log in on the panel host.

The same is true for a site **admin** and the SEO fields "custom head / body HTML": an admin can
add any script. That is wanted (analytics, chat widgets), but it matters if two sites share one
domain with a path prefix (`example.com` and `example.com/de`): they are the same browser origin,
so the admin of one could attack the admin of the other. Do not give such sites to different owners.

### 2. The origin server can be reached without the CDN

`185.231.115.73` answers for every hostkit site directly (also on plain HTTP). An attacker can skip
ArvanCloud's DDoS and WAF protection. Fix: in `/etc/apache2/sites-available/zz-hostkit.conf` allow
only the Arvan ranges (`Require ip …`, the same list as `/etc/apache2/smsgw-arvan-ips.conf`).
Do it in this vhost only: other sites on waybill7 (for example `bm.peppasoft.com`) need direct access.

### 3. Random query strings skip the page cache

The page cache key includes the query string, so `/?x=1`, `/?x=2`, … each force a new render and
database read, and push real pages out of the 8 MB cache. There is no general request limit in
hostkit. Fix: ignore unknown query parameters in the cache key (keep only `p`, `q` and the ones a
module asks for), and turn on ArvanCloud rate limiting.

### 4. Contact form storage has no limit

Many IPs that solve the CAPTCHA (paid CAPTCHA-solving services) can still fill `module_data`.
Fix if it happens: a total limit per site per hour, and deleting old submissions.

### 5. Small items

- No HSTS header and no Content-Security-Policy.
- The origin shows `Server: Apache/2.4.x (Debian)` (set `ServerTokens Prod`).
- Admin error pages show internal error text (`Internal error: …`).
- The `?ok=` / `?err=` message in the admin comes from the URL, so a link can show a fake message.
- Sessions last 30 days and do not end after inactivity.
- The main admin (`cms.kimiasoft.ir`) has no 2FA and no IP allow-list; it can open every site.
- Login and CAPTCHA counters live in memory: a restart clears them.
