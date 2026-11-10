package contact

import "testing"

func TestSafeReturn(t *testing.T) {
	for _, ok := range []string{"/", "/contact", "/#git_in_touch", "/de/kontakt?x=1"} {
		if !SafeReturn(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"", "contact", "//evil.com", "/\t/evil.com", "/\n/evil.com", "/\\evil.com", "/ /evil.com", "https://evil.com", "/\x00x"} {
		if SafeReturn(bad) {
			t.Errorf("%q should be blocked", bad)
		}
	}
}
