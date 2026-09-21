package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
)

func chatServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	store, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	// More chats than the default page, so the limit is actually exercised.
	for i := 0; i < 60; i++ {
		jid := fmt.Sprintf("97250%06d@s.whatsapp.net", i)
		if err := store.StoreChat(jid, "Contact", base.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StoreChat("120363430762896364@g.us", "Kubecon crew", base); err != nil {
		t.Fatal(err)
	}
	// storeDir is always a temp dir: left empty it would default to ./store, and a unit
	// test must never be able to reach a real store (§4.5).
	return &Server{messageStore: store, storeDir: dir}
}

func getChats(t *testing.T, server *Server, query string) (*httptest.ResponseRecorder, []database.ChatSummary) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats"+query, nil)
	rec := httptest.NewRecorder()
	server.handleChats(rec, req)

	var body struct {
		Success bool                   `json:"success"`
		Data    []database.ChatSummary `json:"data"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
	return rec, body.Data
}

func TestHandleChatsDefaultsAndCap(t *testing.T) {
	server := chatServer(t)

	// No limit given: the default page, not everything.
	rec, chats := getChats(t, server, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(chats) != defaultChatLimit {
		t.Fatalf("got %d chats, want the default %d", len(chats), defaultChatLimit)
	}

	// An absurd limit is capped rather than honoured, so one request cannot pull the
	// whole address book in a single response.
	_, capped := getChats(t, server, "?limit=100000")
	if len(capped) > maxChatLimit {
		t.Fatalf("got %d chats, want at most the cap of %d", len(capped), maxChatLimit)
	}
}

func TestHandleChatsValidation(t *testing.T) {
	server := chatServer(t)

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"unknown type is rejected", "?type=banana", http.StatusBadRequest},
		{"limit must be a number", "?limit=lots", http.StatusBadRequest},
		{"limit must be positive", "?limit=0", http.StatusBadRequest},
		{"negative limit is rejected", "?limit=-5", http.StatusBadRequest},
		{"explicit all is fine", "?type=all", http.StatusOK},
		{"contact is fine", "?type=contact", http.StatusOK},
		{"group is fine", "?type=group", http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := getChats(t, server, tc.query)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandleChatsGroupFilterAndSearch(t *testing.T) {
	server := chatServer(t)

	_, groups := getChats(t, server, "?type=group")
	if len(groups) != 1 || !groups[0].IsGroup || groups[0].JID != "120363430762896364@g.us" {
		t.Fatalf("group filter returned %+v", groups)
	}

	_, found := getChats(t, server, "?q=kubecon")
	if len(found) != 1 || found[0].Name != "Kubecon crew" {
		t.Fatalf("search returned %+v", found)
	}
}

func TestHandleChatsRejectsNonGET(t *testing.T) {
	server := chatServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/chats", nil)
	rec := httptest.NewRecorder()
	server.handleChats(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// writeContactStore gives a test store WhatsApp's contact tables, shaped like the live ones:
// the saved name on the phone JID, the push name on the LID.
func writeContactStore(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT);
		CREATE TABLE IF NOT EXISTS whatsmeow_contacts (
			our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
			push_name TEXT, business_name TEXT, redacted_phone TEXT,
			PRIMARY KEY (our_jid, their_jid));
		INSERT OR IGNORE INTO whatsmeow_lid_map VALUES
			('244332250583270', '972527872233'),
			('62156783493249',  '972547669192');
		INSERT OR IGNORE INTO whatsmeow_contacts (our_jid, their_jid, full_name, push_name) VALUES
			('me', '972527872233@s.whatsapp.net', 'DUDI COHEN',   'DUDI COHEN'),
			('me', '244332250583270@lid',         '',             'DUDI COHEN'),
			('me', '972547669192@s.whatsapp.net', 'אילן וולפסון', 'Ilan Wolfson');
	`); err != nil {
		t.Fatal(err)
	}
}

// The reported case: 972527872233 has only a LID chat, named after its own LID. Searching
// the number — or the name — must find it, under its real name, with the number shown.
func TestHandleChatsFindsLIDOnlyContact(t *testing.T) {
	server := chatServer(t)
	writeContactStore(t, server.storeDir)
	if err := server.messageStore.StoreChat("244332250583270@lid", "244332250583270",
		time.Date(2026, 9, 18, 10, 6, 11, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"972527872233", "dudi"} {
		_, chats := getChats(t, server, "?q="+q)
		if len(chats) != 1 {
			t.Fatalf("q=%s: got %d chats %+v, want the one LID chat", q, len(chats), chats)
		}
		if chats[0].JID != "244332250583270@lid" || chats[0].Name != "DUDI COHEN" || chats[0].Phone != "972527872233" {
			t.Fatalf("q=%s: got %+v, want DUDI COHEN at 244332250583270@lid with his number", q, chats[0])
		}
	}
}

// B12: a chat stored under the user's own LID is shown under the contact's real name.
func TestHandleChatsRepairsNameAtReadTime(t *testing.T) {
	server := chatServer(t)
	writeContactStore(t, server.storeDir)
	if err := server.messageStore.StoreChat("62156783493249@lid", "14018823594220",
		time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	_, chats := getChats(t, server, "?type=contact&limit=1")
	if chats[0].JID != "62156783493249@lid" || chats[0].Name != "אילן וולפסון" {
		t.Fatalf("got %+v, want the chat shown as אילן וולפסון", chats[0])
	}

	// Display only: the stored row is untouched (§4.3).
	var stored string
	if err := server.messageStore.GetDB().QueryRow(
		`SELECT name FROM chats WHERE jid = ?`, "62156783493249@lid").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "14018823594220" {
		t.Fatalf("stored name changed to %q; the API must not write chats", stored)
	}
}

// A contact store that cannot be read must not take the picker down with it.
func TestHandleChatsSurvivesBrokenContactStore(t *testing.T) {
	server := chatServer(t)
	// A whatsapp.db with none of whatsmeow's tables: every contact query fails.
	if err := os.WriteFile(filepath.Join(server.storeDir, "whatsapp.db"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	rec, chats := getChats(t, server, "?q=Kubecon")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(chats) != 1 || chats[0].Name != "Kubecon crew" {
		t.Fatalf("text search should still answer with stored names, got %+v", chats)
	}
}
