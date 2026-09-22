package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

const (
	chatA  = "111111111111@s.whatsapp.net"
	chatB  = "222222222222@s.whatsapp.net"
	family = "120363430762896364@g.us"
	dana   = "333333333333@s.whatsapp.net"
	other  = "444444444444@s.whatsapp.net"
)

func text(s string) *waProto.Message { return &waProto.Message{Conversation: proto.String(s)} }

func audio() *waProto.Message {
	return &waProto.Message{AudioMessage: &waProto.AudioMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/x.enc"), PTT: proto.Bool(true),
	}}
}

func image() *waProto.Message {
	return &waProto.Message{ImageMessage: &waProto.ImageMessage{URL: proto.String("https://mmg.whatsapp.net/v/i.enc")}}
}

func message(t *testing.T, chatJID, senderJID string, content *waProto.Message) *events.Message {
	t.Helper()
	msg := lidMessage(t, chatJID, senderJID)
	msg.Message = content
	return msg
}

func trig(triggerType, value, matchType string) types.WebhookTrigger {
	return types.WebhookTrigger{TriggerType: triggerType, TriggerValue: value, MatchType: matchType, Enabled: true}
}

// modeManager loads one listener with the given mode and triggers, IDs numbered in order.
func modeManager(storeDir, mode string, triggers ...types.WebhookTrigger) *Manager {
	for i := range triggers {
		triggers[i].ID = i + 1
	}
	wm := NewManager(nil, waLog.Noop, "", storeDir)
	wm.configs = []*types.WebhookConfig{{ID: 1, Name: "listener", Enabled: true, TriggerMode: mode, Triggers: triggers}}
	wm.triggerAliases = wm.buildTriggerAliases(wm.configs)
	return wm
}

type probe struct {
	name    string
	chat    string
	sender  string
	content *waProto.Message
	orWant  bool
	andWant bool
}

// One test per row of ADR 0001's worked-examples table. The OR column is today's behaviour
// and must not change, including "audio from anywhere, plus everything in A".
func TestWorkedExamples(t *testing.T) {
	lidDir := seedLIDMap(t)

	rows := []struct {
		row      string
		storeDir string
		triggers []types.WebhookTrigger
		probes   []probe
	}{
		{
			row:      "chat = A, media = audio",
			triggers: []types.WebhookTrigger{trig("chat_jid", chatA, "exact"), trig("media_type", "audio", "exact")},
			probes: []probe{
				{name: "audio in A", chat: chatA, sender: chatA, content: audio(), orWant: true, andWant: true},
				{name: "text in A", chat: chatA, sender: chatA, content: text("hi"), orWant: true, andWant: false},
				{name: "audio in B", chat: chatB, sender: chatB, content: audio(), orWant: true, andWant: false},
				{name: "text in B", chat: chatB, sender: chatB, content: text("hi"), orWant: false, andWant: false},
			},
		},
		{
			row:      "chat = A, chat = B",
			triggers: []types.WebhookTrigger{trig("chat_jid", chatA, "exact"), trig("chat_jid", chatB, "exact")},
			probes: []probe{
				// Under AND the validator rejects this (R3); the matcher can only ever say no.
				{name: "in A", chat: chatA, sender: chatA, content: text("hi"), orWant: true, andWant: false},
				{name: "in B", chat: chatB, sender: chatB, content: text("hi"), orWant: true, andWant: false},
				{name: "elsewhere", chat: other, sender: other, content: text("hi"), orWant: false, andWant: false},
			},
		},
		{
			row:      "chat = phone, chat = its LID",
			storeDir: lidDir,
			triggers: []types.WebhookTrigger{trig("chat_jid", "972547669192", "exact"), trig("chat_jid", "62156783493249@lid", "exact")},
			probes: []probe{
				{name: "the LID chat", chat: "62156783493249@lid", sender: "62156783493249@lid", content: text("hi"), orWant: true, andWant: true},
				{name: "the phone chat", chat: "972547669192@s.whatsapp.net", sender: "972547669192@s.whatsapp.net", content: text("hi"), orWant: true, andWant: true},
				{name: "someone else", chat: other, sender: other, content: text("hi"), orWant: false, andWant: false},
			},
		},
		{
			row:      "sender = Dana, chat = family",
			triggers: []types.WebhookTrigger{trig("sender", dana, "exact"), trig("chat_jid", family, "exact")},
			probes: []probe{
				{name: "Dana in family", chat: family, sender: dana, content: text("hi"), orWant: true, andWant: true},
				{name: "Dana elsewhere", chat: dana, sender: dana, content: text("hi"), orWant: true, andWant: false},
				{name: "someone else in family", chat: family, sender: other, content: text("hi"), orWant: true, andWant: false},
			},
		},
		{
			row:      "keyword = invoice, keyword = overdue",
			triggers: []types.WebhookTrigger{trig("keyword", "invoice", "contains"), trig("keyword", "overdue", "contains")},
			probes: []probe{
				{name: "both words", chat: chatA, sender: chatA, content: text("Invoice 12 is overdue"), orWant: true, andWant: true},
				{name: "one word", chat: chatA, sender: chatA, content: text("invoice attached"), orWant: true, andWant: false},
				{name: "neither", chat: chatA, sender: chatA, content: text("hello"), orWant: false, andWant: false},
			},
		},
		{
			row:      "keyword = urgent, media = audio",
			triggers: []types.WebhookTrigger{trig("keyword", "urgent", "contains"), trig("media_type", "audio", "exact")},
			probes: []probe{
				// Rejected under AND (R6): no message has both.
				{name: "urgent text", chat: chatA, sender: chatA, content: text("urgent!"), orWant: true, andWant: false},
				{name: "audio", chat: chatA, sender: chatA, content: audio(), orWant: true, andWant: false},
			},
		},
		{
			row:      "media = sticker",
			triggers: []types.WebhookTrigger{trig("media_type", "sticker", "exact")},
			probes: []probe{
				// Stickers are never recognised, so R1 rejects this in both modes.
				{name: "sticker", chat: chatA, sender: chatA, content: &waProto.Message{StickerMessage: &waProto.StickerMessage{}}, orWant: false, andWant: false},
				{name: "image", chat: chatA, sender: chatA, content: image(), orWant: false, andWant: false},
			},
		},
		{
			row:      "all, media = audio",
			triggers: []types.WebhookTrigger{trig("all", "", "exact"), trig("media_type", "audio", "exact")},
			probes: []probe{
				{name: "text", chat: chatA, sender: chatA, content: text("hi"), orWant: true, andWant: false},
				{name: "image", chat: chatA, sender: chatA, content: image(), orWant: true, andWant: false},
				{name: "audio", chat: chatA, sender: chatA, content: audio(), orWant: true, andWant: true},
			},
		},
	}

	for _, row := range rows {
		for _, p := range row.probes {
			for _, mode := range []string{"or", "and"} {
				want := p.orWant
				if mode == "and" {
					want = p.andWant
				}
				t.Run(row.row+"/"+mode+"/"+p.name, func(t *testing.T) {
					triggers := append([]types.WebhookTrigger(nil), row.triggers...)
					wm := modeManager(row.storeDir, mode, triggers...)
					got := len(wm.MatchesTriggers(message(t, p.chat, p.sender, p.content), "chat")) == 1
					if got != want {
						t.Fatalf("matched = %v, want %v", got, want)
					}
				})
			}
		}
	}
}

