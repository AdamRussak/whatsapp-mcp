package database

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"whatsapp-bridge/internal/types"

	_ "github.com/mattn/go-sqlite3"
)

// oldSchema is messages.db exactly as it looked before schema v1: no direct_path,
// mimetype, seconds or is_ptt, and no schema_migrations table.
const oldSchema = `
	CREATE TABLE chats (
		jid TEXT PRIMARY KEY,
		name TEXT,
		last_message_time TIMESTAMP
	);
	CREATE TABLE messages (
		id TEXT,
		chat_jid TEXT,
		sender TEXT,
		content TEXT,
		timestamp TIMESTAMP,
		is_from_me BOOLEAN,
		media_type TEXT,
		filename TEXT,
		url TEXT,
		media_key BLOB,
		file_sha256 BLOB,
		file_enc_sha256 BLOB,
		file_length INTEGER,
		PRIMARY KEY (id, chat_jid),
		FOREIGN KEY (chat_jid) REFERENCES chats(jid)
	);
`

// seedOldSchemaDB writes a pre-v1 messages.db with one audio row and one text row,
// then returns its directory.
func seedOldSchemaDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	db, err := sql.Open("sqlite3", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)`,
		"c@s.whatsapp.net", "C", at); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := db.Exec(`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"OLD1", "c@s.whatsapp.net", "1", "", at, false, "audio", "audio_20260304_050607.ogg",
		"https://mmg.whatsapp.net/v/old.enc", key, key, key, 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"OLD2", "c@s.whatsapp.net", "1", "hello", at, true, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	return dir
}

