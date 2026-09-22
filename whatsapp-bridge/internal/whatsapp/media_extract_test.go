package whatsapp

import (
	"strings"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// audioMsg builds a message carrying one audio attachment.
func audioMsg(a *waProto.AudioMessage) *waProto.Message {
	return &waProto.Message{AudioMessage: a}
}

func TestExtractMediaInfoAudio(t *testing.T) {
	key := make([]byte, 32)

	t.Run("voice note carries ptt, seconds, mimetype and direct path", func(t *testing.T) {
		meta := ExtractMediaInfo(audioMsg(&waProto.AudioMessage{
			URL:           proto.String("https://mmg.whatsapp.net/v/t62/x.enc?ccb=11-4"),
			DirectPath:    proto.String("/v/t62/x.enc"),
			Mimetype:      proto.String("audio/ogg; codecs=opus"),
			Seconds:       proto.Uint32(9),
			PTT:           proto.Bool(true),
			MediaKey:      key,
			FileSHA256:    key,
			FileEncSHA256: key,
			FileLength:    proto.Uint64(2048),
		}), "MSGID1")

		if meta.MediaType != "audio" {
			t.Errorf("MediaType = %q, want audio", meta.MediaType)
		}
		if !meta.IsPTT {
			t.Error("IsPTT = false, want true for a voice note")
		}
		if meta.Seconds != 9 {
			t.Errorf("Seconds = %d, want 9", meta.Seconds)
		}
		if meta.Mimetype != "audio/ogg; codecs=opus" {
			t.Errorf("Mimetype = %q", meta.Mimetype)
		}
		if meta.DirectPath != "/v/t62/x.enc" {
			t.Errorf("DirectPath = %q", meta.DirectPath)
		}
		if meta.Filename != "voice_MSGID1.ogg" {
			t.Errorf("Filename = %q, want voice_MSGID1.ogg", meta.Filename)
		}
		if meta.FileLength != 2048 {
			t.Errorf("FileLength = %d, want 2048", meta.FileLength)
		}
	})

	t.Run("attached audio file is not a voice note", func(t *testing.T) {
		meta := ExtractMediaInfo(audioMsg(&waProto.AudioMessage{
			URL:      proto.String("https://mmg.whatsapp.net/v/t62/song.enc"),
			Mimetype: proto.String("audio/mpeg"),
			PTT:      proto.Bool(false),
		}), "MSGID2")

		if meta.IsPTT {
			t.Error("IsPTT = true, want false for an attached audio file")
		}
		if meta.Filename != "audio_MSGID2.ogg" {
			t.Errorf("Filename = %q, want audio_MSGID2.ogg", meta.Filename)
		}
	})

	t.Run("view-once audio keeps no decryption material", func(t *testing.T) {
		meta := ExtractMediaInfo(audioMsg(&waProto.AudioMessage{
			URL:        proto.String("https://mmg.whatsapp.net/v/t62/gone.enc"),
			DirectPath: proto.String("/v/t62/gone.enc"),
			MediaKey:   key,
			PTT:        proto.Bool(true),
			ViewOnce:   proto.Bool(true),
		}), "MSGID3")

		if meta.MediaType != "" || meta.URL != "" || meta.MediaKey != nil || meta.DirectPath != "" {
			t.Errorf("view-once audio was captured: %+v", meta)
		}
	})

	t.Run("no message id falls back to a timestamp name", func(t *testing.T) {
		meta := ExtractMediaInfo(audioMsg(&waProto.AudioMessage{
			URL: proto.String("https://mmg.whatsapp.net/v/t62/x.enc"),
			PTT: proto.Bool(true),
		}), "")

		if !strings.HasPrefix(meta.Filename, "voice_") || !strings.HasSuffix(meta.Filename, ".ogg") {
			t.Errorf("Filename = %q, want voice_<timestamp>.ogg", meta.Filename)
		}
		if meta.Filename == "voice_.ogg" {
			t.Error("Filename has an empty timestamp")
		}
	})

	t.Run("nil message yields nothing", func(t *testing.T) {
		if meta := ExtractMediaInfo(nil, "X"); meta.MediaType != "" {
			t.Errorf("nil message gave %+v", meta)
		}
	})
}

// TestExtractMediaInfoOtherTypes pins the media type of the non-audio branches. Image,
// video and document may carry direct_path and mimetype, and video seconds (ADR 0002);
// captions_test.go checks those. is_ptt stays audio-only.
func TestExtractMediaInfoOtherTypes(t *testing.T) {
	cases := []struct {
		name      string
		msg       *waProto.Message
		mediaType string
	}{
		{"image", &waProto.Message{ImageMessage: &waProto.ImageMessage{URL: proto.String("u")}}, "image"},
		{"video", &waProto.Message{VideoMessage: &waProto.VideoMessage{URL: proto.String("u")}}, "video"},
		{"document", &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
			URL: proto.String("u"), FileName: proto.String("report.pdf")}}, "document"},
		{"text", &waProto.Message{Conversation: proto.String("hi")}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := ExtractMediaInfo(tc.msg, "ID")
			if meta.MediaType != tc.mediaType {
				t.Errorf("MediaType = %q, want %q", meta.MediaType, tc.mediaType)
			}
			// Only audio is a voice note; none of these messages set a duration
			if meta.IsPTT || meta.Seconds != 0 {
				t.Errorf("non-audio carried audio metadata: %+v", meta)
			}
		})
	}

	if got := ExtractMediaInfo(&waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		URL: proto.String("u"), FileName: proto.String("report.pdf")}}, "ID").Filename; got != "report.pdf" {
		t.Errorf("document filename = %q, want report.pdf", got)
	}
}

// Every media type ExtractMediaInfo returns is in ProducedMediaTypes, which listener
// validation treats as the closed set of what a message can be (ADR 0001, R1 and R5).
func TestProducedMediaTypesCoverExtractMediaInfo(t *testing.T) {
	msgs := map[string]*waProto.Message{
		"poll":      {PollCreationMessage: &waProto.PollCreationMessage{Name: proto.String("q")}},
		"poll_vote": {PollUpdateMessage: &waProto.PollUpdateMessage{}},
		"image":     {ImageMessage: &waProto.ImageMessage{URL: proto.String("u")}},
		"video":     {VideoMessage: &waProto.VideoMessage{URL: proto.String("u")}},
		"audio":     {AudioMessage: &waProto.AudioMessage{URL: proto.String("u")}},
		"document":  {DocumentMessage: &waProto.DocumentMessage{URL: proto.String("u")}},
		"text":      {Conversation: proto.String("hi")},
		"sticker":   {StickerMessage: &waProto.StickerMessage{URL: proto.String("u")}},
	}
	produced := make(map[string]bool)
	for _, mt := range ProducedMediaTypes {
		produced[mt] = true
	}
	seen := make(map[string]bool)
	for name, msg := range msgs {
		mt := ExtractMediaInfo(msg, "M1").MediaType
		if mt != "" && !produced[mt] {
			t.Errorf("%s: ExtractMediaInfo returned %q, which ProducedMediaTypes does not list", name, mt)
		}
		seen[mt] = true
	}
	for _, mt := range ProducedMediaTypes {
		if !seen[mt] {
			t.Errorf("ProducedMediaTypes lists %q, but no message here produces it", mt)
		}
	}
}
