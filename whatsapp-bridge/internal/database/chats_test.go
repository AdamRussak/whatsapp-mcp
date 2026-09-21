package database

import (
	"testing"
	"time"
)

// seedChats writes a spread of chats: two groups, two contacts, and one whose name never
// resolved and so reads as a bare LID — which is what the live database actually holds.
func seedChats(t *testing.T) *MessageStore {
	t.Helper()
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	rows := []struct {
		jid  string
		name string
		age  time.Duration
	}{
		{"62156783493249@lid", "14018823594220", 0},                      // unresolved name
		{"120363430762896364@g.us", "Kubecon crew", 1 * time.Hour},       // group
		{"972547669192@s.whatsapp.net", "Dana", 2 * time.Hour},           // contact
		{"972509041042-1555359369@g.us", "Post GoT life", 3 * time.Hour}, // legacy group
		{"972525721988@s.whatsapp.net", "Yossi", 4 * time.Hour},          // contact
	}
	for _, row := range rows {
		if err := store.StoreChat(row.jid, row.name, base.Add(-row.age)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func jids(chats []ChatSummary) []string {
	out := make([]string, len(chats))
	for i, chat := range chats {
		out[i] = chat.JID
	}
	return out
}

func TestListChatsOrdersByMostRecent(t *testing.T) {
	store := seedChats(t)

	chats, err := store.ListChats("", ChatKindAll, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 5 {
		t.Fatalf("got %d chats, want 5", len(chats))
	}
	if chats[0].JID != "62156783493249@lid" {
		t.Fatalf("most recent chat is %s, want the LID chat", chats[0].JID)
	}
	if chats[len(chats)-1].JID != "972525721988@s.whatsapp.net" {
		t.Fatalf("oldest chat is %s, want Yossi", chats[len(chats)-1].JID)
	}
}

func TestListChatsFiltersByKind(t *testing.T) {
	store := seedChats(t)

	groups, err := store.ListChats("", ChatKindGroup, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %v, want the two @g.us chats", jids(groups))
	}
	for _, chat := range groups {
		if !chat.IsGroup {
			t.Errorf("%s came back from the group filter with is_group false", chat.JID)
		}
	}

	// A sender trigger offers 1:1 chats only, so groups must not appear here.
	contacts, err := store.ListChats("", ChatKindContact, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 3 {
		t.Fatalf("got %v, want the three non-group chats", jids(contacts))
	}
	for _, chat := range contacts {
		if chat.IsGroup {
			t.Errorf("%s is a group but came back from the contact filter", chat.JID)
		}
	}
}

func TestListChatsSearch(t *testing.T) {
	store := seedChats(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"by name", "dana", []string{"972547669192@s.whatsapp.net"}},
		{"name is case-insensitive", "KUBECON", []string{"120363430762896364@g.us"}},
		{"by phone number in the JID", "972547669192", []string{"972547669192@s.whatsapp.net"}},
		{"a partial number matches", "9725476", []string{"972547669192@s.whatsapp.net"}},
		// The unresolved chat is findable by the number it displays and by its own JID.
		{"unresolved chat by displayed name", "1401882", []string{"62156783493249@lid"}},
		{"unresolved chat by lid", "621567", []string{"62156783493249@lid"}},
		{"no match", "nobody", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chats, err := store.ListChats(tc.query, ChatKindAll, 50)
			if err != nil {
				t.Fatal(err)
			}
			got := jids(chats)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestListChatsHonoursLimit(t *testing.T) {
	store := seedChats(t)

	chats, err := store.ListChats("", ChatKindAll, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 {
		t.Fatalf("got %d chats, want the limit of 2", len(chats))
	}
	// The limit must keep the newest, not an arbitrary two.
	if chats[0].JID != "62156783493249@lid" {
		t.Fatalf("limited result starts at %s, want the newest chat", chats[0].JID)
	}
}
