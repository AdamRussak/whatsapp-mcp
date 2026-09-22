package whatsapp

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waTypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// ADR 0002, decision 1: the first non-empty value wins, in the order conversation,
// extended text, image / video / document caption, poll question.
func TestExtractTextContentReadsCaptions(t *testing.T) {
	cases := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{"image caption", &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("look at this")}}, "look at this"},
		{"video caption", &waProto.Message{VideoMessage: &waProto.VideoMessage{Caption: proto.String("the clip")}}, "the clip"},
		{"document caption", &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
			FileName: proto.String("invoice.pdf"), Caption: proto.String("invoice attached")}}, "invoice attached"},
		{"empty caption stays empty", &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("")}}, ""},
		{"no caption stays empty", &waProto.Message{VideoMessage: &waProto.VideoMessage{}}, ""},
		{"plain text wins over a caption", &waProto.Message{
			Conversation: proto.String("text"),
			ImageMessage: &waProto.ImageMessage{Caption: proto.String("caption")}}, "text"},
		{"extended text wins over a caption", &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("ext")},
			ImageMessage:        &waProto.ImageMessage{Caption: proto.String("caption")}}, "ext"},
		{"empty extended text does not shadow a caption", &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("")},
			ImageMessage:        &waProto.ImageMessage{Caption: proto.String("caption")}}, "caption"},
		{"image caption wins over video caption", &waProto.Message{
			ImageMessage: &waProto.ImageMessage{Caption: proto.String("img")},
			VideoMessage: &waProto.VideoMessage{Caption: proto.String("vid")}}, "img"},
		{"poll question unchanged", &waProto.Message{PollCreationMessage: &waProto.PollCreationMessage{Name: proto.String("lunch?")}}, "lunch?"},
		{"audio has no text", &waProto.Message{AudioMessage: &waProto.AudioMessage{PTT: proto.Bool(true)}}, ""},
		{"nil message", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractTextContent(tc.msg); got != tc.want {
				t.Errorf("ExtractTextContent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCarriesText(t *testing.T) {
	for mediaType, want := range map[string]bool{
		"": true, "poll": true, "image": true, "video": true, "document": true,
		"audio": false, "poll_vote": false, "sticker": false,
	} {
		if got := CarriesText(mediaType); got != want {
			t.Errorf("CarriesText(%q) = %v, want %v", mediaType, got, want)
		}
	}
}

// ADR 0002, decision 3: image, video and document fill direct_path and mimetype; video
// also fills seconds; is_ptt stays audio-only.
func TestExtractMediaInfoDownloadFields(t *testing.T) {
	key := make([]byte, 32)

	img := ExtractMediaInfo(&waProto.Message{ImageMessage: &waProto.ImageMessage{
		URL: proto.String("https://mmg.whatsapp.net/o1/v/i.enc?ccb=9-4"), DirectPath: proto.String("/o1/v/i.enc?ccb=9-4"),
		Mimetype: proto.String("image/jpeg"), MediaKey: key, FileSHA256: key, FileEncSHA256: key,
		FileLength: proto.Uint64(4096), Caption: proto.String("hi"),
	}}, "IMG1")
	if img.MediaType != "image" || img.DirectPath != "/o1/v/i.enc?ccb=9-4" || img.Mimetype != "image/jpeg" {
		t.Errorf("image: %+v", img)
	}
	if img.Seconds != 0 || img.IsPTT {
		t.Errorf("image carried seconds or ptt: %+v", img)
	}
	if img.Filename != "image_IMG1.jpg" {
		t.Errorf("image filename = %q, want image_IMG1.jpg", img.Filename)
	}
	if img.URL == "" || img.MediaKey == nil || img.FileLength != 4096 {
		t.Errorf("image lost its decryption material: %+v", img)
	}

	vid := ExtractMediaInfo(&waProto.Message{VideoMessage: &waProto.VideoMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/v.enc"), DirectPath: proto.String("/v/v.enc"),
		Mimetype: proto.String("video/mp4"), Seconds: proto.Uint32(12), MediaKey: key,
	}}, "VID1")
	if vid.MediaType != "video" || vid.DirectPath != "/v/v.enc" || vid.Mimetype != "video/mp4" || vid.Seconds != 12 || vid.IsPTT {
		t.Errorf("video: %+v", vid)
	}
	if vid.Filename != "video_VID1.mp4" {
		t.Errorf("video filename = %q, want video_VID1.mp4", vid.Filename)
	}

	doc := ExtractMediaInfo(&waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/d.enc"), DirectPath: proto.String("/v/d.enc"),
		Mimetype: proto.String("application/pdf"), FileName: proto.String("report.pdf"),
	}}, "DOC1")
	if doc.MediaType != "document" || doc.DirectPath != "/v/d.enc" || doc.Mimetype != "application/pdf" || doc.Seconds != 0 || doc.IsPTT {
		t.Errorf("document: %+v", doc)
	}
	if doc.Filename != "report.pdf" {
		t.Errorf("document filename = %q, want report.pdf", doc.Filename)
	}
}

