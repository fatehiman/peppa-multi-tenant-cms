package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheEvictionAndInvalidate(t *testing.T) {
	c := NewCache(8 << 20)
	body := make([]byte, 1<<20) // 1 MB
	for i := 0; i < 12; i++ {
		c.Put("s1", "/p"+string(rune('a'+i)), "text/html", 200, body)
	}
	size, n := c.Size()
	if size > 8<<20 || n > 8 {
		t.Fatalf("cache over limit: %d bytes, %d entries", size, n)
	}
	if c.Get("s1", "/pa") != nil {
		t.Error("oldest entry was not evicted")
	}
	if c.Get("s1", "/pl") == nil {
		t.Error("newest entry missing")
	}
	c.Put("s2", "/x", "text/html", 200, []byte("x"))
	c.Invalidate("s1")
	if _, n := c.Size(); n != 1 || c.Get("s2", "/x") == nil {
		t.Errorf("invalidate removed the wrong entries (%d left)", n)
	}
	c.TTL = time.Millisecond
	c.Put("s3", "/y", "text/html", 200, []byte("y"))
	time.Sleep(5 * time.Millisecond)
	if c.Get("s3", "/y") != nil {
		t.Error("expired entry returned")
	}
}

func TestParseShortcodes(t *testing.T) {
	codes := ParseShortcodes(`a [[box title="Hello [x]" n=5 flag]] b [[ bad ]] [[c-2 t='it']] [[open title="x"`)
	if len(codes) != 2 {
		t.Fatalf("got %d shortcodes: %+v", len(codes), codes)
	}
	if codes[0].Name != "box" || codes[0].Attrs["title"] != "Hello [x]" || codes[0].Attrs["n"] != "5" || codes[0].Attrs["flag"] != "true" {
		t.Errorf("first: %+v", codes[0])
	}
	if codes[1].Name != "c-2" || codes[1].Attrs["t"] != "it" {
		t.Errorf("second: %+v", codes[1])
	}
}

func TestThemeCheck(t *testing.T) {
	if p, _ := Check(filepath.Join("..", "..", "themes", "default")); len(p) != 0 {
		t.Errorf("default theme problems: %v", p)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pages"), 0o755)
	os.WriteFile(filepath.Join(dir, "layout.html"), []byte(`<html><head></head><body>{{block "content" .}}{{end}}<link href="{{asset "css/missing.css"}}"></body></html>`), 0o644)
	os.WriteFile(filepath.Join(dir, "pages", "default.html"), []byte(`{{define "content"}}x{{end}}`), 0o644)
	p, w := Check(dir)
	joined := strings.Join(p, "|")
	if !strings.Contains(joined, "{{seo}}") || !strings.Contains(joined, "missing.css") {
		t.Errorf("problems: %v", p)
	}
	if !strings.Contains(strings.Join(w, "|"), "bodyEnd") {
		t.Errorf("warnings: %v", w)
	}
	// unknown helper → parse error
	os.WriteFile(filepath.Join(dir, "pages", "default.html"), []byte(`{{define "content"}}{{nosuchhelper}}{{end}}`), 0o644)
	if p, _ := Check(dir); len(p) == 0 || !strings.Contains(p[0], "nosuchhelper") {
		t.Errorf("unknown helper not reported: %v", p)
	}
}

func TestJalaliAndExcerpt(t *testing.T) {
	if y, m, d := ToJalali(2026, 3, 21); y != 1405 || m != 1 || d != 1 {
		t.Errorf("2026-03-21 → %d/%d/%d", y, m, d)
	}
	if got := Excerpt("<p>Hello <b>world</b> [[box a=1]] again</p>", 100); got != "Hello world again" {
		t.Errorf("excerpt: %q", got)
	}
	if got := FaDigits(1405); got != "۱۴۰۵" {
		t.Errorf("fadigits: %q", got)
	}
}
