package mediapath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	store := t.TempDir()
	mediaRoot := filepath.Join(store, "media")
	chatDir := filepath.Join(mediaRoot, "972500000000")
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		filepath.Join(store, "whatsapp.db"),
		filepath.Join(store, "messages.db"),
		filepath.Join(chatDir, "a.ogg"),
		filepath.Join(chatDir, "b.mp3"),
		filepath.Join(chatDir, "index.json"),
		filepath.Join(chatDir, "c.ogg.part"),
		filepath.Join(chatDir, "p.jpg"),
		filepath.Join(chatDir, "p2.JPEG"),
		filepath.Join(chatDir, "p.png"),
		filepath.Join(chatDir, "p.webp"),
		filepath.Join(chatDir, "p.gif"),
		filepath.Join(chatDir, "v.mp4"),
		filepath.Join(chatDir, "d.pdf"),
		filepath.Join(chatDir, "p.jpg.part"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink inside media/ pointing at the session DB, disguised as audio
	if err := os.Symlink(filepath.Join(store, "whatsapp.db"), filepath.Join(chatDir, "evil.ogg")); err != nil {
		t.Fatal(err)
	}
	// ... and disguised as a photo (ADR 0002 added image extensions)
	if err := os.Symlink(filepath.Join(store, "whatsapp.db"), filepath.Join(chatDir, "evil.jpg")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		subpath string
		ok      bool
		ctype   string
	}{
		{"972500000000/a.ogg", true, "audio/ogg; codecs=opus"},
		{"972500000000/b.mp3", true, "audio/mpeg"},
		{"972500000000/p.jpg", true, "image/jpeg"},
		{"972500000000/p2.JPEG", true, "image/jpeg"},
		{"972500000000/p.png", true, "image/png"},
		{"972500000000/p.webp", true, "image/webp"},
		{"972500000000/p.gif", true, "image/gif"},
		{"972500000000/v.mp4", true, "video/mp4"},
		{"972500000000/d.pdf", false, ""},
		{"972500000000/p.jpg.part", false, ""},
		{"972500000000/evil.jpg", false, ""},
		{"whatsapp.db", false, ""},
		{"../whatsapp.db", false, ""},
		{"../messages.db", false, ""},
		{"972500000000/../../whatsapp.db", false, ""},
		{"/etc/passwd", false, ""},
		{"972500000000/index.json", false, ""},
		{"972500000000/c.ogg.part", false, ""},
		{"972500000000/evil.ogg", false, ""},
		{"972500000000/missing.ogg", false, ""},
		{"972500000000", false, ""},
		{"", false, ""},
	}
	for _, c := range cases {
		_, ctype, ok := Resolve(mediaRoot, c.subpath)
		if ok != c.ok || ctype != c.ctype {
			t.Errorf("Resolve(%q) = (%q, %v), want (%q, %v)", c.subpath, ctype, ok, c.ctype, c.ok)
		}
	}
}
