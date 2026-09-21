package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"
)

const (
	lidChat   = "62156783493249@lid"
	phoneChat = "972547669192@s.whatsapp.net"
	groupChat = "120363430762896364@g.us"
)

// downloadsServer seeds a store the way the live one looks: voice notes in a contact's LID
// chat, one in a group, a file that has since gone missing, and a failed download.
//
// The download times are deliberately all "today" while the messages span a week, so a
// date filter that used the download time instead of the send time would visibly fail.
func downloadsServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()

	messages, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { messages.Close() })
	media, err := database.NewMediaStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })

	// The LID map, so a chat filter given as a phone number reaches the LID chat.
	wa, err := sql.Open("sqlite3", filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wa.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT);
		INSERT INTO whatsmeow_lid_map VALUES ('62156783493249', '972547669192');`); err != nil {
		t.Fatal(err)
	}
	wa.Close()

	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	downloadedToday := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)

	for _, c := range []struct{ jid, name string }{
		{lidChat, "Dana"}, {groupChat, "Kubecon crew"},
	} {
		if err := messages.StoreChat(c.jid, c.name, day(21)); err != nil {
			t.Fatal(err)
		}
	}

	seed := []struct {
		id, chat, sender string
		sent             time.Time
		fromMe           bool
		status           string
		writeFile        bool
		errMsg           string
	}{
		{"NEWEST", lidChat, "62156783493249", day(20), false, "ok", true, ""},
		{"MINE", lidChat, "14018823594220", day(18), true, "ok", true, ""},
		{"GROUP", groupChat, "216011219464202", day(16), false, "ok", true, ""},
		{"OLDEST", lidChat, "62156783493249", day(14), false, "ok", true, ""},
		{"GONE", lidChat, "62156783493249", day(19), false, "ok", false, ""},
		{"EXPIRED", lidChat, "62156783493249", day(15), false, "failed", false, "direct download expired"},
	}

	for _, m := range seed {
		if err := messages.StoreMessage(m.id, m.chat, m.sender, "", m.sent, m.fromMe, types.MediaMetadata{
			MediaType: "audio", Filename: "voice_" + m.id + ".ogg",
			URL: "https://mmg.whatsapp.net/v/x.enc", Mimetype: "audio/ogg; codecs=opus",
			Seconds: 7, IsPTT: true,
		}); err != nil {
			t.Fatal(err)
		}

		rel := filepath.Join("media", "972547669192", m.id+".ogg")
		if m.writeFile {
			abs := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte("OggS-voice-"+m.id), 0644); err != nil {
				t.Fatal(err)
			}
		}
		rec := &database.MediaDownloadRecord{MessageID: m.id, ChatJID: m.chat, Status: m.status, Error: m.errMsg}
		if m.status == "ok" {
			rec.LocalPath = rel
			rec.DownloadedAt = &downloadedToday
		}
		if err := media.RecordDownload(rec); err != nil {
			t.Fatal(err)
		}
	}

	return &Server{messageStore: messages, mediaStore: media, storeDir: dir,
		publicBaseURL: "http://whatsapp-mcp-whatsapp-bridge-1:8080"}
}

type downloadsResponse struct {
	Data   []downloadItem `json:"data"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func listDownloads(t *testing.T, server *Server, query string) (int, downloadsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/downloads"+query, nil)
	rec := httptest.NewRecorder()
	server.handleDownloads(rec, req)

	var body downloadsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, body
}

func ids(items []downloadItem) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.MessageID
	}
	return strings.Join(out, ",")
}

// By default: successful downloads only, newest message first, with everything the view
// shows about each.
func TestDownloadsDefault(t *testing.T) {
	server := downloadsServer(t)

	code, body := listDownloads(t, server, "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := ids(body.Data); got != "NEWEST,GONE,MINE,GROUP,OLDEST" {
		t.Fatalf("order = %s, want newest message first and failures excluded", got)
	}
	if body.Total != 5 {
		t.Fatalf("total = %d, want 5", body.Total)
	}

	newest := body.Data[0]
	if newest.ChatName != "Dana" || newest.Sender != "62156783493249" || newest.Seconds != 7 || !newest.IsPTT {
		t.Fatalf("metadata not joined: %+v", newest)
	}
	if newest.Timestamp != "2026-09-20T12:00:00Z" {
		t.Fatalf("timestamp = %q, want the message's send time", newest.Timestamp)
	}
	if newest.MediaPath != "972547669192/NEWEST.ogg" {
		t.Fatalf("media_path = %q", newest.MediaPath)
	}
	if newest.SizeBytes != int64(len("OggS-voice-NEWEST")) {
		t.Fatalf("size_bytes = %d", newest.SizeBytes)
	}
	// The absolute URL uses PUBLIC_BASE_URL, for API consumers on the Docker network.
	if newest.DownloadURL != "http://whatsapp-mcp-whatsapp-bridge-1:8080/api/media/972547669192/NEWEST.ogg" {
		t.Fatalf("download_url = %q", newest.DownloadURL)
	}
	if !body.Data[2].IsFromMe {
		t.Fatalf("MINE should be marked from me: %+v", body.Data[2])
	}
}

