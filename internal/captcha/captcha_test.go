package captcha

import (
	"strings"
	"testing"
)

func TestNormalizeAndCheck(t *testing.T) {
	if got := Normalize("۱۲ 3٤5x"); got != "12345" {
		t.Errorf("Normalize: %q", got)
	}
	s := New()
	code := s.NewCode("site:a", "abcdefgh1")
	fa := strings.Map(func(r rune) rune { return '۰' + (r - '0') }, code)
	if !s.Check("site:a", "abcdefgh1", fa) {
		t.Error("Persian digits not accepted")
	}
	if s.Check("site:a", "abcdefgh1", code) {
		t.Error("answer used twice")
	}
	code = s.NewCode("site:a", "abcdefgh2")
	if s.Check("site:b", "abcdefgh2", code) {
		t.Error("answer accepted in another scope")
	}
	if ValidID("short") || ValidID("bad id with spaces") || !ValidID("0a1b2c3d4e5f") {
		t.Error("ValidID")
	}
	if svg := string(SVG("01234")); !strings.HasPrefix(svg, "<svg") || strings.Contains(svg, ">0<") {
		t.Error("SVG must draw shapes, not text")
	}
}
