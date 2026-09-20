package database

import (
	"strings"
	"testing"
	"time"

	"whatsapp-bridge/internal/types"
)

// TestDownloadQueriesAreReadOnly runs the two MessageStore methods the download code uses
// (see TestDownloadCodeOnlyReadsMessages in internal/whatsapp) on a connection where SQLite
// rejects every write, so any INSERT/UPDATE/DELETE/DDL they issued would fail the test.
func TestDownloadQueriesAreReadOnly(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	if err := store.StoreChat("c@s.whatsapp.net", "C", at); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := store.StoreMessage("A1", "c@s.whatsapp.net", "1", "", at, false,
		types.MediaMetadata{MediaType: "audio", Filename: "a.ogg", URL: "https://mmg.whatsapp.net/v/x.enc",
			MediaKey: key, FileSHA256: key, FileEncSHA256: key, FileLength: 1}); err != nil {
		t.Fatal(err)
	}

	// NewMessageStoreWithDir limits the pool to one connection, so every later statement runs on
	// the connection switched to query_only here
	store.db.SetMaxIdleConns(1)
	if _, err := store.db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}

	// The guard is armed: a normal write is rejected (content is needed, StoreMessage skips empty messages)
	err = store.StoreMessage("A2", "c@s.whatsapp.net", "1", "hello", at, false, types.MediaMetadata{})
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("write under query_only = %v; want a readonly error", err)
	}

	info, err := store.GetMediaInfo("A1", "c@s.whatsapp.net")
	if err != nil || info == nil || info.MediaType != "audio" {
		t.Fatalf("GetMediaInfo under query_only = %+v, %v", info, err)
	}
	list, err := store.ListAudioMessages([]string{"c@s.whatsapp.net", "c2@lid"}, "2026-01-01", "2026-12-31T00:00:00Z", 10, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAudioMessages under query_only = %d rows, %v", len(list), err)
	}
}
