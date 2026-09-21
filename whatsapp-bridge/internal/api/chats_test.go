package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
)

func chatServer(t *testing.T) *Server {
	t.Helper()
	store, err := database.NewMessageStoreWithDir(t.TempDir())
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
	return &Server{messageStore: store}
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
