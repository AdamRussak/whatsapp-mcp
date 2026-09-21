package whatsapp

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// seedContactStore writes a whatsapp.db shaped like the live one: saved names on the phone
// JID, push names on the LID, and the LID map joining them. The people are the real cases
// from B12, with the user's own LID among them.
func seedContactStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT);
		CREATE TABLE whatsmeow_contacts (
			our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
			push_name TEXT, business_name TEXT, redacted_phone TEXT,
			PRIMARY KEY (our_jid, their_jid)
		);
		INSERT INTO whatsmeow_lid_map VALUES
			('62156783493249',  '972547669192'),  -- Ilan: saved name on the phone JID
			('244332250583270', '972527872233'),  -- Dudi: LID-only chat
			('14018823594220',  '972526996141'),  -- the user themselves
			('111111111111111', '972500000001');  -- known number, no contact at all
		INSERT INTO whatsmeow_contacts (our_jid, their_jid, first_name, full_name, push_name, business_name) VALUES
			('me', '972547669192@s.whatsapp.net', 'אילן', 'אילן וולפסון', 'Ilan Wolfson', ''),
			('me', '62156783493249@lid',          '',     '',             'Ilan Wolfson', ''),
			('me', '972527872233@s.whatsapp.net', '',     'DUDI COHEN',   'DUDI COHEN',   ''),
			('me', '244332250583270@lid',         '',     '',             'DUDI COHEN',   ''),
			('me', '555555555555555@lid',         '',     '',             'Push Only',    ''),
			('me', '777777777777777@lid',         '',     '',             'Chosen Name',  'Acme Plumbing');
	`); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openDirectory(t *testing.T, dir string) *ContactDirectory {
	t.Helper()
	d, err := OpenContactDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPickChatNameOrder(t *testing.T) {
	saved := ContactNames{FullName: "Saved Name", PushName: "Their Choice", BusinessName: "Biz"}
	tests := []struct {
		name       string
		phone, lid ContactNames
		number     string
		jidUser    string
		want       string
	}{
		{"the name you saved wins", saved, ContactNames{PushName: "LID push"}, "972500000000", "123", "Saved Name"},
		{"a saved name on the LID side still counts", ContactNames{}, ContactNames{FullName: "On LID"}, "", "123", "On LID"},
		{"a business's verified name beats a push name", ContactNames{PushName: "p"}, ContactNames{BusinessName: "Acme"}, "", "123", "Acme"},
		{"their chosen name, when nothing is saved", ContactNames{}, ContactNames{PushName: "Their Choice"}, "972500000000", "123", "Their Choice"},
		{"a number before an opaque ID", ContactNames{}, ContactNames{}, "972527872233", "244332250583270", "972527872233"},
		{"the ID only as the last resort", ContactNames{}, ContactNames{}, "", "244332250583270", "244332250583270"},
		{"whitespace is not a name", ContactNames{FullName: "   "}, ContactNames{PushName: "Real"}, "", "123", "Real"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickChatName(tc.phone, tc.lid, tc.number, tc.jidUser); got != tc.want {
				t.Fatalf("PickChatName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNeedsName(t *testing.T) {
	placeholders := []string{"", "   ", "14018823594220", "244332250583270", "972527872233", "+972527872233"}
	for _, name := range placeholders {
		if !NeedsName(name) {
			t.Errorf("NeedsName(%q) = false, want true: it is a placeholder, not a name", name)
		}
	}
	// A real name is left alone — including one that contains digits.
	names := []string{"Dana", "אילן וולפסון", "Group 120363430762896364", "Flat 12B", "Post GoT life - HotD/S02"}
	for _, name := range names {
		if NeedsName(name) {
			t.Errorf("NeedsName(%q) = true, want false: it is a real name", name)
		}
	}
}

func TestIdentifyFixesB12(t *testing.T) {
	d := openDirectory(t, seedContactStore(t))

	stored := map[string]string{
		// The B12 case: a contact's chat named after the user's own LID.
		"62156783493249@lid": "14018823594220",
		// A LID-only chat named after its own ID.
		"244332250583270@lid": "244332250583270",
		// A number is known but nobody ever named them: show the number, not the LID.
		"111111111111111@lid": "111111111111111",
		// Nothing known at all: the ID is all there is.
		"999999999999999@lid": "",
		// Their own chosen name, when no number is mapped.
		"555555555555555@lid": "555555555555555",
		// A real stored name is never overwritten.
		"972525721988@s.whatsapp.net": "Yossi (work)",
		// Groups are never renamed here.
		"120363430762896364@g.us":      "Kubecon crew",
		"972509041042-1555359369@g.us": "",
	}

	got, err := d.Identify(stored)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]ChatIdentity{
		"62156783493249@lid":           {Name: "אילן וולפסון", Phone: "972547669192"},
		"244332250583270@lid":          {Name: "DUDI COHEN", Phone: "972527872233"},
		"111111111111111@lid":          {Name: "972500000001", Phone: "972500000001"},
		"999999999999999@lid":          {Name: "999999999999999", Phone: ""},
		"555555555555555@lid":          {Name: "Push Only", Phone: ""},
		"972525721988@s.whatsapp.net":  {Name: "Yossi (work)", Phone: "972525721988"},
		"120363430762896364@g.us":      {Name: "Kubecon crew", Phone: ""},
		"972509041042-1555359369@g.us": {Name: "", Phone: ""},
	}
	for jid, w := range want {
		if got[jid] != w {
			t.Errorf("%s: got %+v, want %+v", jid, got[jid], w)
		}
	}
}

// Without a whatsapp.db the directory knows nothing, and stored names pass through.
func TestIdentifyWithoutContactStore(t *testing.T) {
	d := openDirectory(t, t.TempDir())

	got, err := d.Identify(map[string]string{"62156783493249@lid": "14018823594220"})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing better is known, so the chat keeps a placeholder — but never a sender's.
	if got["62156783493249@lid"].Name != "62156783493249" {
		t.Fatalf("got %+v", got["62156783493249@lid"])
	}
}

func TestSearchPeople(t *testing.T) {
	d := openDirectory(t, seedContactStore(t))

	contains := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name  string
		query string
		want  []string // each must be present
		empty bool
	}{
		// The reported case: a LID-only chat found by a number neither its ID nor its
		// stored name contains.
		{"full number reaches the LID chat", "972527872233", []string{"244332250583270@lid", "972527872233@s.whatsapp.net"}, false},
		{"formatted number", "+972 52-787-2233", []string{"244332250583270@lid"}, false},
		{"partial number", "27872", []string{"244332250583270@lid"}, false},
		{"saved name, case-insensitive", "dudi", []string{"244332250583270@lid"}, false},
		{"a name in Hebrew", "וולפסון", []string{"62156783493249@lid", "972547669192@s.whatsapp.net"}, false},
		{"a push name", "ilan", []string{"62156783493249@lid"}, false},
		{"a business name", "plumbing", []string{"777777777777777@lid"}, false},
		// Four digits would match half an address book, so numbers need at least five.
		{"too few digits to be a number search", "9725", nil, true},
		{"nobody", "zzzz", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.SearchPeople(tc.query, 200)
			if err != nil {
				t.Fatal(err)
			}
			if tc.empty && len(got) != 0 {
				t.Fatalf("got %v, want nothing", got)
			}
			for _, w := range tc.want {
				if !contains(got, w) {
					t.Fatalf("got %v, missing %s", got, w)
				}
			}
			for _, jid := range got {
				if !strings.HasSuffix(jid, "@lid") && !strings.HasSuffix(jid, "@s.whatsapp.net") {
					t.Fatalf("SearchPeople returned a non-person JID %s", jid)
				}
			}
		})
	}
}
