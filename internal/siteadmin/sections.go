package siteadmin

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"image"
	_ "image/gif" // register decoders for DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"hostkit/internal/auth"
	"hostkit/internal/core"
	"hostkit/internal/module"
	"hostkit/internal/render"
	"hostkit/internal/seo"
	"hostkit/internal/store"
)

func settingsOf(q *req) map[string]string { return core.Settings(q.DB) }

// ---------- menus ----------

func (h *Handler) menuKeys(q *req) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if t := h.App.Theme(q.Site); t != nil {
		for _, k := range t.Meta.Menus {
			add(k)
		}
	}
	add("main")
	if list, err := store.ListMenus(q.DB); err == nil {
		for _, m := range list {
			add(m.Key)
		}
	}
	return keys
}

func (h *Handler) menus(q *req) {
	keys := h.menuKeys(q)
	cur := q.R.URL.Query().Get("m")
	if cur == "" {
		cur = keys[0]
	}
	items, err := store.MenuTree(q.DB, cur)
	if err != nil {
		q.fail(err)
		return
	}
	pages, _ := store.ListPages(q.DB)
	var edit *store.MenuItem
	if eid, _ := strconv.ParseInt(q.R.URL.Query().Get("edit"), 10, 64); eid != 0 {
		for _, it := range items {
			if it.ID == eid {
				edit = it
			}
			for _, c := range it.Children {
				if c.ID == eid {
					edit = c
				}
			}
		}
	}
	if edit == nil {
		edit = &store.MenuItem{}
	}
	q.render(h, "menus", "Menus", "menus", map[string]any{"Keys": keys, "Cur": cur, "Items": items, "Pages": pages, "Edit": edit})
}

func (h *Handler) menuItemSave(q *req) {
	r := q.R
	key := strings.TrimSpace(r.FormValue("menu"))
	if key == "" {
		key = "main"
	}
	menuID, err := store.EnsureMenu(q.DB, key)
	if err != nil {
		q.fail(err)
		return
	}
	it := &store.MenuItem{MenuID: menuID, Label: strings.TrimSpace(r.FormValue("label")), URL: strings.TrimSpace(r.FormValue("url"))}
	it.ID, _ = strconv.ParseInt(r.FormValue("id"), 10, 64)
	it.PageID, _ = strconv.ParseInt(r.FormValue("page_id"), 10, 64)
	it.ParentID, _ = strconv.ParseInt(r.FormValue("parent_id"), 10, 64)
	back := "/menus?m=" + key
	if it.ParentID == it.ID {
		it.ParentID = 0
	}
	if it.PageID != 0 {
		it.URL = ""
		if it.Label == "" {
			if p, err := store.PageByID(q.DB, it.PageID); err == nil {
				it.Label = p.Title
			}
		}
	}
	if it.Label == "" || (it.PageID == 0 && it.URL == "") {
		q.back(back, "", "Give a label and pick a page or type a URL.")
		return
	}
	if err := store.SaveMenuItem(q.DB, it); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back(back, "Saved.", "")
}

func (h *Handler) menuItemDelete(q *req) {
	if err := store.DeleteMenuItem(q.DB, q.id()); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/menus?m="+q.R.FormValue("menu"), "Item deleted.", "")
}

func (h *Handler) menuItemMove(q *req) {
	dir := 1
	if q.R.FormValue("dir") == "up" {
		dir = -1
	}
	if err := store.MoveMenuItem(q.DB, q.id(), dir); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/menus?m="+q.R.FormValue("menu"), "", "")
}

// ---------- SEO ----------

var seoKeys = []string{"seo_title_suffix", "meta_description", "default_og_image", "org_name", "org_logo", "org_phone",
	"org_social", "robots_txt", "head_html", "body_end_html"}

func (h *Handler) seoForm(q *req) {
	reds, err := store.ListRedirects(q.DB)
	if err != nil {
		q.fail(err)
		return
	}
	set := settingsOf(q)
	robotsNow := seo.Robots(h.App.Cfg.Scheme, q.Site, set, h.App.Reg.SisterSites(q.Site.Domain))
	q.render(h, "seo", "SEO", "seo", map[string]any{"S": set, "Redirects": reds, "RobotsNow": robotsNow,
		"SitemapURL": seo.AbsURL(h.App.Cfg.Scheme, q.Site, "/sitemap.xml"),
		"RobotsURL":  seo.AbsURL(h.App.Cfg.Scheme, q.Site, "/robots.txt"), "Prefixed": q.Site.BasePath != ""})
}

