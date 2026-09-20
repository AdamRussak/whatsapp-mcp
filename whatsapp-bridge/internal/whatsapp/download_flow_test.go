package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	"whatsapp-bridge/internal/database"
	bridgetypes "whatsapp-bridge/internal/types"
)

const testChat = "972500000001@s.whatsapp.net"

// newDownloadFixture returns stores in a temp dir holding one audio message "AUD1" (from me)
// and one text message "TXT1". The Client has no whatsmeow connection: any network call
// panics, which proves the paths under test never reach WhatsApp.
func newDownloadFixture(t *testing.T, mediaKey []byte) (string, *Client, *database.MessageStore, *database.MediaStore, time.Time) {
	t.Helper()
	dir := t.TempDir()
	ms, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ms.Close() })
	media, err := database.NewMediaStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })

	at := time.Date(2026, 9, 18, 10, 20, 48, 0, time.UTC)
	if err := ms.StoreChat(testChat, "Test", at); err != nil {
		t.Fatal(err)
	}
	hash := make([]byte, 32)
	if err := ms.StoreMessage("AUD1", testChat, "972500000001", "", at, true,
		bridgetypes.MediaMetadata{MediaType: "audio", Filename: "audio_x.ogg",
			URL:      "https://mmg.whatsapp.net/v/t62.7117-24/x.enc?ccb=11-4&oh=o&oe=e",
			MediaKey: mediaKey, FileSHA256: hash, FileEncSHA256: hash, FileLength: 100}); err != nil {
		t.Fatal(err)
	}
	if err := ms.StoreMessage("TXT1", testChat, "972500000001", "hi", at, false, bridgetypes.MediaMetadata{}); err != nil {
		t.Fatal(err)
	}
	return dir, &Client{}, ms, media, at
}

func TestDownloadMediaCacheHitOnDisk(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))

	// Where DownloadMedia names the file: <folder>/<UTC ts>_<me|them>_<id>.ogg
	rel := filepath.Join("media", "972500000001", "2026-09-18_102048_me_AUD1.ogg")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("OggS-cached"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := c.DownloadMedia(context.Background(), media, ms, "AUD1", testChat, dir)
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if !res.Cached || res.Path != rel || res.Size != len("OggS-cached") {
		t.Errorf("result = %+v, want cached %s", res, rel)
	}
}

func TestDownloadMediaCacheHitFromRecord(t *testing.T) {
	// A file recorded in media.db under another name (e.g. written by an earlier naming scheme)
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	rel := filepath.Join("media", "972500000001", "older_name.ogg")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("OggS"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := media.RecordDownload(&database.MediaDownloadRecord{
		MessageID: "AUD1", ChatJID: testChat, LocalPath: rel, SHA256: "abc", Status: database.DownloadStatusOK,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := c.DownloadMedia(context.Background(), media, ms, "AUD1", testChat, dir)
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if !res.Cached || res.Path != rel || res.SHA256 != "abc" {
		t.Errorf("result = %+v, want cached %s with recorded sha", res, rel)
	}
}

func TestDownloadMediaIncompleteMetadata(t *testing.T) {
	// No media key: recorded as a permanent failure before any network access
	dir, c, ms, media, _ := newDownloadFixture(t, nil)

	if _, err := c.DownloadMedia(context.Background(), media, ms, "AUD1", testChat, dir); err == nil {
		t.Fatal("expected error for missing media key")
	}
	rec, err := media.GetDownload("AUD1", testChat)
	if err != nil || rec == nil || rec.Status != database.DownloadStatusIncompleteMetadata {
		t.Fatalf("record = %+v, %v; want %s", rec, err, database.DownloadStatusIncompleteMetadata)
	}
	if !database.IsPermanentDownloadFailure(rec.Status) {
		t.Error("incomplete metadata should be skipped on re-runs")
	}
}

func TestDownloadMediaRejectsNonAudio(t *testing.T) {
	dir, c, ms, media, _ := newDownloadFixture(t, make([]byte, 32))
	for _, id := range []string{"TXT1", "MISSING"} {
		if _, err := c.DownloadMedia(context.Background(), media, ms, id, testChat, dir); err == nil {
			t.Errorf("DownloadMedia(%s): expected error", id)
		}
		if rec, _ := media.GetDownload(id, testChat); rec != nil {
			t.Errorf("DownloadMedia(%s) recorded %+v; nothing should be recorded", id, rec)
		}
	}
}

func TestMediaRetryWaiter(t *testing.T) {
	c := &Client{}
	ch := c.RegisterMediaRetryWaiter("MSG1")

	c.HandleMediaRetry(&events.MediaRetry{MessageID: "OTHER"})
	c.HandleMediaRetry(&events.MediaRetry{MessageID: "MSG1"})
	select {
	case evt := <-ch:
		if evt.MessageID != "MSG1" {
			t.Errorf("waiter got event for %s", evt.MessageID)
		}
	default:
		t.Fatal("waiter did not receive its event")
	}
	select {
	case evt := <-ch:
		t.Errorf("waiter got an extra event for %s", evt.MessageID)
	default:
	}

	// A duplicate while the buffer is full must not block the whatsmeow event loop
	done := make(chan struct{})
	go func() {
		c.HandleMediaRetry(&events.MediaRetry{MessageID: "MSG1"})
		c.HandleMediaRetry(&events.MediaRetry{MessageID: "MSG1"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleMediaRetry blocked on a full waiter")
	}

	c.UnregisterMediaRetryWaiter("MSG1")
	c.HandleMediaRetry(&events.MediaRetry{MessageID: "MSG1"}) // no waiter: dropped
	c.HandleMediaRetry(nil)
}

func TestMediaRetryTimeoutDefault(t *testing.T) {
	c := &Client{}
	if got := c.MediaRetryTimeout(); got != defaultMediaRetryTimeout {
		t.Errorf("default = %v, want %v", got, defaultMediaRetryTimeout)
	}
	c.SetMediaRetryTimeout(45 * time.Second)
	if got := c.MediaRetryTimeout(); got != 45*time.Second {
		t.Errorf("after set = %v, want 45s", got)
	}
}
