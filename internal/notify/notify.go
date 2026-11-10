// Package notify sends Telegram messages through the sms.kimiasoft gateway
// (one HTTP GET: <URL>&m=<message>). Any module or core code can use it.
//
// Message format (fixed by the gateway owner): bold title, date and time with zone,
// sender code, then body lines. "_C" is the gateway's line break.
package notify

import (
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// tehran is Iran time (UTC+03:30, no DST since 2022).
var tehran = time.FixedZone("IRST", 3*3600+1800)

// Telegram is a notifier. A zero URL turns sending off.
type Telegram struct {
	URL    string // gateway URL with every parameter except m, e.g. https://sms.kimiasoft.ir/sendMsg?p=…&c=t&r=monitoring
	Server string // server name, first part of the sender code (e.g. waybill7)
	Client *http.Client
	Tries  int           // attempts (default 5)
	Pause  time.Duration // pause between attempts (default 3 s)
}

// Esc escapes a value for Telegram HTML (& < >).
func Esc(v string) string { return html.EscapeString(v) }

// Field makes a body line "<b>label:</b> value" with the value escaped.
func Field(label, value string) string { return "<b>" + Esc(label) + ":</b> " + Esc(value) }

// Format builds the message text. title is plain text (escaped here); body lines are
// HTML (escape inserted values with Esc or Field). New lines inside a body line become
// line breaks.
func Format(title, sender string, at time.Time, body ...string) string {
	clean := func(s string) string { return strings.ReplaceAll(s, "_C", "_ C") }
	parts := []string{"<b>" + clean(Esc(title)) + "</b>", at.In(tehran).Format("2006-01-02 15:04:05") + " IRST", clean(Esc(sender))}
	for _, l := range body {
		l = strings.ReplaceAll(clean(l), "\r\n", "\n")
		parts = append(parts, strings.Split(l, "\n")...)
	}
	msg := strings.Join(parts, "_C")
	if utf8.RuneCountInString(msg) > 3500 {
		msg = string([]rune(msg)[:3490]) + " …"
	}
	return msg
}

// Send formats and sends a message in the background (it never blocks the caller).
// senderTail is added to the server name: "<server>-<senderTail>".
func (t *Telegram) Send(title, senderTail string, body ...string) {
	if t == nil || t.URL == "" {
		return
	}
	sender := senderTail
	if t.Server != "" {
		sender = t.Server + "-" + senderTail
	}
	msg := Format(title, sender, time.Now(), body...)
	go t.deliver(msg)
}

func (t *Telegram) deliver(msg string) {
	c := t.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	tries, pause := t.Tries, t.Pause
	if tries <= 0 {
		tries = 5
	}
	if pause <= 0 {
		pause = 3 * time.Second
	}
	sep := "&"
	if !strings.Contains(t.URL, "?") {
		sep = "?"
	}
	u := t.URL + sep + "m=" + url.QueryEscape(msg)
	var last string
	for i := 0; i < tries; i++ {
		if i > 0 {
			time.Sleep(pause)
		}
		res, err := c.Get(u)
		if err != nil {
			last = err.Error()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if strings.Contains(string(b), "telegramResult=") {
			return
		}
		last = res.Status + " " + strings.TrimSpace(string(b[:min(len(b), 200)]))
	}
	slog.Warn("telegram notify failed", "err", last)
}
