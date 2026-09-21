package whatsapp

import (
	"context"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waTypes "go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// The user's own identity in these tests, matching the live store.
var (
	me    = waTypes.NewJID("972526996141", waTypes.DefaultUserServer)
	meLID = waTypes.NewJID("14018823594220", waTypes.HiddenUserServer)
)

// namingClient builds a client over an in-memory whatsmeow store holding the same shape of
// contact data as the live one: saved names on phone JIDs, push names on LIDs.
func namingClient(t *testing.T) (*Client, *database.MessageStore) {
	t.Helper()
	ctx := context.Background()

	container, err := sqlstore.New(ctx, "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { container.Close() })

	// A device's contact and LID stores only exist once it has an ID and is saved. Saving
	// also reads the pairing identity, which a paired device always has.
	device := container.NewDevice()
	device.ID = &me
	device.LID = meLID
	// Placeholder bytes sized to whatsmeow's schema CHECKs (64/32/64); nothing verifies them.
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{0},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := device.Save(ctx); err != nil {
		t.Fatalf("saving device: %v", err)
	}

	ilanPN := waTypes.NewJID("972547669192", waTypes.DefaultUserServer)
	ilanLID := waTypes.NewJID("62156783493249", waTypes.HiddenUserServer)
	strangerPN := waTypes.NewJID("972500000001", waTypes.DefaultUserServer)
	strangerLID := waTypes.NewJID("111111111111111", waTypes.HiddenUserServer)

	for _, m := range [][2]waTypes.JID{{ilanLID, ilanPN}, {strangerLID, strangerPN}, {meLID, me}} {
		if err := device.LIDs.PutLIDMapping(ctx, m[0], m[1]); err != nil {
			t.Fatalf("PutLIDMapping: %v", err)
		}
	}
	// Your saved name for Ilan lives on his phone JID; his LID carries only his push name.
	//
	// Beware the argument order: whatsmeow's ContactStore interface names the parameters
	// (fullName, firstName), but its SQL implementation is (firstName, fullName), and Go
	// binds by position. Following the interface's labels stores the two swapped — which
	// is exactly what this test did first, and PickChatName then correctly returned the
	// "full" name it was given: the first name.
	if err := device.Contacts.PutContactName(ctx, ilanPN, "אילן", "אילן וולפסון"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := device.Contacts.PutPushName(ctx, ilanLID, "Ilan Wolfson"); err != nil {
		t.Fatal(err)
	}
	pushOnly := waTypes.NewJID("555555555555555", waTypes.HiddenUserServer)
	if _, _, err := device.Contacts.PutPushName(ctx, pushOnly, "Push Only"); err != nil {
		t.Fatal(err)
	}

	messages, err := database.NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { messages.Close() })

	return &Client{Client: whatsmeow.NewClient(device, waLog.Noop), logger: waLog.Noop}, messages
}

func TestGetChatNameB12(t *testing.T) {
	client, messages := namingClient(t)
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	jid := func(s string) waTypes.JID {
		j, err := waTypes.ParseJID(s)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	tests := []struct {
		name   string
		chat   string
		stored string // "" = no row yet
		sender string
		want   string
	}{
		{
			// The live bug: Ilan's chat stored under the user's own LID, and the message
			// being handled is the user's own. Must heal to his saved name.
			name: "a chat named after you heals to the contact's saved name",
			chat: "62156783493249@lid", stored: "14018823594220", sender: meLID.User,
			want: "אילן וולפסון",
		},
		{
			name: "a new chat whose first message is yours is not named after you",
			chat: "62156783493249@lid", sender: meLID.User,
			want: "אילן וולפסון",
		},
		{
			name: "a number beats an opaque LID when nobody named them",
			chat: "111111111111111@lid", sender: meLID.User,
			want: "972500000001",
		},
		{
			name: "their chosen name when no number is mapped",
			chat: "555555555555555@lid", sender: "555555555555555",
			want: "Push Only",
		},
		{
			name: "a real stored name is kept as it is",
			chat: "62156783493249@lid", stored: "Ilan (building committee)", sender: meLID.User,
			want: "Ilan (building committee)",
		},
		{
			name: "the phone-JID chat finds the same saved name",
			chat: "972547669192@s.whatsapp.net", sender: "972547669192",
			want: "אילן וולפסון",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := messages.GetDB().Exec(`DELETE FROM chats WHERE jid = ?`, tc.chat); err != nil {
				t.Fatal(err)
			}
			if tc.stored != "" {
				if err := messages.StoreChat(tc.chat, tc.stored, now); err != nil {
					t.Fatal(err)
				}
			}

			got := client.GetChatName(messages, jid(tc.chat), tc.chat, nil, tc.sender)
			if got != tc.want {
				t.Fatalf("GetChatName() = %q, want %q", got, tc.want)
			}
			if got == meLID.User || got == me.User {
				t.Fatalf("chat %s was named after the user", tc.chat)
			}
		})
	}
}
