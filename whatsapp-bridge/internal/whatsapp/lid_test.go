package whatsapp

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// writeLIDMap creates <dir>/whatsapp.db with whatsmeow's lid map schema and the given lid→pn rows
func writeLIDMap(t *testing.T, dir string, rows map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for lid, pn := range rows {
		if _, err := db.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`, lid, pn); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveChatJIDs(t *testing.T) {
	dir := t.TempDir()
	writeLIDMap(t, dir, map[string]string{"18674685612157": "972523996302"})

	both := []string{"18674685612157@lid", "972523996302@s.whatsapp.net"}
	cases := []struct {
		input string
		want  []string
	}{
		{"972523996302", both},                                                     // plain phone number
		{"+972-52-399-6302", both},                                                 // formatted phone number
		{"972523996302@s.whatsapp.net", both},                                      // phone JID
		{"972523996302:12@s.whatsapp.net", both},                                   // device suffix is dropped
		{"18674685612157@lid", both},                                               // LID resolves back to the phone JID
		{"  18674685612157@lid ", both},                                            // surrounding whitespace
		{"244332250583270@lid", []string{"244332250583270@lid"}},                   // unmapped LID
		{"972500000000", []string{"972500000000@s.whatsapp.net"}},                  // unmapped phone
		{"120363239970576980@g.us", []string{"120363239970576980@g.us"}},           // group: itself only
		{"status@broadcast", []string{"status@broadcast"}},                         // broadcast: itself only
		{"972546740356-1603022062@g.us", []string{"972546740356-1603022062@g.us"}}, // old-style group
	}
	for _, c := range cases {
		got, err := ResolveChatJIDs(c.input, dir)
		if err != nil {
			t.Errorf("ResolveChatJIDs(%q): %v", c.input, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ResolveChatJIDs(%q) = %v, want %v", c.input, got, c.want)
		}
	}

	for _, bad := range []string{"", "   ", "@lid"} {
		if _, err := ResolveChatJIDs(bad, dir); err == nil {
			t.Errorf("ResolveChatJIDs(%q): expected error", bad)
		}
	}
}

func TestResolveChatJIDsWithoutSessionDB(t *testing.T) {
	// No whatsapp.db: only the input's own JID comes back, and the file is not created
	dir := t.TempDir()
	got, err := ResolveChatJIDs("18674685612157@lid", dir)
	if err != nil || !reflect.DeepEqual(got, []string{"18674685612157@lid"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*")); len(matches) != 0 {
		t.Errorf("ResolveChatJIDs created files: %v", matches)
	}
}
