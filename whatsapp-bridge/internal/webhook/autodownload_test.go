package webhook

import (
	"testing"

	"whatsapp-bridge/internal/autodownload"
	"whatsapp-bridge/internal/types"

	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// recordingDownloader stands in for the auto-download pool.
type recordingDownloader struct{ jobs []autodownload.Job }

func (r *recordingDownloader) Enqueue(job autodownload.Job) bool {
	r.jobs = append(r.jobs, job)
	return true
}

func testMessage(t *testing.T, chatJID string) *events.Message {
	t.Helper()
	chat, err := waTypes.ParseJID(chatJID)
	if err != nil {
		t.Fatal(err)
	}
	return &events.Message{
		Info: waTypes.MessageInfo{
			MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat},
			ID:            "M1",
		},
	}
}

// voiceNote is what ExtractMediaInfo produces for a live voice note since schema v1.
func voiceNote() types.MediaMetadata {
	return types.MediaMetadata{
		MediaType:  "audio",
		Filename:   "voice_M1.ogg",
		URL:        "https://mmg.whatsapp.net/v/x.enc?ccb=1",
		DirectPath: "/v/x.enc?ccb=1",
		IsPTT:      true,
	}
}

func listener(id int, autoDownload bool, voiceOnly *bool) *types.WebhookConfig {
	return &types.WebhookConfig{
		ID:                    id,
		Name:                  "listener",
		Enabled:               true,
		AutoDownload:          autoDownload,
		AutoDownloadVoiceOnly: voiceOnly,
	}
}

func boolPtr(b bool) *bool { return &b }

// The trigger decides who is captured; these flags decide what.
func TestEnqueueAutoDownloadsGate(t *testing.T) {
	audioFile := func() types.MediaMetadata {
		m := voiceNote()
		m.Filename, m.IsPTT = "audio_M1.mp3", false
		return m
	}
	image := func() types.MediaMetadata {
		m := voiceNote()
		m.MediaType, m.IsPTT = "image", false
		return m
	}
	text := func() types.MediaMetadata { return types.MediaMetadata{} }
	// View-once audio is deliberately stored without url, key or direct path (§2.11), so
	// a download could only ever fail.
	viewOnce := func() types.MediaMetadata {
		m := voiceNote()
		m.URL, m.DirectPath = "", ""
		return m
	}

	tests := []struct {
		name    string
		media   types.MediaMetadata
		configs []*types.WebhookConfig
		want    bool
	}{
		{"voice note, voice-only listener", voiceNote(), []*types.WebhookConfig{listener(1, true, boolPtr(true))}, true},
		{"voice note, all-audio listener", voiceNote(), []*types.WebhookConfig{listener(1, true, boolPtr(false))}, true},
		{"voice note, listener with the flag never set", voiceNote(), []*types.WebhookConfig{listener(1, true, nil)}, true},
		{"audio file, voice-only listener", audioFile(), []*types.WebhookConfig{listener(1, true, boolPtr(true))}, false},
		{"audio file, all-audio listener", audioFile(), []*types.WebhookConfig{listener(1, true, boolPtr(false))}, true},
		{"image", image(), []*types.WebhookConfig{listener(1, true, boolPtr(false))}, false},
		{"text message from a matching sender", text(), []*types.WebhookConfig{listener(1, true, boolPtr(false))}, false},
		{"view-once audio", viewOnce(), []*types.WebhookConfig{listener(1, true, boolPtr(true))}, false},
		{"matched, but auto-download off", voiceNote(), []*types.WebhookConfig{listener(1, false, nil)}, false},
		{"no matched configs", voiceNote(), nil, false},
		{
			"a voice-only listener does not stop a later all-audio one",
			audioFile(),
			[]*types.WebhookConfig{listener(1, true, boolPtr(true)), listener(2, true, boolPtr(false))},
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingDownloader{}
			wm := NewManager(nil, waLog.Noop, "")
			wm.SetAutoDownloader(rec)

			wm.enqueueAutoDownloads(testMessage(t, "c@s.whatsapp.net"), tc.media, tc.configs)

			if got := len(rec.jobs) == 1; got != tc.want {
				t.Fatalf("enqueued %d job(s), want enqueued=%v", len(rec.jobs), tc.want)
			}
		})
	}
}

// Two listeners wanting the same message produce one job, attributed to the first.
func TestEnqueueAutoDownloadsQueuesOnceForSeveralListeners(t *testing.T) {
	rec := &recordingDownloader{}
	wm := NewManager(nil, waLog.Noop, "")
	wm.SetAutoDownloader(rec)

	configs := []*types.WebhookConfig{listener(3, true, boolPtr(true)), listener(4, true, boolPtr(true))}
	wm.enqueueAutoDownloads(testMessage(t, "c@s.whatsapp.net"), voiceNote(), configs)

	if len(rec.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(rec.jobs))
	}
	job := rec.jobs[0]
	if job.MessageID != "M1" || job.ChatJID != "c@s.whatsapp.net" || job.RuleID != 3 {
		t.Fatalf("job = %+v, want M1 in c@s.whatsapp.net attributed to rule 3", job)
	}
}

// Without a pool attached, matching must simply do nothing.
func TestEnqueueAutoDownloadsWithoutPool(t *testing.T) {
	wm := NewManager(nil, waLog.Noop, "")
	wm.enqueueAutoDownloads(testMessage(t, "c@s.whatsapp.net"), voiceNote(),
		[]*types.WebhookConfig{listener(1, true, boolPtr(true))})
}

func TestValidateWebhookConfigURLRules(t *testing.T) {
	wm := NewManager(nil, waLog.Noop, "")
	trigger := []types.WebhookTrigger{{TriggerType: "sender", TriggerValue: "972500000000", MatchType: "exact", Enabled: true}}

	tests := []struct {
		name    string
		config  types.WebhookConfig
		wantErr bool
	}{
		{
			"download-only listener needs no URL",
			types.WebhookConfig{Name: "capture", AutoDownload: true, Triggers: trigger},
			false,
		},
		{
			"delivery listener still needs a URL",
			types.WebhookConfig{Name: "notify", Triggers: trigger},
			true,
		},
		{
			"a URL is still validated when auto-download is on",
			types.WebhookConfig{Name: "both", WebhookURL: "ftp://example.com", AutoDownload: true, Triggers: trigger},
			true,
		},
		{
			"both actions together",
			types.WebhookConfig{Name: "both", WebhookURL: "https://example.com/hook", AutoDownload: true, Triggers: trigger},
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := wm.ValidateWebhookConfig(&tc.config)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateWebhookConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Testing a listener that posts nowhere has to fail loudly rather than pretend to deliver.
func TestTestWebhookRejectsDownloadOnlyListener(t *testing.T) {
	wm := NewManager(nil, waLog.Noop, "")
	err := wm.TestWebhook(&types.WebhookConfig{Name: "capture", AutoDownload: true})
	if err == nil {
		t.Fatal("TestWebhook accepted a listener with no URL")
	}
}
