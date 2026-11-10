// Package captcha is a self-hosted numeric CAPTCHA: an SVG picture of 5 digits drawn as
// seven-segment shapes (no text node a scraper could read) plus a small in-memory store.
//
// It works with cached pages: the form has no server-made value. A tiny inline script
// makes a random id in the browser, loads the picture from "<base>/_captcha?k=<id>" and
// puts the id in the hidden field "captcha_id". The visitor types the digits into
// "captcha". Several CAPTCHAs on one page work, because each has its own id.
// Every answer is single-use and expires after TTL.
package captcha

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// Length is the number of digits.
	Length = 5
	// TTL is how long a picture stays valid.
	TTL = 15 * time.Minute
	// MaxEntries bounds the memory (about 100 bytes each).
	MaxEntries = 20000
	width      = 150
	height     = 50
)

type entry struct {
	code string
	exp  time.Time
}

// Store keeps the answers in memory. A restart forgets them; the visitor then gets
// "type the new numbers" and simply tries again.
type Store struct {
	mu sync.Mutex
	m  map[string]entry
}

// New makes an empty store.
func New() *Store { return &Store{m: map[string]entry{}} }

// ValidID reports whether id looks like an id made by the widget script.
func ValidID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func rnd(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// between returns a random int in [a, b].
func between(a, b int) int { return a + rnd(b-a+1) }

// NewCode stores a new random answer for scope+id and returns it.
func (s *Store) NewCode(scope, id string) string {
	var b strings.Builder
	for i := 0; i < Length; i++ {
		b.WriteByte(byte('0' + rnd(10)))
	}
	code := b.String()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= MaxEntries {
		for k, e := range s.m {
			if now.After(e.exp) {
				delete(s.m, k)
			}
		}
		for k := range s.m { // still full: drop arbitrary entries
			if len(s.m) < MaxEntries*9/10 {
				break
			}
			delete(s.m, k)
		}
	}
	s.m[scope+"|"+id] = entry{code: code, exp: now.Add(TTL)}
	return code
}

// Check compares an answer. The entry is deleted whatever the result (single use).
func (s *Store) Check(scope, id, answer string) bool {
	if !ValidID(id) {
		return false
	}
	answer = Normalize(answer)
	s.mu.Lock()
	e, ok := s.m[scope+"|"+id]
	delete(s.m, scope+"|"+id)
	s.mu.Unlock()
	if !ok || time.Now().After(e.exp) || len(answer) != Length {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(answer), []byte(e.code)) == 1
}

// FromRequest checks the form fields "captcha_id" and "captcha".
func (s *Store) FromRequest(r *http.Request, scope string) bool {
	return s.Check(scope, r.FormValue("captcha_id"), r.FormValue("captcha"))
}

// Code returns the stored answer without using it (for tests).
func (s *Store) Code(scope, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[scope+"|"+id].code
}

// Normalize turns Persian and Arabic-Indic digits into ASCII and drops everything else,
// so the visitor can type with any keyboard.
func Normalize(v string) string {
	var b strings.Builder
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c >= '۰' && c <= '۹':
			b.WriteRune('0' + (c - '۰'))
		case c >= '٠' && c <= '٩':
			b.WriteRune('0' + (c - '٠'))
		}
	}
	return b.String()
}

// ServeImage answers GET <base>/_captcha?k=<id> with a new picture for scope.
func (s *Store) ServeImage(w http.ResponseWriter, r *http.Request, scope string) {
	id := r.URL.Query().Get("k")
	if !ValidID(id) {
		http.Error(w, "bad captcha id", http.StatusBadRequest)
		return
	}
	code := s.NewCode(scope, id)
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml; charset=utf-8")
	h.Set("Cache-Control", "no-store, max-age=0")
	h.Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(SVG(code))
}

