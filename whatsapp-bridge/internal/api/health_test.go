package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-bridge/internal/database"
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