// An empty trigger_mode is OR: listeners saved before v5 keep their meaning.
func TestEmptyModeIsOr(t *testing.T) {
	wm := modeManager("", "", trig("chat_jid", chatA, "exact"), trig("media_type", "audio", "exact"))
	if len(wm.MatchesTriggers(message(t, chatB, chatB, audio()), "chat")) != 1 {
		t.Fatal("an empty mode did not behave as OR")
	}
}

func TestDisabledTriggersAreSkipped(t *testing.T) {
	disabled := trig("media_type", "audio", "exact")
	disabled.Enabled = false

	for _, mode := range []string{"or", "and"} {
		t.Run(mode, func(t *testing.T) {
			wm := modeManager("", mode, trig("chat_jid", chatA, "exact"), disabled)
			// Under AND a disabled audio trigger adds no requirement: text in A fires.
			if len(wm.MatchesTriggers(message(t, chatA, chatA, text("hi")), "chat")) != 1 {
				t.Fatal("text in A did not fire")
			}
			// Under OR a disabled audio trigger does not fire for audio elsewhere.
			if len(wm.MatchesTriggers(message(t, chatB, chatB, audio()), "chat")) != 0 {
				t.Fatal("a disabled trigger fired")
			}
		})
	}
}

func TestListenerWithNoEnabledTriggersMatchesNothing(t *testing.T) {
	disabled := trig("all", "", "exact")
	disabled.Enabled = false

	for _, mode := range []string{"or", "and"} {
		for name, triggers := range map[string][]types.WebhookTrigger{
			"no triggers":           nil,
			"only disabled trigger": {disabled},
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				wm := modeManager("", mode, triggers...)
				if len(wm.MatchesTriggers(message(t, chatA, chatA, text("hi")), "chat")) != 0 {
					t.Fatal("a listener with no enabled triggers fired")
				}
			})
		}
	}
}

// matchingTriggers is the list the payload is built from: one entry under OR, every enabled
// trigger under AND, in trigger order.
func TestMatchingTriggersList(t *testing.T) {
	msg := message(t, chatA, chatA, audio())

	wm := modeManager("", "or", trig("keyword", "x", "exact"), trig("media_type", "audio", "exact"), trig("chat_jid", chatA, "exact"))
	got := wm.matchingTriggers(wm.configs[0], msg, "", "audio", "chat")
	if len(got) != 1 || got[0].TriggerType != "media_type" {
		t.Fatalf("OR list = %+v, want just the media_type trigger", got)
	}

	wm = modeManager("", "and", trig("chat_jid", chatA, "exact"), trig("all", "", "exact"), trig("media_type", "audio", "exact"))
	got = wm.matchingTriggers(wm.configs[0], msg, "", "audio", "chat")
	if len(got) != 3 || got[0].TriggerType != "chat_jid" || got[1].TriggerType != "all" || got[2].TriggerType != "media_type" {
		t.Fatalf("AND list = %+v, want chat_jid, all, media_type", got)
	}
}

