package tenant

import "testing"

func TestParseHosts(t *testing.T) {
	h, err := ParseHosts("Example.com\nhttps://shop.example.com/\nexample.com/de\n\nexample.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].String() != "example.com" || !h[0].Primary || h[2].Prefix != "/de" || h[1].Primary {
		t.Errorf("hosts: %+v", h)
	}
	for _, bad := range []string{"exa mple.com", "example.com/DE?", "example.com/admin", "example.com/_m", "-x.com", "www.example.com", "https://WWW.example.com/de"} {
		if _, err := ParseHosts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{"A.com:8080": "a.com", "a.com.": "a.com", "[::1]:80": "::1"} {
		if got := NormalizeHost(in); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
}

func TestLookupLongestPrefix(t *testing.T) {
	root := &Site{Key: "root", Hosts: []Host{{Domain: "d.com", Primary: true}}}
	de := &Site{Key: "de", Hosts: []Host{{Domain: "d.com", Prefix: "/de", Primary: true}}}
	at := &Site{Key: "at", Hosts: []Host{{Domain: "d.com", Prefix: "/de/at", Primary: true}}}
	r := &Registry{byHost: map[string]*Site{"d.com": root, "d.com/de": de, "d.com/de/at": at}, domains: map[string]bool{"d.com": true}}
	cases := map[string][2]string{
		"/":           {"root", "/"},
		"/design":     {"root", "/design"},
		"/de":         {"de", "/"},
		"/de/kontakt": {"de", "/kontakt"},
		"/DE/x":       {"de", "/x"},
		"/de/at/y":    {"at", "/y"},
		"/de/atlas":   {"de", "/atlas"},
	}
	for path, want := range cases {
		m := r.Lookup("D.com:443", path)
		if m.Site == nil || m.Site.Key != want[0] || m.Rest != want[1] {
			t.Errorf("%s → %+v, want %v", path, m, want)
		}
	}
	if m := r.Lookup("www.d.com", "/de/x"); !m.WWW || m.Site.Key != "de" {
		t.Errorf("www: %+v", m)
	}
	if m := r.Lookup("other.com", "/"); !m.Unknown {
		t.Error("unknown host matched")
	}
}