func (h *Handler) seoSave(q *req) {
	m := map[string]string{}
	for _, k := range seoKeys {
		m[k] = strings.ReplaceAll(q.R.FormValue(k), "\r\n", "\n")
		if k != "head_html" && k != "body_end_html" && k != "robots_txt" && k != "org_social" {
			m[k] = strings.TrimRight(m[k], " \n")
		}
	}
	if err := store.SetSettings(q.DB, m); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/seo", "Saved.", "")
}

func (h *Handler) redirectAdd(q *req) {
	from := strings.TrimSpace(q.R.FormValue("from_path"))
	to := strings.TrimSpace(q.R.FormValue("to_url"))
	code, _ := strconv.Atoi(q.R.FormValue("code"))
	if code != 301 && code != 302 && code != 410 {
		code = 301
	}
	if !strings.HasPrefix(from, "/") || len(from) < 2 {
		q.back("/seo", "", "The 'from' path must start with / (for example /old-page).")
		return
	}
	if len(from) > 1 {
		from = strings.TrimRight(from, "/")
	}
	if code != 410 && to == "" {
		q.back("/seo", "", "Type a target URL, or choose 410 (gone).")
		return
	}
	if err := store.SaveRedirect(q.DB, from, to, code); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/seo", "Redirect saved.", "")
}

func (h *Handler) redirectDelete(q *req) {
	if err := store.DeleteRedirect(q.DB, q.id()); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/seo", "Redirect deleted.", "")
}

// ---------- settings ----------

type fieldVal struct {
	render.Field
	Value string
}