// A record whose file was deleted is shown honestly rather than as playable.
func TestDownloadsFlagsMissingFile(t *testing.T) {
	server := downloadsServer(t)
	_, body := listDownloads(t, server, "")

	for _, it := range body.Data {
		if it.MessageID != "GONE" {
			continue
		}
		if !it.FileMissing || it.MediaPath != "" || it.DownloadURL != "" {
			t.Fatalf("missing file not flagged, or still offered for playback: %+v", it)
		}
		return
	}
	t.Fatal("GONE not listed")
}

func TestDownloadsStatusFilter(t *testing.T) {
	server := downloadsServer(t)

	_, failed := listDownloads(t, server, "?status=failed")
	if ids(failed.Data) != "EXPIRED" || failed.Data[0].Error != "direct download expired" {
		t.Fatalf("status=failed returned %+v", failed.Data)
	}
	if failed.Data[0].MediaPath != "" {
		t.Fatal("a failed download must not offer a media path")
	}

	_, all := listDownloads(t, server, "?status=all")
	if all.Total != 6 {
		t.Fatalf("status=all total = %d, want 6", all.Total)
	}

	if code, _ := listDownloads(t, server, "?status=bogus"); code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d, want 400", code)
	}
}

// Dates mean when the message was sent. Every file here was downloaded on the 21st, so a
// filter that used download time would return all or nothing.
func TestDownloadsDateFilterUsesSendTime(t *testing.T) {
	server := downloadsServer(t)

	_, body := listDownloads(t, server, "?after=2026-09-16&before=2026-09-19")
	if got := ids(body.Data); got != "MINE,GROUP" {
		t.Fatalf("got %s, want the messages sent 16–18 Sep", got)
	}
	if body.Total != 2 {
		t.Fatalf("total = %d, want 2", body.Total)
	}

	if code, _ := listDownloads(t, server, "?after=yesterday"); code != http.StatusBadRequest {
		t.Fatalf("unparseable date: %d, want 400", code)
	}
}

// Filtering by a phone number must reach the LID chat the contact actually writes from.
func TestDownloadsChatFilterExpandsLID(t *testing.T) {
	server := downloadsServer(t)

	_, byPhone := listDownloads(t, server, "?chat=972547669192")
	if got := ids(byPhone.Data); got != "NEWEST,GONE,MINE,OLDEST" {
		t.Fatalf("phone filter got %s, want every file in the LID chat and nothing from the group", got)
	}

	_, byGroup := listDownloads(t, server, "?chat="+groupChat)
	if ids(byGroup.Data) != "GROUP" {
		t.Fatalf("group filter got %s", ids(byGroup.Data))
	}
}

func TestDownloadsPaging(t *testing.T) {
	server := downloadsServer(t)

	_, first := listDownloads(t, server, "?limit=2")
	_, second := listDownloads(t, server, "?limit=2&offset=2")
	_, last := listDownloads(t, server, "?limit=2&offset=4")
	_, past := listDownloads(t, server, "?limit=2&offset=99")

	if ids(first.Data) != "NEWEST,GONE" || ids(second.Data) != "MINE,GROUP" || ids(last.Data) != "OLDEST" {
		t.Fatalf("pages: %s | %s | %s", ids(first.Data), ids(second.Data), ids(last.Data))
	}
	if first.Total != 5 || second.Total != 5 {
		t.Fatal("total must be the whole filtered count on every page")
	}
	if len(past.Data) != 0 {
		t.Fatalf("offset past the end returned %s", ids(past.Data))
	}
}

func TestDownloadsValidation(t *testing.T) {
	server := downloadsServer(t)

	for _, q := range []string{"?limit=0", "?limit=abc", "?offset=-1", "?offset=x"} {
		if code, _ := listDownloads(t, server, q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	if _, body := listDownloads(t, server, "?limit=100000"); body.Limit != maxDownloadsLimit {
		t.Errorf("limit not capped: %d", body.Limit)
	}
}

// ?download=1 must make the browser save the file; without it the player streams inline.
func TestServeMediaDownloadFlag(t *testing.T) {
	server := downloadsServer(t)

	serve := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/media/972547669192/NEWEST.ogg"+query, nil)
		rec := httptest.NewRecorder()
		server.handleServeMedia(rec, req)
		return rec
	}

	inline := serve("")
	if inline.Code != http.StatusOK {
		t.Fatalf("inline status %d", inline.Code)
	}
	if cd := inline.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("inline response carries Content-Disposition %q", cd)
	}

	attached := serve("?download=1")
	if attached.Code != http.StatusOK {
		t.Fatalf("download status %d", attached.Code)
	}
	if cd := attached.Header().Get("Content-Disposition"); cd != "attachment; filename=NEWEST.ogg" {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}