func TestMediaFilenames(t *testing.T) {
	cases := []struct {
		name string
		msg  *waProto.Message
		id   string
		want string
	}{
		{"png", &waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("image/png")}}, "A", "image_A.png"},
		{"webp", &waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("image/webp")}}, "A", "image_A.webp"},
		{"gif", &waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("image/gif")}}, "A", "image_A.gif"},
		{"jpeg with parameters", &waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("IMAGE/JPEG; q=1")}}, "A", "image_A.jpg"},
		{"image without mimetype", &waProto.Message{ImageMessage: &waProto.ImageMessage{}}, "A", "image_A.jpg"},
		{"image with unknown mimetype", &waProto.Message{ImageMessage: &waProto.ImageMessage{Mimetype: proto.String("image/heic")}}, "A", "image_A.jpg"},
		{"video without mimetype", &waProto.Message{VideoMessage: &waProto.VideoMessage{}}, "B", "video_B.mp4"},
		{"gif playback is still mp4", &waProto.Message{VideoMessage: &waProto.VideoMessage{
			Mimetype: proto.String("video/mp4"), GifPlayback: proto.Bool(true)}}, "B", "video_B.mp4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractMediaInfo(tc.msg, tc.id).Filename; got != tc.want {
				t.Errorf("Filename = %q, want %q", got, tc.want)
			}
		})
	}

	// Two images in the same second with different ids no longer share a name
	a := ExtractMediaInfo(&waProto.Message{ImageMessage: &waProto.ImageMessage{}}, "ID_A").Filename
	b := ExtractMediaInfo(&waProto.Message{ImageMessage: &waProto.ImageMessage{}}, "ID_B").Filename
	if a == b {
		t.Errorf("two ids share filename %q", a)
	}

	// No id falls back to the timestamp name
	for _, tc := range []struct {
		msg            *waProto.Message
		prefix, suffix string
	}{
		{&waProto.Message{ImageMessage: &waProto.ImageMessage{}}, "image_", ".jpg"},
		{&waProto.Message{VideoMessage: &waProto.VideoMessage{}}, "video_", ".mp4"},
	} {
		got := ExtractMediaInfo(tc.msg, "").Filename
		if !strings.HasPrefix(got, tc.prefix) || !strings.HasSuffix(got, tc.suffix) || got == tc.prefix+tc.suffix {
			t.Errorf("no id: Filename = %q, want %s<timestamp>%s", got, tc.prefix, tc.suffix)
		}
	}
}

