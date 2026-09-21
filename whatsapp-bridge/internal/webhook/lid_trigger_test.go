package webhook

import (
	"database/sql"
	"path/filepath"
	"testing"

	"whatsapp-bridge/internal/types"

	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
)

// seedLIDMap writes a whatsapp.db holding just the mapping ResolveChatJIDs reads, using
// the real pairing observed live: 972547669192 writes from 62156783493249@lid.
func seedLIDMap(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	db, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)`,
		"62156783493249", "972547669192"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lidMessage(t *testing.T, chatJID, senderJID string) *events.Message {
	t.Helper()
	chat, err := waTypes.ParseJID(chatJID)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := waTypes.ParseJID(senderJID)
	if err != nil {
		t.Fatal(err)
	}
	return &events.Message{
		Info: waTypes.MessageInfo{
			MessageSource: waTypes.MessageSource{Chat: chat, Sender: sender},
			ID:            "M1",
		},
	}
}

// B11: a rule typed as a phone number must match the LID chat the contact actually writes
// from. Before the fix this silently matched nothing.
func TestTriggerTypedAsPhoneMatchesLIDChat(t *testing.T) {
	dir := seedLIDMap(t)

	tests := []struct {
		name         string
		triggerType  string
		triggerValue string
		matchType    string
		chatJID      string
		senderJID    string
		want         bool
	}{
		{"sender as phone, message from LID", "sender", "972547669192", "exact",
			"62156783493249@lid", "62156783493249@lid", true},
		{"chat as phone, message in LID chat", "chat_jid", "972547669192", "exact",
			"62156783493249@lid", "62156783493249@lid", true},
		{"sender as phone, message from the phone JID still matches", "sender", "972547669192", "exact",
			"972547669192@s.whatsapp.net", "972547669192@s.whatsapp.net", true},
		{"sender as the LID itself", "sender", "62156783493249", "exact",
			"62156783493249@lid", "62156783493249@lid", true},
		{"an unrelated contact is not swept in", "sender", "972547669192", "exact",
			"99999999999@lid", "99999999999@lid", false},
		{"group JIDs are unaffected", "chat_jid", "120363430762896364@g.us", "exact",
			"120363430762896364@g.us", "216011219464202@lid", true},
		// Expanding a substring or regex would change what the user wrote into something else.
		{"contains is left alone", "sender", "972547669192", "contains",
			"62156783493249@lid", "62156783493249@lid", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wm := NewManager(nil, waLog.Noop, "", dir)
			config := &types.WebhookConfig{
				ID: 1, Name: "listener", Enabled: true,
				Triggers: []types.WebhookTrigger{{
					ID: 42, TriggerType: tc.triggerType, TriggerValue: tc.triggerValue,
					MatchType: tc.matchType, Enabled: true,
				}},
			}
			wm.configs = []*types.WebhookConfig{config}
			wm.triggerAliases = wm.buildTriggerAliases(wm.configs)

			msg := lidMessage(t, tc.chatJID, tc.senderJID)
			matched := wm.MatchesTriggers(msg, "chat")

			if got := len(matched) == 1; got != tc.want {
				t.Fatalf("matched=%v, want %v (aliases: %v)", got, tc.want, wm.triggerAliases[42])
			}
		})
	}
}

// Without a whatsapp.db there is nothing to expand, and matching must still work as typed.
func TestTriggerAliasesWithoutLIDMap(t *testing.T) {
	wm := NewManager(nil, waLog.Noop, "", t.TempDir())
	config := &types.WebhookConfig{
		ID: 1, Name: "listener", Enabled: true,
		Triggers: []types.WebhookTrigger{{
			ID: 7, TriggerType: "sender", TriggerValue: "972547669192", MatchType: "exact", Enabled: true,
		}},
	}
	wm.configs = []*types.WebhookConfig{config}
	wm.triggerAliases = wm.buildTriggerAliases(wm.configs)

	msg := lidMessage(t, "972547669192@s.whatsapp.net", "972547669192@s.whatsapp.net")
	if len(wm.MatchesTriggers(msg, "chat")) != 1 {
		t.Fatal("a plain phone trigger stopped matching its own phone JID")
	}
}
