// Package contact is a contact form: honeypot, CSRF token, CAPTCHA, per-IP rate limit,
// saves submissions in module_data, sends a Telegram message to the monitoring group
// and optionally an email by SMTP.
//
// Form fields: name, email, phone, subject, message; hidden: _csrf ({{csrf}}),
// website (honeypot, must stay empty), _return (optional site path to go back to);
// CAPTCHA: {{captcha}} (fields captcha_id + captcha). Every form that posts to the
// contact page needs {{captcha}}, unless the site setting contact_captcha = 0.
// Site settings for mail: smtp_host (host:port), smtp_user, smtp_pass, smtp_from, contact_to.
// Site setting contact_telegram = 0 turns the Telegram message off for one site.
package contact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"hostkit/internal/auth"
	"hostkit/internal/module"
	"hostkit/internal/notify"
	"hostkit/internal/tenant"
)

func init() { module.Register(New()) }

// Contact is the module.
type Contact struct{ limit *auth.Limiter }

// New makes the module: 5 messages per IP per 10 minutes.
func New() *Contact { return &Contact{limit: auth.NewLimiter(5, 10*time.Minute)} }

// Name implements module.Module.
func (c *Contact) Name() string { return "contact" }

// Submission is one stored message.
type Submission struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	IP      string `json:"ip"`
	Page    string `json:"page"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n])
	}
	return s
}

// SafeReturn reports whether v is a safe local path to send the visitor back to.
// It blocks "//evil.com", "/\evil.com" and control characters such as a tab:
// browsers delete tabs and new lines from a URL, so "/<TAB>/evil.com" would become
// "//evil.com" (an open redirect to another site).
func SafeReturn(v string) bool {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || len(v) > 500 {
		return false
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f || c == '\\' || unicode.IsSpace(c) {
			return false
		}
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "" && u.Host == "" && !strings.HasPrefix(u.Path, "//")
}

// msg picks the Persian text on Persian sites.
func msg(ctx *module.Ctx, en, fa string) string {
	if strings.HasPrefix(strings.ToLower(ctx.Site.Lang), "fa") {
		return fa
	}
	return en
}

// Handle implements module.Module.
func (c *Contact) Handle(ctx *module.Ctx) (any, bool, error) {
	r := ctx.R
	data := map[string]any{"Sent": r.URL.Query().Get("sent") == "1"}
	if r.Method != http.MethodPost {
		return data, false, nil
	}
	ret := ctx.URL("/" + ctx.Page.Slug)
	if v := r.FormValue("_return"); SafeReturn(v) {
		ret = ctx.URL(v)
	}
	done := func() (any, bool, error) {
		sep := "?"
		if strings.Contains(ret, "?") {
			sep = "&"
		}
		anchor := ""
		if i := strings.Index(ret, "#"); i >= 0 {
			ret, anchor = ret[:i], ret[i:]
		}
		http.Redirect(ctx.W, r, ret+sep+"sent=1"+anchor, http.StatusSeeOther)
		return nil, true, nil
	}
	sub := Submission{Name: clip(r.FormValue("name"), 200), Email: clip(r.FormValue("email"), 200),
		Phone: clip(r.FormValue("phone"), 50), Subject: clip(r.FormValue("subject"), 300),
		Message: clip(r.FormValue("message"), 5000), IP: ctx.ClientIP, Page: ctx.Path}
	data["Values"] = sub
	if r.FormValue("website") != "" { // bots fill the hidden field: act as if it worked
		return done()
	}
	if !ctx.CSRFValid() {
		data["Error"] = msg(ctx, "The form has expired. Please send it again.", "فرم منقضی شده است. لطفاً دوباره ارسال کنید.")
		return data, false, nil
	}
	if sub.Message == "" || (sub.Email == "" && sub.Phone == "") {
		data["Error"] = msg(ctx, "Please write a message and an email or phone number.", "لطفاً پیام و ایمیل یا شماره تلفن خود را بنویسید.")
		return data, false, nil
	}
	if ctx.Settings["contact_captcha"] != "0" && (ctx.CaptchaValid == nil || !ctx.CaptchaValid()) {
		data["Error"] = msg(ctx, "The numbers of the picture are not correct. Please type the new numbers.", "اعداد تصویر درست وارد نشده است. لطفاً اعداد تصویر جدید را وارد کنید.")
		return data, false, nil
	}
	key := ctx.Site.Key + "|" + ctx.ClientIP
	if !c.limit.Allow(key) {
		data["Error"] = msg(ctx, "Too many messages. Please try again later.", "تعداد پیام‌ها زیاد است. لطفاً بعداً تلاش کنید.")
		return data, false, nil
	}
	c.limit.Add(key)
	if _, err := ctx.Store.Put("submission", sub); err != nil {
		return nil, false, err
	}
	sendMail(ctx.Settings, ctx.Site.Name, sub)
	if ctx.Notify != nil && ctx.Settings["contact_telegram"] != "0" {
		body := []string{notify.Field("Site", ctx.Site.Domain+ctx.Site.BasePath), notify.Field("Name", sub.Name),
			notify.Field("Email", sub.Email), notify.Field("Phone", sub.Phone), notify.Field("Subject", sub.Subject),
			notify.Field("Page", sub.Page), notify.Field("IP", sub.IP), "<b>Message:</b>", notify.Esc(clip(sub.Message, 2500)),
			notify.Esc("https://" + ctx.Site.Domain + ctx.Site.BasePath + tenant.AdminPath + "/modules/contact/submissions")}
		ctx.Notify("New contact message", body...)
	}
	return done()
}

func sendMail(set map[string]string, siteName string, s Submission) {
	host, to := set["smtp_host"], set["contact_to"]
	if host == "" || to == "" {
		return
	}
	from := set["smtp_from"]
	if from == "" {
		from = to
	}
	go func() {
		var body bytes.Buffer
		fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n",
			from, to, b64("["+siteName+"] "+s.Subject))
		fmt.Fprintf(&body, "Name: %s\r\nEmail: %s\r\nPhone: %s\r\nPage: %s\r\nIP: %s\r\n\r\n%s\r\n", s.Name, s.Email, s.Phone, s.Page, s.IP, s.Message)
		var a smtp.Auth
		if u := set["smtp_user"]; u != "" {
			h, _, _ := net.SplitHostPort(host)
			a = smtp.PlainAuth("", u, set["smtp_pass"], h)
		}
		if err := smtp.SendMail(host, a, from, strings.Split(to, ","), body.Bytes()); err != nil {
			slog.Warn("contact mail failed", "err", err)
		}
	}()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Admin implements module.Module: a list of submissions.
func (c *Contact) Admin() []module.AdminScreen {
	return []module.AdminScreen{{Key: "submissions", Title: "Contact form submissions", Handle: c.adminList}}
}

var listTmpl = template.Must(template.New("l").Parse(`
<table><tr><th>Date</th><th>From</th><th>Message</th><th></th></tr>
{{range .Rows}}<tr><td class="muted" style="white-space:nowrap">{{.Date}}</td>
<td>{{.S.Name}}<div class="hint">{{.S.Email}} {{.S.Phone}}</div><div class="hint">{{.S.Page}} · {{.S.IP}}</div></td>
<td><b>{{.S.Subject}}</b><div style="white-space:pre-wrap">{{.S.Message}}</div></td>
<td class="act"><form method="post" action="{{$.URL}}" onsubmit="return confirm('Delete?')">{{$.CSRF}}<input type="hidden" name="delete" value="{{.ID}}">
<button class="btn icon tone-red" type="submit" title="Delete" aria-label="Delete"><svg class="ic" viewBox="0 0 24 24"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/></svg></button></form></td></tr>
{{else}}<tr><td colspan="4" class="muted">No messages yet.</td></tr>{{end}}</table>
<div class="actions">{{if .Prev}}<a class="btn tone-gray" href="{{.URL}}?p={{.Prev}}">Newer</a>{{end}}{{if .Next}}<a class="btn tone-gray" href="{{.URL}}?p={{.Next}}">Older</a>{{end}}</div>`))

func (c *Contact) adminList(a *module.AdminCtx) (template.HTML, error) {
	if a.R.Method == http.MethodPost {
		if id, _ := strconv.ParseInt(a.R.FormValue("delete"), 10, 64); id != 0 {
			_ = a.Store.Delete(id)
		}
		http.Redirect(a.W, a.R, a.BaseURL, http.StatusSeeOther)
		a.Done = true
		return "", nil
	}
	page, _ := strconv.Atoi(a.R.URL.Query().Get("p"))
	if page < 1 {
		page = 1
	}
	const per = 50
	recs, err := a.Store.List("submission", per+1, (page-1)*per)
	if err != nil {
		return "", err
	}
	type row struct {
		ID   int64
		Date string
		S    Submission
	}
	var rows []row
	for i, r := range recs {
		if i == per {
			break
		}
		var s Submission
		_ = json.Unmarshal([]byte(r.ValueJSON), &s)
		rows = append(rows, row{ID: r.ID, Date: time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"), S: s})
	}
	d := map[string]any{"Rows": rows, "URL": a.BaseURL, "CSRF": a.CSRFField}
	if page > 1 {
		d["Prev"] = page - 1
	}
	if len(recs) > per {
		d["Next"] = page + 1
	}
	var b bytes.Buffer
	err = listTmpl.Execute(&b, d)
	return template.HTML(b.String()), err
}
