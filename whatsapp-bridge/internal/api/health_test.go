package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/retention"
	"whatsapp-bridge/internal/types"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// fakePool stands in for the auto-download manager.
type fakePool struct{ workers, depth int }

func (f fakePool) Workers() int    { return f.workers }
func (f fakePool) QueueDepth() int { return f.depth }

func healthServer(t *testing.T) *Server {
	t.Helper()
	store, err := database.NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Server{messageStore: store}
}

func getHealth(t *testing.T, server *Server) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	var body map[string]interface{}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, body
}

// The schema version is the whole point: it is how a deploy is proven to have taken effect.
func TestHealthReportsSchemaVersion(t *testing.T) {
	server := healthServer(t)

	code, body := getHealth(t, server)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}

	want, err := server.messageStore.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if got := int(body["schema_version"].(float64)); got != want {
		t.Fatalf("schema_version = %d, want %d", got, want)
	}
	if want == 0 {
		t.Fatal("a migrated store should report a non-zero schema version")
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}
}

func TestHealthReportsAutoDownloadPool(t *testing.T) {
	server := healthServer(t)

	// No pool attached: auto-download is off, and no worker counts are invented.
	_, body := getHealth(t, server)
	off := body["auto_download"].(map[string]interface{})
	if off["enabled"] != false {
		t.Fatalf("auto_download = %v, want disabled", off)
	}
	if _, present := off["workers"]; present {
		t.Error("worker count reported while auto-download is disabled")
	}

	server.SetAutoDownloadStatus(fakePool{workers: 2, depth: 7})
	_, body = getHealth(t, server)
	on := body["auto_download"].(map[string]interface{})
	if on["enabled"] != true {
		t.Fatalf("auto_download = %v, want enabled", on)
	}
	if int(on["workers"].(float64)) != 2 || int(on["queue_depth"].(float64)) != 7 {
		t.Fatalf("auto_download = %v, want 2 workers and depth 7", on)
	}
}

// With no client wired in, health must answer rather than panic.
func TestHealthWithoutClient(t *testing.T) {
	server := healthServer(t)
	code, body := getHealth(t, server)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if body["whatsapp_connected"] != false {
		t.Fatalf("whatsapp_connected = %v, want false", body["whatsapp_connected"])
	}
}

func TestHealthRejectsNonGET(t *testing.T) {
	server := healthServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestHealthReportsRetention(t *testing.T) {
	server := healthServer(t)

	_, body := getHealth(t, server)
	if off := body["retention"].(map[string]interface{}); off["enabled"] != false {
		t.Fatalf("retention = %v, want disabled with no sweeper", off)
	}

	media, err := database.NewMediaStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	server.SetRetention(retention.New(server.messageStore, media, t.TempDir(), waLog.Noop,
		retention.Options{DryRun: true, Interval: 90 * time.Minute}))

	_, body = getHealth(t, server)
	on := body["retention"].(map[string]interface{})
	if on["enabled"] != true || on["dry_run"] != true || int(on["interval_minutes"].(float64)) != 90 {
		t.Fatalf("retention = %v, want enabled, dry run, every 90 minutes", on)
	}
}

// The preview lists what would go and removes nothing.
func TestRetentionPreview(t *testing.T) {
	server := healthServer(t)
	dir := t.TempDir()
	media, err := database.NewMediaStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })

	rule := &types.WebhookConfig{Name: "1 day", Enabled: true, AutoDownload: true, RetentionDays: 1}
	if err := server.messageStore.StoreWebhookConfig(rule); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("media", "c", "OLD.ogg")
	if err := os.MkdirAll(filepath.Join(dir, "media", "c"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("OggS"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := media.RecordDownload(&database.MediaDownloadRecord{MessageID: "OLD", ChatJID: "c@lid", Status: "ok", LocalPath: rel}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.messageStore.GetDB().Exec(`INSERT INTO webhook_logs
		(webhook_config_id, message_id, chat_jid, delivered_at, created_at, event_type)
		VALUES (?, 'OLD', 'c@lid', '2026-01-01 00:00:00', '2026-01-01 00:00:00', 'auto_download')`, rule.ID); err != nil {
		t.Fatal(err)
	}
	server.SetRetention(retention.New(server.messageStore, media, dir, waLog.Noop, retention.Options{}))

	req := httptest.NewRequest(http.MethodGet, "/api/retention/preview", nil)
	rec := httptest.NewRecorder()
	server.handleRetentionPreview(rec, req)
	var body struct {
		Due  int                   `json:"due"`
		Data []retention.Candidate `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if body.Due != 1 || len(body.Data) != 1 || body.Data[0].MessageID != "OLD" || body.Data[0].RuleName != "1 day" {
		t.Fatalf("preview = %+v", body)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatal("the preview removed the file")
	}
}
