package siteadmin

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestCheckUpload(t *testing.T) {
	small := pngOf(100, 50)
	if mime, w, _, err := CheckUpload("a.png", small[:64], bytes.NewReader(small[64:])); err != nil || mime != "image/png" || w != 100 {
		t.Errorf("small png: %v %s %d", err, mime, w)
	}
	big := pngOf(2100, 10)
	if _, _, _, err := CheckUpload("big.png", big[:64], bytes.NewReader(big[64:])); err == nil {
		t.Error("image wider than 2000 px accepted")
	}
	if _, _, _, err := CheckUpload("x.php", []byte("<?php echo 1;"), bytes.NewReader(nil)); err == nil {
		t.Error(".php accepted")
	}
	if _, _, _, err := CheckUpload("x.svg", []byte("<svg onload=alert(1)>"), bytes.NewReader(nil)); err == nil {
		t.Error(".svg accepted")
	}
	if _, _, _, err := CheckUpload("fake.jpg", []byte("<html><script>"), bytes.NewReader(nil)); err == nil {
		t.Error("html renamed to .jpg accepted")
	}
	if _, _, _, err := CheckUpload("doc.pdf", []byte("%PDF-1.4 test"), bytes.NewReader(nil)); err != nil {
		t.Errorf("pdf: %v", err)
	}
}

func TestCleanSlug(t *testing.T) {
	ok := map[string]string{" /About Us/ ": "about-us", "news/First": "news/first", "درباره-ما": "درباره-ما", "": ""}
	for in, want := range ok {
		if got, err := CleanSlug(in); err != nil || got != want {
			t.Errorf("CleanSlug(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"admin/x", "a//b", "a?b", "../x", "_m/x", "sitemap.xml"} {
		if _, err := CleanSlug(bad); err == nil {
			t.Errorf("CleanSlug(%q) accepted", bad)
		}
	}
}