// deliveredPayload runs ProcessMessage against a real receiver and returns what it posted.
func deliveredPayload(t *testing.T, mode string, triggers []types.WebhookTrigger, msg *events.Message) types.WebhookPayload {
	t.Helper()
	bodies := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
	}))
	t.Cleanup(receiver.Close)

	store, err := database.NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	wm := modeManager("", mode, triggers...)
	wm.messageStore = store
	wm.delivery = NewDeliveryService(store, waLog.Noop)
	wm.configs[0].WebhookURL = receiver.URL

	wm.ProcessMessage(nil, msg, "chat", nil, nil)

	select {
	case body := <-bodies:
		var payload types.WebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
		return types.WebhookPayload{}
	}
}

// T5: a two-trigger AND match sends both in matched_triggers and the first in trigger.
func TestPayloadUnderAnd(t *testing.T) {
	payload := deliveredPayload(t, "and",
		[]types.WebhookTrigger{trig("chat_jid", chatA, "exact"), trig("media_type", "audio", "exact")},
		message(t, chatA, chatA, audio()))

	if payload.TriggerMode != "and" {
		t.Errorf("trigger_mode = %q, want and", payload.TriggerMode)
	}
	if payload.Trigger != (types.WebhookTriggerInfo{Type: "chat_jid", Value: chatA, MatchType: "exact"}) {
		t.Errorf("trigger = %+v, want the chat trigger", payload.Trigger)
	}
	if len(payload.MatchedTriggers) != 2 ||
		payload.MatchedTriggers[0].Type != "chat_jid" || payload.MatchedTriggers[1].Type != "media_type" {
		t.Errorf("matched_triggers = %+v, want chat_jid then media_type", payload.MatchedTriggers)
	}
}

// T5: an OR match that hits the second trigger reports it as trigger and as the only entry.
func TestPayloadUnderOr(t *testing.T) {
	payload := deliveredPayload(t, "or",
		[]types.WebhookTrigger{trig("chat_jid", chatA, "exact"), trig("media_type", "audio", "exact")},
		message(t, chatB, chatB, audio()))

	if payload.TriggerMode != "or" {
		t.Errorf("trigger_mode = %q, want or", payload.TriggerMode)
	}
	want := types.WebhookTriggerInfo{Type: "media_type", Value: "audio", MatchType: "exact"}
	if payload.Trigger != want {
		t.Errorf("trigger = %+v, want %+v", payload.Trigger, want)
	}
	if len(payload.MatchedTriggers) != 1 || payload.MatchedTriggers[0] != want {
		t.Errorf("matched_triggers = %+v, want just %+v", payload.MatchedTriggers, want)
	}
}

func captioned(caption string) *waProto.Message {
	return &waProto.Message{ImageMessage: &waProto.ImageMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/i.enc"), Caption: proto.String(caption),
	}}
}

// ADR 0002, decision 2: a keyword reads an image's caption, in both modes.
func TestKeywordMatchesCaption(t *testing.T) {
	or := modeManager("", "or", trig("keyword", "invoice", "contains"))
	if len(or.MatchesTriggers(message(t, chatA, chatA, captioned("the invoice for March")), "chat")) != 1 {
		t.Error("OR keyword did not match an image whose caption contains it")
	}
	if len(or.MatchesTriggers(message(t, chatA, chatA, captioned("")), "chat")) != 0 {
		t.Error("OR keyword matched an image with an empty caption")
	}
	if len(or.MatchesTriggers(message(t, chatA, chatA, captioned("holiday photos")), "chat")) != 0 {
		t.Error("OR keyword matched a caption without the word")
	}

	and := modeManager("", "and", trig("keyword", "invoice", "contains"), trig("media_type", "image", "exact"))
	if len(and.MatchesTriggers(message(t, chatA, chatA, captioned("invoice attached")), "chat")) != 1 {
		t.Error("AND keyword + image did not match a captioned image")
	}
	if len(and.MatchesTriggers(message(t, chatA, chatA, text("invoice attached")), "chat")) != 0 {
		t.Error("AND keyword + image matched plain text")
	}
}

// The webhook payload carries the caption as content.
func TestPayloadContentIsCaption(t *testing.T) {
	got := deliveredPayload(t, "or", []types.WebhookTrigger{trig("keyword", "invoice", "contains")},
		message(t, chatA, chatA, captioned("invoice attached")))
	if got.Message.Content != "invoice attached" || got.Message.MediaType != "image" {
		t.Errorf("content = %q, media_type = %q", got.Message.Content, got.Message.MediaType)
	}
}