func (h *Handler) settingsForm(q *req) {
	set := settingsOf(q)
	known := map[string]bool{}
	for _, k := range seoKeys {
		known[k] = true
	}
	var fields []fieldVal
	add := func(f render.Field) {
		if known[f.Key] {
			return
		}
		known[f.Key] = true
		if f.Type == "" {
			f.Type = "text"
		}
		if f.Label == "" {
			f.Label = f.Key
		}
		fields = append(fields, fieldVal{Field: f, Value: set[f.Key]})
	}
	add(render.Field{Key: "site_title", Label: "Site title"})
	if t := h.App.Theme(q.Site); t != nil {
		for _, f := range t.Meta.Settings {
			add(f)
		}
	}
	var extra []string
	for k := range set {
		if !known[k] && !strings.HasPrefix(k, "_") {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	q.render(h, "settings", "Settings", "settings", map[string]any{"Fields": fields, "Extra": extra, "S": set})
}

func (h *Handler) settingsSave(q *req) {
	r := q.R
	_ = r.ParseForm()
	m := map[string]string{}
	for k, v := range r.PostForm {
		if strings.HasPrefix(k, "s_") && len(v) > 0 {
			m[strings.TrimPrefix(k, "s_")] = strings.ReplaceAll(v[0], "\r\n", "\n")
		}
	}
	if nk := strings.TrimSpace(r.FormValue("new_key")); nk != "" {
		if strings.HasPrefix(nk, "_") || strings.ContainsAny(nk, " \t") {
			q.back("/settings", "", "A new key may not start with _ or have spaces.")
			return
		}
		m[nk] = r.FormValue("new_value")
	}
	if err := store.SetSettings(q.DB, m); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/settings", "Saved.", "")
}

// ---------- media ----------

// allowed extensions and the sniffed MIME types each may have.
var allowedUploads = map[string][]string{
	".jpg": {"image/jpeg"}, ".jpeg": {"image/jpeg"}, ".png": {"image/png"}, ".gif": {"image/gif"},
	".webp": {"image/webp"}, ".ico": {"image/x-icon", "image/vnd.microsoft.icon"},
	".pdf": {"application/pdf"},
	".doc": {"application/octet-stream", "application/msword"}, ".xls": {"application/octet-stream"},
	".ppt":  {"application/octet-stream"},
	".docx": {"application/zip"}, ".xlsx": {"application/zip"}, ".pptx": {"application/zip"},
	".odt": {"application/zip"}, ".ods": {"application/zip"},
	".txt": {"text/plain"}, ".csv": {"text/plain"},
	".zip": {"application/zip"}, ".mp4": {"video/mp4"}, ".webm": {"video/webm"}, ".mp3": {"audio/mpeg"},
}

// MaxImageWidth: wider images are rejected rather than resized.
const MaxImageWidth = 2000

// CheckUpload validates a file by extension, sniffed MIME type and image width.
// It returns the MIME type and the image size (0 for non-images).
func CheckUpload(name string, head []byte, r io.Reader) (mime string, w, hgt int, err error) {
	ext := strings.ToLower(filepath.Ext(name))
	allowed, ok := allowedUploads[ext]
	if !ok {
		return "", 0, 0, fmt.Errorf("file type %s is not allowed", ext)
	}
	sniff := http.DetectContentType(head)
	sniffBase, _, _ := strings.Cut(sniff, ";")
	match := false
	for _, a := range allowed {
		if sniffBase == a {
			match = true
		}
	}
	if !match {
		return "", 0, 0, fmt.Errorf("the content of the file (%s) does not match %s", sniffBase, ext)
	}
	if strings.HasPrefix(sniffBase, "image/") && sniffBase != "image/webp" && sniffBase != "image/x-icon" && sniffBase != "image/vnd.microsoft.icon" {
		cfg, _, err := image.DecodeConfig(io.MultiReader(bytes.NewReader(head), r))
		if err != nil {
			return "", 0, 0, errors.New("the image could not be read")
		}
		if cfg.Width > MaxImageWidth {
			return "", 0, 0, fmt.Errorf("the image is %d px wide; the limit is %d px. Make it smaller first", cfg.Width, MaxImageWidth)
		}
		return sniffBase, cfg.Width, cfg.Height, nil
	}
	return sniffBase, 0, 0, nil
}

type mediaRow struct {
	*store.Media
	URL     string
	IsImage bool
}

func (h *Handler) mediaList(q *req) {
	list, err := store.ListMedia(q.DB)
	if err != nil {
		q.fail(err)
		return
	}
	rows := make([]mediaRow, len(list))
	for i, m := range list {
		rows[i] = mediaRow{Media: m, URL: render.MediaURL(q.Site.Key, m.FileName), IsImage: strings.HasPrefix(m.Mime, "image/")}
	}
	q.render(h, "media", "Media", "media", map[string]any{"Items": rows, "MaxWidth": MaxImageWidth})
}

func (h *Handler) mediaUpload(q *req) {
	q.R.Body = http.MaxBytesReader(q.W, q.R.Body, 32<<20)
	if err := q.R.ParseMultipartForm(4 << 20); err != nil {
		q.back("/media", "", "Upload failed: the file is too big (limit 30 MB).")
		return
	}
	if !auth.CheckCSRF(q.R, q.U.Sess) {
		http.Error(q.W, "Bad CSRF token", http.StatusForbidden)
		return
	}
	files := q.R.MultipartForm.File["file"]
	if len(files) == 0 {
		q.back("/media", "", "Choose a file first.")
		return
	}
	dir := h.App.Reg.UploadsDir(q.Site.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		q.fail(err)
		return
	}
	var errs []string
	n := 0
	for _, fh := range files {
		if err := h.saveUpload(q, dir, fh.Filename, func() (io.ReadSeekCloser, error) { return fh.Open() }); err != nil {
			errs = append(errs, fh.Filename+": "+err.Error())
			continue
		}
		n++
	}
	h.saved(q)
	q.back("/media", fmt.Sprintf("%d file(s) uploaded.", n), strings.Join(errs, " · "))
}

func (h *Handler) saveUpload(q *req, dir, name string, open func() (io.ReadSeekCloser, error)) error {
	f, err := open()
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 512)
	nh, _ := io.ReadFull(f, head)
	head = head[:nh]
	mime, w, hgt, err := CheckUpload(name, head, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(name))
	stored := strings.ToLower(strings.ReplaceAll(auth.RandomToken(9), "_", "x")) + ext
	stored = strings.ReplaceAll(stored, "-", "y")
	out, err := os.OpenFile(filepath.Join(dir, stored), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	size, err := io.Copy(out, f)
	out.Close()
	if err != nil {
		os.Remove(filepath.Join(dir, stored))
		return err
	}
	return store.InsertMedia(q.DB, &store.Media{FileName: stored, OrigName: filepath.Base(name), Mime: mime, Size: size, Width: w, Height: hgt})
}

func (h *Handler) mediaAlt(q *req) {
	if err := store.SetMediaAlt(q.DB, q.id(), strings.TrimSpace(q.R.FormValue("alt"))); err != nil {
		q.fail(err)
		return
	}
	h.saved(q)
	q.back("/media", "Alt text saved.", "")
}

func (h *Handler) mediaDelete(q *req) {
	m, err := store.MediaByID(q.DB, q.id())
	if err != nil {
		q.back("/media", "", "File not found.")
		return
	}
	if err := store.DeleteMedia(q.DB, m.ID); err != nil {
		q.fail(err)
		return
	}
	_ = os.Remove(filepath.Join(h.App.Reg.UploadsDir(q.Site.Key), filepath.Base(m.FileName)))
	h.saved(q)
	q.back("/media", "File deleted.", "")
}

// ---------- modules ----------

type screenLink struct {
	Module, Key, Title, URL string
}

func moduleScreens(siteKey string) []screenLink {
	var out []screenLink
	for _, m := range module.ForSite(siteKey) {
		for _, s := range m.Admin() {
			out = append(out, screenLink{Module: m.Name(), Key: s.Key, Title: s.Title})
		}
	}
	return out
}

func (h *Handler) modulesList(q *req) {
	list := moduleScreens(q.Site.Key)
	for i := range list {
		list[i].URL = q.A + "/modules/" + list[i].Module + "/" + list[i].Key
	}
	q.render(h, "modules", "Modules", "modules", map[string]any{"Screens": list})
}

func (h *Handler) moduleScreen(q *req) {
	m := module.Get(q.R.PathValue("module"))
	if m == nil || !module.Allowed(m, q.Site.Key) {
		http.NotFound(q.W, q.R)
		return
	}
	for _, s := range m.Admin() {
		if s.Key != q.R.PathValue("screen") {
			continue
		}
		a := &module.AdminCtx{Site: q.Site, DB: q.DB, Store: module.Store{DB: q.DB, Module: m.Name()}, W: q.W, R: q.R,
			CSRFField: template.HTML(`<input type="hidden" name="_csrf" value="` + template.HTMLEscapeString(q.U.Sess.CSRF) + `">`),
			BaseURL:   q.A + "/modules/" + m.Name() + "/" + s.Key}
		out, err := s.Handle(a)
		if err != nil {
			q.fail(err)
			return
		}
		if a.Done {
			return
		}
		if q.R.Method == http.MethodPost {
			h.saved(q)
		}
		q.render(h, "module_screen", s.Title, "modules", map[string]any{"Title": s.Title, "HTML": out})
		return
	}
	http.NotFound(q.W, q.R)
}

// ---------- users ----------

func (h *Handler) usersList(q *req) {
	list, err := store.ListUsers(q.DB)
	if err != nil {
		q.fail(err)
		return
	}
	q.render(h, "users", "Users", "users", map[string]any{"Users": list, "Me": q.U})
}

func (h *Handler) userSave(q *req) {
	r := q.R
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	role := r.FormValue("role")
	if role != "admin" {
		role = "editor"
	}
	pw := r.FormValue("password")
	var hash string
	if pw != "" {
		var err error
		if hash, err = auth.HashPassword(pw); err != nil {
			q.back("/users", "", err.Error())
			return
		}
	}
	if id == 0 {
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		if !strings.Contains(email, "@") || hash == "" {
			q.back("/users", "", "Give an email and a password (at least 8 characters).")
			return
		}
		if err := store.CreateUser(q.DB, email, hash, role); err != nil {
			q.back("/users", "", "Could not add the user (is the email already used?).")
			return
		}
		q.back("/users", "User added.", "")
		return
	}
	u, err := store.UserByID(q.DB, id)
	if err != nil {
		q.back("/users", "", "User not found.")
		return
	}
	if u.Role == "admin" && role != "admin" && store.CountAdmins(q.DB) <= 1 {
		q.back("/users", "", "The last admin cannot become an editor.")
		return
	}
	if err := store.UpdateUser(q.DB, id, role, hash); err != nil {
		q.fail(err)
		return
	}
	if hash != "" {
		h.App.Auth.DestroyUser(q.Site.Key, "site:"+strconv.FormatInt(id, 10))
	}
	q.back("/users", "User saved.", "")
}

func (h *Handler) userDelete(q *req) {
	u, err := store.UserByID(q.DB, q.id())
	if err != nil {
		q.back("/users", "", "User not found.")
		return
	}
	if u.Role == "admin" && store.CountAdmins(q.DB) <= 1 {
		q.back("/users", "", "The last admin cannot be deleted.")
		return
	}
	_ = store.DeleteUser(q.DB, u.ID)
	h.App.Auth.DestroyUser(q.Site.Key, "site:"+strconv.FormatInt(u.ID, 10))
	q.back("/users", "User deleted.", "")
}