// ADR 0002, decision 4: a view-once image or video keeps its type and nothing that could
// download it.
func TestExtractMediaInfoViewOnceImageAndVideo(t *testing.T) {
	key := make([]byte, 32)
	for _, msg := range []*waProto.Message{
		{ImageMessage: &waProto.ImageMessage{
			URL: proto.String("https://mmg.whatsapp.net/v/gone.enc"), DirectPath: proto.String("/v/gone.enc"),
			MediaKey: key, FileSHA256: key, FileEncSHA256: key, FileLength: proto.Uint64(10),
			Mimetype: proto.String("image/jpeg"), ViewOnce: proto.Bool(true),
		}},
		{VideoMessage: &waProto.VideoMessage{
			URL: proto.String("https://mmg.whatsapp.net/v/gone.enc"), DirectPath: proto.String("/v/gone.enc"),
			MediaKey: key, FileSHA256: key, FileEncSHA256: key, FileLength: proto.Uint64(10),
			Seconds: proto.Uint32(3), ViewOnce: proto.Bool(true),
		}},
	} {
		meta := ExtractMediaInfo(msg, "VO1")
		if meta.MediaType != "image" && meta.MediaType != "video" {
			t.Errorf("view-once lost its media type: %+v", meta)
		}
		if meta.URL != "" || meta.MediaKey != nil || meta.FileSHA256 != nil || meta.FileEncSHA256 != nil ||
			meta.FileLength != 0 || meta.DirectPath != "" {
			t.Errorf("view-once %s kept download material: %+v", meta.MediaType, meta)
		}
	}
}

// testClient is a logged-in Client over an in-memory whatsmeow store.
func testClient(t *testing.T) *Client {
	t.Helper()
	container, err := sqlstore.New(context.Background(), "sqlite3", "file::memory:?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { container.Close() })
	device := container.NewDevice()
	me := waTypes.NewJID("999999999999", waTypes.DefaultUserServer)
	device.ID = &me
	return &Client{Client: whatsmeow.NewClient(device, waLog.Noop), logger: waLog.Noop}
}

func testMessageStore(t *testing.T) *database.MessageStore {
	t.Helper()
	store, err := database.NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

type storedRow struct {
	content, mediaType                  string
	url, directPath, mimetype, filename sql.NullString
	mediaKey                            []byte
	seconds                             sql.NullInt64
	isPTT                               sql.NullBool
}

func readRow(t *testing.T, store *database.MessageStore, id string) (storedRow, bool) {
	t.Helper()
	var r storedRow
	err := store.GetDB().QueryRow(`SELECT content, media_type, url, direct_path, mimetype, filename, media_key, seconds, is_ptt
		FROM messages WHERE id = ?`, id).Scan(&r.content, &r.mediaType, &r.url, &r.directPath, &r.mimetype,
		&r.filename, &r.mediaKey, &r.seconds, &r.isPTT)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func liveMessage(id string, content *waProto.Message) *events.Message {
	chat := waTypes.NewJID("111111111111", waTypes.DefaultUserServer)
	return &events.Message{
		Info: waTypes.MessageInfo{
			MessageSource: waTypes.MessageSource{Chat: chat, Sender: chat},
			ID:            waTypes.MessageID(id),
			Timestamp:     time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		},
		Message: content,
	}
}

// The live path stores a caption as content, and the image's download fields (T1, T3).
func TestHandleMessageStoresCaptionAndDownloadFields(t *testing.T) {
	client, store := testClient(t), testMessageStore(t)
	key := make([]byte, 32)

	client.HandleMessage(store, nil, liveMessage("IMG1", &waProto.Message{ImageMessage: &waProto.ImageMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/i.enc"), DirectPath: proto.String("/v/i.enc"),
		Mimetype: proto.String("image/jpeg"), MediaKey: key, FileSHA256: key, FileEncSHA256: key,
		Caption: proto.String("invoice attached"),
	}}))
	row, ok := readRow(t, store, "IMG1")
	if !ok {
		t.Fatal("captioned image was not stored")
	}
	if row.content != "invoice attached" || row.mediaType != "image" {
		t.Errorf("content = %q, media_type = %q", row.content, row.mediaType)
	}
	if row.directPath.String != "/v/i.enc" || row.mimetype.String != "image/jpeg" {
		t.Errorf("direct_path = %v, mimetype = %v", row.directPath, row.mimetype)
	}
	if row.isPTT.Valid {
		t.Errorf("is_ptt = %v, want NULL for an image", row.isPTT)
	}
	if row.filename.String != "image_IMG1.jpg" {
		t.Errorf("filename = %v", row.filename)
	}

	client.HandleMessage(store, nil, liveMessage("VID1", &waProto.Message{VideoMessage: &waProto.VideoMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/v.enc"), Mimetype: proto.String("video/mp4"), Seconds: proto.Uint32(8),
	}}))
	row, _ = readRow(t, store, "VID1")
	if row.content != "" || row.mediaType != "video" || row.seconds.Int64 != 8 || row.isPTT.Valid {
		t.Errorf("uncaptioned video: %+v", row)
	}

	// A view-once image keeps its type and caption, and no url, key or direct path
	client.HandleMessage(store, nil, liveMessage("VO1", &waProto.Message{ImageMessage: &waProto.ImageMessage{
		URL: proto.String("https://mmg.whatsapp.net/v/gone.enc"), DirectPath: proto.String("/v/gone.enc"),
		MediaKey: key, FileSHA256: key, FileEncSHA256: key, Caption: proto.String("once"), ViewOnce: proto.Bool(true),
	}}))
	row, ok = readRow(t, store, "VO1")
	if !ok {
		t.Fatal("view-once image was not stored")
	}
	if row.mediaType != "image" || row.content != "once" {
		t.Errorf("view-once: media_type = %q, content = %q", row.mediaType, row.content)
	}
	if row.url.String != "" || len(row.mediaKey) != 0 || row.directPath.Valid {
		t.Errorf("view-once kept url %v, key %x, direct_path %v", row.url, row.mediaKey, row.directPath)
	}

	// Plain text is unchanged
	client.HandleMessage(store, nil, liveMessage("TXT1", &waProto.Message{Conversation: proto.String("hello")}))
	row, _ = readRow(t, store, "TXT1")
	if row.content != "hello" || row.mediaType != "" {
		t.Errorf("text: %+v", row)
	}
}

func historyMsg(id string, ts uint64, content *waProto.Message) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(false)},
		MessageTimestamp: proto.Uint64(ts),
		Message:          content,
	}}
}