// segments lit for each digit, and their boxes inside a 20x34 cell.
var segmentMap = map[byte]string{'0': "abcdef", '1': "bc", '2': "abdeg", '3': "abcdg", '4': "bcfg",
	'5': "acdfg", '6': "acdefg", '7': "abc", '8': "abcdefg", '9': "abcdfg"}

var segments = []struct {
	key        byte
	x, y, w, h int
}{{'a', 3, 0, 14, 4}, {'g', 3, 15, 14, 4}, {'d', 3, 30, 14, 4}, {'f', 0, 2, 4, 14}, {'b', 16, 2, 4, 14},
	{'e', 0, 18, 4, 14}, {'c', 16, 18, 4, 14}}

// SVG draws code with noise dots, curves and a random transform per digit.
func SVG(code string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="captcha">`,
		width, height, width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#f5f6f8"/>`)
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%.1f" fill="#c3c8d0"/>`, rnd(width+1), rnd(height+1), float64(between(3, 11))/10)
	}
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<path d="M0 %d Q %d %d %d %d T %d %d" fill="none" stroke="#ccd2da" stroke-width="%.1f"/>`,
			between(4, height-4), between(30, 70), between(-8, height+8), width/2, between(4, height-4), width, between(4, height-4),
			float64(between(8, 16))/10)
	}
	slot := (width - 24) / Length
	for i := 0; i < len(code); i++ {
		fmt.Fprintf(&b, `<g transform="translate(%d %d) rotate(%d 10 17) skewX(%d) scale(%.2f)" fill="#%02x%02x%02x">`,
			10+i*slot+between(-2, 2), between(4, 10), between(-12, 12), between(-8, 8), float64(between(85, 105))/100,
			between(20, 80), between(20, 80), between(70, 140))
		lit := segmentMap[code[i]]
		for _, sg := range segments {
			if strings.IndexByte(lit, sg.key) >= 0 {
				fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="1.5"/>`, sg.x, sg.y, sg.w, sg.h)
			}
		}
		b.WriteString(`</g>`)
	}
	fmt.Fprintf(&b, `<path d="M0 %d Q %d %d %d %d" fill="none" stroke="#7a8494" stroke-width="1.2" opacity="0.8"/>`,
		between(14, height-14), width/3, rnd(height+1), width, between(14, height-14))
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

// Widget returns the form HTML: picture (click = new picture), hidden id and the input.
// imgURL is "<base>/_captcha". inputClass is added to the text input (for example
// "form-control"); placeholder and title are shown to the visitor.
func Widget(imgURL, inputClass, placeholder, title string) template.HTML {
	esc := template.HTMLEscapeString
	cls := "hk-captcha-input"
	if inputClass != "" {
		cls += " " + inputClass
	}
	return template.HTML(`<span class="hk-captcha" data-src="` + esc(imgURL) + `">` +
		`<img class="hk-captcha-img" width="150" height="50" alt="captcha" title="` + esc(title) + `" style="cursor:pointer;vertical-align:middle">` +
		`<input type="hidden" name="captcha_id" value="">` +
		`<input class="` + esc(cls) + `" type="text" name="captcha" inputmode="numeric" maxlength="5" autocomplete="off" dir="ltr" required placeholder="` + esc(placeholder) + `">` +
		`</span><script>(function(s){var w=s.previousElementSibling,i=w.querySelector("img"),h=w.querySelector("input[type=hidden]"),u=w.getAttribute("data-src");` +
		`function n(){var a=new Uint8Array(12),k="";crypto.getRandomValues(a);for(var j=0;j<a.length;j++)k+=("0"+a[j].toString(16)).slice(-2);h.value=k;i.src=u+"?k="+k;}` +
		`i.addEventListener("click",n);n();})(document.currentScript);</script>`)
}

// Texts returns the default placeholder and title for a language.
func Texts(lang string) (placeholder, title string) {
	if strings.HasPrefix(strings.ToLower(lang), "fa") {
		return "اعداد تصویر", "برای تصویر جدید کلیک کنید"
	}
	return "Numbers in the picture", "Click for a new picture"
}
