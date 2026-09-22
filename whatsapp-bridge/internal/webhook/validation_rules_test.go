package webhook

import (
	"errors"
	"strings"
	"testing"

	"whatsapp-bridge/internal/types"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func validate(t *testing.T, storeDir string, config types.WebhookConfig) ValidationErrors {
	t.Helper()
	if config.Name == "" {
		config.Name = "listener"
	}
	if config.WebhookURL == "" && !config.AutoDownload {
		config.WebhookURL = "https://example.com/hook"
	}
	err := NewManager(nil, waLog.Noop, "", storeDir).ValidateWebhookConfig(&config)
	if err == nil {
		return nil
	}
	var errs ValidationErrors
	if !errors.As(err, &errs) {
		t.Fatalf("ValidateWebhookConfig returned %T, want ValidationErrors", err)
	}
	return errs
}

func and(triggers ...types.WebhookTrigger) types.WebhookConfig {
	return types.WebhookConfig{TriggerMode: "and", Triggers: triggers}
}

func or(triggers ...types.WebhookTrigger) types.WebhookConfig {
	return types.WebhookConfig{TriggerMode: "or", Triggers: triggers}
}

func capture(config types.WebhookConfig) types.WebhookConfig {
	config.AutoDownload = true
	return config
}

// One test per rule: the rejected example from ADR 0001, and a near miss that must pass.
func TestValidationRules(t *testing.T) {
	lidDir := seedLIDMap(t)

	tests := []struct {
		name      string
		storeDir  string
		config    types.WebhookConfig
		wantRule  string // "" means the listener must be accepted
		wantIndex int
	}{
		// R1: a media type must be one the bridge produces.
		{name: "R1 sticker, OR", config: or(trig("media_type", "sticker", "exact")), wantRule: "R1", wantIndex: 0},
		{name: "R1 sticker, AND", config: and(trig("chat_jid", chatA, "exact"), trig("media_type", "sticker", "exact")), wantRule: "R1", wantIndex: 1},
		{name: "R1 typo", config: or(trig("media_type", "audoi", "exact")), wantRule: "R1", wantIndex: 0},
		{name: "R1 contains nothing produced", config: or(trig("media_type", "stick", "contains")), wantRule: "R1", wantIndex: 0},
		{name: "R1 regex nothing produced", config: or(trig("media_type", "^gif$", "regex")), wantRule: "R1", wantIndex: 0},
		{name: "R1 near miss: contains that hits a produced type", config: or(trig("media_type", "poll", "contains"))},
		{name: "R1 near miss: exact produced type", config: or(trig("media_type", "video", "exact"))},

		// R2: auto-download needs a listener that can match audio.
		{name: "R2 image only", config: capture(or(trig("media_type", "image", "exact"))), wantRule: "R2", wantIndex: -1},
		{name: "R2 keyword only, OR", config: capture(or(trig("keyword", "urgent", "contains"))), wantRule: "R2", wantIndex: -1},
		{name: "R2 AND with a keyword", config: capture(and(trig("chat_jid", chatA, "exact"), trig("keyword", "x", "contains"))), wantRule: "R2", wantIndex: 1},
		{name: "R2 no enabled triggers", config: capture(or()), wantRule: "R2", wantIndex: -1},
		{name: "R2 near miss: OR with a chat beside a keyword", config: capture(or(trig("keyword", "x", "contains"), trig("chat_jid", chatA, "exact")))},
		{name: "R2 near miss: media accepting audio", config: capture(or(trig("media_type", "^(audio|image)$", "regex")))},
		{name: "R2 near miss: AND chat and audio", config: capture(and(trig("chat_jid", chatA, "exact"), trig("media_type", "audio", "exact")))},
		{name: "R2 near miss: off, so image is fine", config: or(trig("media_type", "image", "exact"))},

		// R3: one value per single-valued field.
		{name: "R3 two chats", config: and(trig("chat_jid", chatA, "exact"), trig("chat_jid", chatB, "exact")), wantRule: "R3", wantIndex: 1},
		{name: "R3 two senders", config: and(trig("sender", dana, "exact"), trig("sender", other, "exact")), wantRule: "R3", wantIndex: 1},
		{name: "R3 two media types", config: and(trig("media_type", "audio", "exact"), trig("media_type", "image", "exact")), wantRule: "R3", wantIndex: 1},
		{name: "R3 near miss: phone and LID of one contact", storeDir: lidDir,
			config: and(trig("chat_jid", "972547669192", "exact"), trig("chat_jid", "62156783493249@lid", "exact"))},
		{name: "R3 near miss: bare number and its JID", config: and(trig("sender", "333333333333", "exact"), trig("sender", dana, "exact"))},
		{name: "R3 near miss: sender AND chat", config: and(trig("sender", dana, "exact"), trig("chat_jid", family, "exact"))},
		{name: "R3 near miss: same chat twice", config: and(trig("chat_jid", chatA, "exact"), trig("chat_jid", chatA, "exact"))},
		{name: "R3 two chats under OR is fine", config: or(trig("chat_jid", chatA, "exact"), trig("chat_jid", chatB, "exact"))},

		// R4: a pattern must agree with an exact value on the same field.
		{name: "R4 group and @lid pattern", config: and(trig("chat_jid", family, "exact"), trig("chat_jid", "@lid", "contains")), wantRule: "R4", wantIndex: 1},
		{name: "R4 regex against sender", config: and(trig("sender", dana, "exact"), trig("sender", "^9", "regex")), wantRule: "R4", wantIndex: 1},
		{name: "R4 near miss: contains that matches", config: and(trig("chat_jid", family, "exact"), trig("chat_jid", "@g.us", "contains"))},
		{name: "R4 near miss: regex that matches the LID form", storeDir: lidDir,
			config: and(trig("chat_jid", "972547669192", "exact"), trig("chat_jid", "@lid$", "regex"))},
		{name: "R4 near miss: sender pattern on bare user", config: and(trig("sender", dana, "exact"), trig("sender", "^3+$", "regex"))},

		// R5: media-type triggers must overlap.
		{name: "R5 audio and image|video", config: and(trig("media_type", "audio", "exact"), trig("media_type", "^(image|video)$", "regex")), wantRule: "R5", wantIndex: 1},
		{name: "R5 near miss: regex leaves one type", config: and(trig("media_type", "audio", "exact"), trig("media_type", "^(audio|video)$", "regex"))},
		{name: "R5 near miss: two contains that overlap", config: and(trig("media_type", "poll", "contains"), trig("media_type", "vote", "contains"))},

		// R6: keywords need a message that has text.
		{name: "R6 keyword and audio", config: and(trig("keyword", "urgent", "contains"), trig("media_type", "audio", "exact")), wantRule: "R6", wantIndex: 0},
		{name: "R6 keyword and poll_vote", config: and(trig("media_type", "poll_vote", "exact"), trig("keyword", "yes", "exact")), wantRule: "R6", wantIndex: 1},
		{name: "R6 near miss: keyword and poll", config: and(trig("keyword", "lunch", "contains"), trig("media_type", "poll", "exact"))},
		{name: "R6 near miss: two keywords", config: and(trig("keyword", "invoice", "contains"), trig("keyword", "overdue", "contains"))},
		{name: "R6 near miss: keyword and a chat", config: and(trig("keyword", "invoice", "contains"), trig("chat_jid", chatA, "exact"))},
		{name: "R6 keyword and audio under OR is fine", config: or(trig("keyword", "urgent", "contains"), trig("media_type", "audio", "exact"))},
		// ADR 0002: captions are text, so a keyword beside image, video or document can match.
		{name: "R6 near miss: keyword and image", config: and(trig("keyword", "invoice", "contains"), trig("media_type", "image", "exact"))},
		{name: "R6 near miss: keyword and video", config: and(trig("keyword", "invoice", "contains"), trig("media_type", "video", "exact"))},
		{name: "R6 near miss: keyword and document", config: and(trig("keyword", "invoice", "contains"), trig("media_type", "document", "exact"))},
		{name: "R6 keyword and audio|poll_vote", config: and(trig("keyword", "invoice", "contains"), trig("media_type", "^(audio|poll_vote)$", "regex")), wantRule: "R6", wantIndex: 0},

		// Undecidable combinations are allowed.
		{name: "two regexes on one field", config: and(trig("chat_jid", "^1", "regex"), trig("chat_jid", "^2", "regex"))},
		{name: "all beside audio under AND", config: and(trig("all", "", "exact"), trig("media_type", "audio", "exact"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := validate(t, tc.storeDir, tc.config)
			if tc.wantRule == "" {
				if errs != nil {
					t.Fatalf("rejected a listener that can match: %+v", errs)
				}
				return
			}
			for _, e := range errs {
				if e.Rule == tc.wantRule && e.TriggerIndex == tc.wantIndex {
					if !strings.HasPrefix(e.Message, tc.wantRule+": ") {
						t.Errorf("message %q does not name its rule", e.Message)
					}
					return
				}
			}
			t.Fatalf("want %s on trigger %d, got %+v", tc.wantRule, tc.wantIndex, errs)
		})
	}
}

// The R3 message for chats is the one the ADR words, with the fix it offers.
func TestR3ChatMessage(t *testing.T) {
	errs := validate(t, seedLIDMap(t), and(trig("chat_jid", chatA, "exact"), trig("chat_jid", chatB, "exact")))
	if len(errs) != 1 {
		t.Fatalf("got %+v, want one error", errs)
	}
	msg := errs[0].Message
	for _, want := range []string{"A message belongs to one chat", "chat = " + chatA, "chat = " + chatB,
		"set Match to OR with just the two chat triggers", "create one listener per chat"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "skipped") {
		t.Errorf("the LID map was readable, but the message says the check was skipped: %q", msg)
	}
}

// Without a readable LID map, R3 compares values as typed and says so.
func TestR3WithoutLIDMapSaysSo(t *testing.T) {
	errs := validate(t, t.TempDir(), and(trig("chat_jid", "972547669192", "exact"), trig("chat_jid", "62156783493249@lid", "exact")))
	if len(errs) != 1 || errs[0].Rule != "R3" {
		t.Fatalf("got %+v, want one R3", errs)
	}
	if !strings.Contains(errs[0].Message, "phone/LID check was skipped") {
		t.Errorf("message does not say the LID check was skipped: %q", errs[0].Message)
	}
}

// trigger_index is the position in the submitted array, so a disabled row in the middle
// does not renumber the rows the UI draws. Disabled triggers are not judged.
func TestTriggerIndexCountsDisabledRows(t *testing.T) {
	disabled := trig("chat_jid", chatB, "exact")
	disabled.Enabled = false
	errs := validate(t, "", and(trig("chat_jid", chatA, "exact"), disabled, trig("media_type", "sticker", "exact")))
	if len(errs) != 1 || errs[0].Rule != "R1" || errs[0].TriggerIndex != 2 {
		t.Fatalf("got %+v, want only R1 on trigger 2", errs)
	}
}

// Every failure is collected, not just the first.
func TestValidationCollectsEveryFailure(t *testing.T) {
	config := and(
		trig("chat_jid", chatA, "exact"),
		trig("chat_jid", chatB, "exact"),
		trig("media_type", "sticker", "exact"),
	)
	config.WebhookURL = "ftp://nope" // and no name
	var errs ValidationErrors
	if !errors.As(NewManager(nil, waLog.Noop, "", "").ValidateWebhookConfig(&config), &errs) {
		t.Fatal("want ValidationErrors")
	}

	var got []string
	for _, e := range errs {
		got = append(got, e.Rule)
	}
	// name, URL, R1 on the sticker, R3 on chat B
	if len(errs) != 4 || errs[0].TriggerIndex != -1 || errs[1].TriggerIndex != -1 {
		t.Fatalf("got %+v", errs)
	}
	if errs.Error() != errs[0].Message {
		t.Errorf("Error() = %q, want the first message", errs.Error())
	}
	if !strings.Contains(strings.Join(got, ","), "R1") || !strings.Contains(strings.Join(got, ","), "R3") {
		t.Errorf("rules = %v, want R1 and R3", got)
	}
}

func TestTriggerModeMustBeOrOrAnd(t *testing.T) {
	for _, mode := range []string{"AND", "xor", " or"} {
		config := or(trig("all", "", "exact"))
		config.TriggerMode = mode
		errs := validate(t, "", config)
		if len(errs) != 1 || errs[0].TriggerIndex != -1 || !strings.Contains(errs[0].Message, "trigger_mode") {
			t.Errorf("mode %q: got %+v, want one form-level trigger_mode error", mode, errs)
		}
	}
	// Absent means OR.
	if errs := validate(t, "", types.WebhookConfig{Triggers: []types.WebhookTrigger{trig("all", "", "exact")}}); errs != nil {
		t.Errorf("absent mode rejected: %+v", errs)
	}
}

// An enabled trigger without a value is refused on its own row: "contains" nothing would
// match every message. A disabled one is left alone, and "all" never takes a value.
func TestEmptyTriggerValue(t *testing.T) {
	disabled := trig("keyword", "", "contains")
	disabled.Enabled = false
	errs := validate(t, "", or(trig("all", "", "exact"), trig("keyword", "  ", "contains"), disabled))
	if len(errs) != 1 || errs[0].TriggerIndex != 1 {
		t.Fatalf("got %+v, want one error on trigger 1", errs)
	}
}

// The listeners live on 2026-09-21 (values replaced; each has a single trigger) all pass.
func TestLiveListenersPass(t *testing.T) {
	disabled := func(c types.WebhookConfig) types.WebhookConfig { c.Enabled = false; return c }
	live := map[string]types.WebhookConfig{
		"basketball-vote": {Enabled: true, Triggers: []types.WebhookTrigger{trig("chat_jid", "972500000000-1381160877@g.us", "exact")}},
		"poll-vote":       {Enabled: true, Triggers: []types.WebhookTrigger{trig("media_type", "poll_vote", "exact")}},
		"pool-create":     {Enabled: true, Triggers: []types.WebhookTrigger{trig("media_type", "poll", "exact")}},
		"test new domain": disabled(types.WebhookConfig{Triggers: []types.WebhookTrigger{trig("all", "", "exact")}}),
		"me-number":       disabled(types.WebhookConfig{Triggers: []types.WebhookTrigger{trig("sender", "972500000001", "exact")}}),
		"orange-test":     disabled(types.WebhookConfig{Triggers: []types.WebhookTrigger{trig("chat_jid", "972500000002-1558556208@g.us", "exact")}}),
	}
	for name, config := range live {
		for _, mode := range []string{"", "or", "and"} {
			config.TriggerMode = mode
			config.Name = name
			if errs := validate(t, "", config); errs != nil {
				t.Errorf("%s (mode %q) rejected: %+v", name, mode, errs)
			}
		}
	}
}

// Review bug 2: a sticker trigger is enabled, so the only honest reason is R1. R2's "no
// enabled trigger" would be a second, wrong one.
func TestR1FailureAloneDoesNotAlsoReportR2(t *testing.T) {
	for _, mode := range []string{"or", "and"} {
		config := capture(or(trig("media_type", "sticker", "exact")))
		config.TriggerMode = mode
		errs := validate(t, "", config)
		if len(errs) != 1 || errs[0].Rule != "R1" || errs[0].TriggerIndex != 0 {
			t.Errorf("mode %s: got %+v, want R1 on trigger 0 only", mode, errs)
		}
	}
	// With nothing enabled at all, R2 is the right reason.
	disabled := trig("media_type", "audio", "exact")
	disabled.Enabled = false
	errs := validate(t, "", capture(or(disabled)))
	if len(errs) != 1 || errs[0].Rule != "R2" || errs[0].TriggerIndex != -1 {
		t.Errorf("no enabled triggers: got %+v, want one form-level R2", errs)
	}
}

// A keyword on audio sees "". A pattern that matches the empty string can therefore match
// audio, and is allowed on purpose (implementation log, point 2); a real word is not.
func TestKeywordMatchingEmptyTextIsAllowedWithAudio(t *testing.T) {
	for _, pattern := range []string{".*", "^$"} {
		config := capture(and(trig("keyword", pattern, "regex"), trig("media_type", "audio", "exact")))
		if errs := validate(t, "", config); errs != nil {
			t.Errorf("keyword regex %q with audio: rejected %+v", pattern, errs)
		}
	}
	errs := validate(t, "", and(trig("keyword", "urgent", "contains"), trig("media_type", "audio", "exact")))
	if len(errs) != 1 || errs[0].Rule != "R6" {
		t.Errorf("keyword urgent with audio: got %+v, want R6", errs)
	}
}

// ADR 0002: the R6 message no longer claims captions are never read, and names the media
// types that do carry text.
func TestR6MessageMentionsCaptions(t *testing.T) {
	errs := validate(t, "", and(trig("keyword", "invoice", "contains"), trig("media_type", "audio", "exact")))
	if len(errs) != 1 || errs[0].Rule != "R6" {
		t.Fatalf("got %+v, want R6", errs)
	}
	msg := errs[0].Message
	if strings.Contains(msg, "never captions") {
		t.Errorf("R6 message still says captions are never read: %q", msg)
	}
	for _, want := range []string{"captions", "image", "video", "document"} {
		if !strings.Contains(msg, want) {
			t.Errorf("R6 message does not mention %q: %q", want, msg)
		}
	}
}