// History sync reads captions with the same function as the live path (T1). It is not
// unwrapped, so a wrapped message still yields nothing, as before.
func TestHandleHistorySyncStoresCaption(t *testing.T) {
	client, store := testClient(t), testMessageStore(t)
	ts := uint64(time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC).Unix())

	client.HandleHistorySync(store, &events.HistorySync{Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String("111111111111@s.whatsapp.net"),
			Messages: []*waHistorySync.HistorySyncMsg{
				historyMsg("H_IMG", ts, &waProto.Message{ImageMessage: &waProto.ImageMessage{
					URL: proto.String("https://mmg.whatsapp.net/v/h.enc"), Mimetype: proto.String("image/png"),
					Caption: proto.String("from history"),
				}}),
				historyMsg("H_EXT", ts, &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("a link")}}),
				historyMsg("H_WRAPPED", ts, &waProto.Message{ViewOnceMessage: &waProto.FutureProofMessage{
					Message: &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("hidden")}},
				}}),
			},
		}},
	}})

	row, ok := readRow(t, store, "H_IMG")
	if !ok {
		t.Fatal("history image was not stored")
	}
	if row.content != "from history" || row.mediaType != "image" || row.mimetype.String != "image/png" || row.filename.String != "image_H_IMG.png" {
		t.Errorf("history image: %+v", row)
	}
	if row, _ := readRow(t, store, "H_EXT"); row.content != "a link" {
		t.Errorf("history extended text: content = %q", row.content)
	}
	if _, ok := readRow(t, store, "H_WRAPPED"); ok {
		t.Error("a wrapped history message was stored; history sync is not meant to unwrap")
	}
}