// dumpMessages renders every message row as text, so two dumps can be compared the way
// the live migration was checked with diff (§4.6 step 6).
func dumpMessages(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type,
		filename, url, hex(media_key), hex(file_sha256), hex(file_enc_sha256), file_length
		FROM messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out string
	for rows.Next() {
		var id, chatJID, sender, content, ts, mediaType, filename, url, mk, sha, esha string
		var isFromMe bool
		var fileLength sql.NullInt64
		if err := rows.Scan(&id, &chatJID, &sender, &content, &ts, &isFromMe, &mediaType,
			&filename, &url, &mk, &sha, &esha, &fileLength); err != nil {
			t.Fatal(err)
		}
		out += id + "|" + chatJID + "|" + sender + "|" + content + "|" + ts + "|" +
			mediaType + "|" + filename + "|" + url + "|" + mk + "|" + sha + "|" + esha + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigrationLeavesExistingRowsUntouched is the unit-test form of the §4.6 before/after
// diff: migrating a pre-v1 database adds the columns without rewriting a single row.
func TestMigrationLeavesExistingRowsUntouched(t *testing.T) {
	dir := seedOldSchemaDB(t)

	raw, err := sql.Open("sqlite3", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	before := dumpMessages(t, raw)
	raw.Close()

	store, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer store.Close()

	if after := dumpMessages(t, store.db); after != before {
		t.Errorf("rows changed across migration:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	v, err := store.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if want := len(migrations); v != want {
		t.Errorf("schema version = %d, want %d", v, want)
	}

	// The v1 columns exist and read NULL for rows that predate them
	for _, col := range []string{"direct_path", "mimetype", "seconds", "is_ptt"} {
		var n int
		if err := store.db.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE ` + col + ` IS NOT NULL`).Scan(&n); err != nil {
			t.Fatalf("column %s missing after migration: %v", col, err)
		}
		if n != 0 {
			t.Errorf("column %s: %d rows non-NULL, want 0 (no row should be rewritten)", col, n)
		}
	}
}

// TestMigrationIsIdempotent reopens an already-migrated database: no migration reruns and
// nothing changes.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := seedOldSchemaDB(t)

	first, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := dumpMessages(t, first.db)
	first.Close()

	second, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	if after := dumpMessages(t, second.db); after != before {
		t.Errorf("second open changed rows:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var applied int
	if err := second.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) {
		t.Errorf("schema_migrations has %d rows after two opens, want %d", applied, len(migrations))
	}
}

// TestStoreMessageReplayKeepsExistingRow covers the §4.3 hazard: a history-sync replay of
// a message we already hold must not rewrite it. Only v1 columns that are still NULL get
// filled in.
func TestStoreMessageReplayKeepsExistingRow(t *testing.T) {
	dir := seedOldSchemaDB(t)
	store, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	before := dumpMessages(t, store.db)

	// A replay arrives with different content, a new filename and fresh media material,
	// plus v1 metadata the original row never had.
	replayKey := []byte("REPLAYED-KEY-REPLAYED-KEY-32BYTE")
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	err = store.StoreMessage("OLD1", "c@s.whatsapp.net", "someone-else", "REWRITTEN", at, true,
		types.MediaMetadata{
			MediaType:     "audio",
			Filename:      "voice_OLD1.ogg",
			URL:           "https://mmg.whatsapp.net/v/REPLAY.enc",
			MediaKey:      replayKey,
			FileSHA256:    replayKey,
			FileEncSHA256: replayKey,
			FileLength:    99999,
			DirectPath:    "/v/t62/replay.enc",
			Mimetype:      "audio/ogg; codecs=opus",
			Seconds:       12,
			IsPTT:         true,
		})
	if err != nil {
		t.Fatal(err)
	}

	if after := dumpMessages(t, store.db); after != before {
		t.Errorf("replay rewrote an existing row:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// ...but the v1 columns, which were NULL, are now filled from the replay
	var directPath, mimetype string
	var seconds int
	var isPTT bool
	if err := store.db.QueryRow(
		`SELECT direct_path, mimetype, seconds, is_ptt FROM messages WHERE id = 'OLD1'`,
	).Scan(&directPath, &mimetype, &seconds, &isPTT); err != nil {
		t.Fatal(err)
	}
	if directPath != "/v/t62/replay.enc" || mimetype != "audio/ogg; codecs=opus" || seconds != 12 || !isPTT {
		t.Errorf("v1 columns not backfilled: direct_path=%q mimetype=%q seconds=%d is_ptt=%v",
			directPath, mimetype, seconds, isPTT)
	}

	// A second replay carrying nothing new must not blank them out again
	err = store.StoreMessage("OLD1", "c@s.whatsapp.net", "1", "x", at, false,
		types.MediaMetadata{MediaType: "audio"})
	if err != nil {
		t.Fatal(err)
	}
	var stillThere string
	if err := store.db.QueryRow(`SELECT direct_path FROM messages WHERE id = 'OLD1'`).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != "/v/t62/replay.enc" {
		t.Errorf("direct_path after empty replay = %q, want it kept", stillThere)
	}
}

// TestStoreMessageWritesV1Columns checks a brand-new voice note lands with its metadata.
func TestStoreMessageWritesV1Columns(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := store.StoreChat("c@s.whatsapp.net", "C", at); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := store.StoreMessage("NEW1", "c@s.whatsapp.net", "1", "", at, false,
		types.MediaMetadata{
			MediaType: "audio", Filename: "voice_NEW1.ogg", URL: "https://mmg.whatsapp.net/v/new.enc",
			MediaKey: key, FileSHA256: key, FileEncSHA256: key, FileLength: 1024,
			DirectPath: "/v/t62/new.enc", Mimetype: "audio/ogg; codecs=opus", Seconds: 7, IsPTT: true,
		}); err != nil {
		t.Fatal(err)
	}

	var directPath, mimetype, filename string
	var seconds int
	var isPTT bool
	if err := store.db.QueryRow(
		`SELECT direct_path, mimetype, seconds, is_ptt, filename FROM messages WHERE id = 'NEW1'`,
	).Scan(&directPath, &mimetype, &seconds, &isPTT, &filename); err != nil {
		t.Fatal(err)
	}
	if directPath != "/v/t62/new.enc" || mimetype != "audio/ogg; codecs=opus" || seconds != 7 || !isPTT {
		t.Errorf("got direct_path=%q mimetype=%q seconds=%d is_ptt=%v", directPath, mimetype, seconds, isPTT)
	}
	if filename != "voice_NEW1.ogg" {
		t.Errorf("filename = %q, want voice_NEW1.ogg", filename)
	}

	// A text message leaves the audio columns NULL rather than writing a misleading 0/false
	if err := store.StoreMessage("TXT", "c@s.whatsapp.net", "1", "hi", at, false, types.MediaMetadata{}); err != nil {
		t.Fatal(err)
	}
	var isPTTNull, secondsNull bool
	if err := store.db.QueryRow(
		`SELECT is_ptt IS NULL, seconds IS NULL FROM messages WHERE id = 'TXT'`,
	).Scan(&isPTTNull, &secondsNull); err != nil {
		t.Fatal(err)
	}
	if !isPTTNull || !secondsNull {
		t.Errorf("text message: is_ptt IS NULL = %v, seconds IS NULL = %v; want both true", isPTTNull, secondsNull)
	}
}

// oldWebhookSchema is webhook_configs as it looked before schema v2: no auto-download
// columns. Its rows are live configuration, so the migration must leave them exactly as
// they are and only widen the table.
const oldWebhookSchema = `
	CREATE TABLE webhook_configs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		webhook_url TEXT NOT NULL,
		secret_token TEXT,
		enabled BOOLEAN DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE webhook_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		webhook_config_id INTEGER REFERENCES webhook_configs(id),
		message_id TEXT,
		chat_jid TEXT,
		trigger_type TEXT,
		trigger_value TEXT,
		payload TEXT,
		response_status INTEGER,
		response_body TEXT,
		attempt_count INTEGER DEFAULT 1,
		delivered_at TIMESTAMP,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
`

// TestMigrationV2AddsAutoDownloadColumns checks the schema-v2 half of §4.6 step 6: an
// existing webhook keeps working untouched, and defaults to capturing nothing.
func TestMigrationV2AddsAutoDownloadColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.db")

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldSchema + oldWebhookSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_configs (name, webhook_url, secret_token, enabled)
		VALUES (?, ?, ?, ?)`, "existing", "https://example.com/hook", "s3cret", true); err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	var name, url, token string
	var enabled, autoDownload, voiceOnly bool
	err = db.QueryRow(`SELECT name, webhook_url, secret_token, enabled, auto_download, auto_download_voice_only
		FROM webhook_configs WHERE id = 1`).Scan(&name, &url, &token, &enabled, &autoDownload, &voiceOnly)
	if err != nil {
		t.Fatalf("reading the migrated row: %v", err)
	}
	if name != "existing" || url != "https://example.com/hook" || token != "s3cret" || !enabled {
		t.Fatalf("the migration rewrote an existing webhook: %q %q %q %v", name, url, token, enabled)
	}
	// An existing webhook must not start downloading anything on its own.
	if autoDownload {
		t.Error("auto_download defaulted to on for an existing webhook")
	}
	if !voiceOnly {
		t.Error("auto_download_voice_only defaulted to off, so enabling capture would take all audio")
	}

	// Re-opening applies nothing further.
	if err := runMigrations(db); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
	var version int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	// Latest, whatever that is now: this test is about v2's columns, not about which
	// migration happens to be newest.
	if want := migrations[len(migrations)-1].version; version != want {
		t.Fatalf("schema version = %d, want %d", version, want)
	}
	db.Close()
}

// createTables carries the v2 columns too, so a fresh database needs no ALTER at all.
func TestFreshDatabaseHasAutoDownloadColumns(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var count int
	err = store.GetDB().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('webhook_configs')
		 WHERE name IN ('auto_download', 'auto_download_voice_only')`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("fresh webhook_configs has %d of the 2 auto-download columns", count)
	}
}

// TestMigrationV3AddsEventTypeToLogs checks that existing delivery logs keep their meaning:
// the column defaults to 'delivery', so nothing already recorded is reclassified.
func TestMigrationV3AddsEventTypeToLogs(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(oldSchema + oldWebhookSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_logs (webhook_config_id, message_id, chat_jid, response_status)
		VALUES (?, ?, ?, ?)`, 1, "OLDLOG", "c@s.whatsapp.net", 200); err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	var messageID, eventType string
	var status int
	if err := db.QueryRow(`SELECT message_id, response_status, event_type FROM webhook_logs WHERE id = 1`).
		Scan(&messageID, &status, &eventType); err != nil {
		t.Fatal(err)
	}
	if messageID != "OLDLOG" || status != 200 {
		t.Fatalf("the migration rewrote an existing log row: %q %d", messageID, status)
	}
	if eventType != "delivery" {
		t.Fatalf("event_type = %q, want delivery so old rows keep their meaning", eventType)
	}
}

// TestMigrationV5AddsTriggerMode checks ADR 0001's T1: a listener saved before v5 keeps
// its meaning — it reads "or", which is what every listener did before match modes.
func TestMigrationV5AddsTriggerMode(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldSchema + oldWebhookSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_configs (name, webhook_url, secret_token, enabled)
		VALUES (?, ?, ?, ?)`, "existing", "https://example.com/hook", "s3cret", true); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer store.Close()

	if v, err := store.SchemaVersion(); err != nil || v != 5 {
		t.Fatalf("SchemaVersion() = %d, %v; want 5", v, err)
	}

	config, err := store.GetWebhookConfig(1)
	if err != nil {
		t.Fatalf("reading the migrated listener: %v", err)
	}
	if config.Name != "existing" || config.WebhookURL != "https://example.com/hook" ||
		config.SecretToken != "s3cret" || !config.Enabled {
		t.Fatalf("the migration rewrote an existing listener: %+v", config)
	}
	if config.TriggerMode != "or" {
		t.Fatalf("trigger_mode = %q, want or", config.TriggerMode)
	}
}

// A fresh database is created at v5 with the column already in place.
func TestFreshDatabaseHasTriggerMode(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if v, err := store.SchemaVersion(); err != nil || v != 5 {
		t.Fatalf("SchemaVersion() = %d, %v; want 5", v, err)
	}
	var dflt sql.NullString
	if err := store.GetDB().QueryRow(
		`SELECT dflt_value FROM pragma_table_info('webhook_configs') WHERE name = 'trigger_mode'`,
	).Scan(&dflt); err != nil {
		t.Fatalf("fresh webhook_configs has no trigger_mode: %v", err)
	}
	if dflt.String != "'or'" {
		t.Fatalf("trigger_mode default = %q, want 'or'", dflt.String)
	}
}
