package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"
	"whatsapp-bridge/internal/webhook"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func webhookServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	store, err := database.NewMessageStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Server{messageStore: store, webhookManager: webhook.NewManager(store, waLog.Noop, "", dir)}
}

type validationResponse struct {
	Success bool                      `json:"success"`
	Error   string                    `json:"error"`
	Errors  []webhook.ValidationError `json:"errors"`
}

func call(t *testing.T, handler http.HandlerFunc, method, path, body string) (int, validationResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var resp validationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return rec.Code, resp
}

func countWebhooks(t *testing.T, server *Server) int {
	t.Helper()
	configs, err := server.messageStore.GetAllWebhookConfigs()
	if err != nil {
		t.Fatal(err)
	}
	return len(configs)
}

const twoChatsUnderAnd = `{"name":"l","webhook_url":"https://example.com","enabled":true,"trigger_mode":"and",
	"triggers":[
		{"trigger_type":"chat_jid","trigger_value":"111@s.whatsapp.net","match_type":"exact","enabled":true},
		{"trigger_type":"chat_jid","trigger_value":"222@s.whatsapp.net","match_type":"exact","enabled":true}]}`

// The same body through validate and through create returns the same errors, and neither
// writes anything.
func TestValidateAndCreateAgree(t *testing.T) {
	server := webhookServer(t)

	vCode, vResp := call(t, server.handleValidateWebhook, http.MethodPost, "/api/webhooks/validate", twoChatsUnderAnd)
	cCode, cResp := call(t, server.handleWebhooks, http.MethodPost, "/api/webhooks", twoChatsUnderAnd)

	if vCode != http.StatusBadRequest || cCode != http.StatusBadRequest {
		t.Fatalf("validate %d, create %d; want 400 from both", vCode, cCode)
	}
	if len(vResp.Errors) != 1 || vResp.Errors[0].Rule != "R3" || vResp.Errors[0].TriggerIndex != 1 {
		t.Fatalf("validate errors = %+v, want one R3 on trigger 1", vResp.Errors)
	}
	if !reflect.DeepEqual(vResp, cResp) {
		t.Fatalf("validate and create disagree:\n%+v\n%+v", vResp, cResp)
	}
	// The toast still has a string to show.
	if cResp.Success || cResp.Error != cResp.Errors[0].Message {
		t.Fatalf("error = %q, want the first message", cResp.Error)
	}
	if n := countWebhooks(t, server); n != 0 {
		t.Fatalf("%d listener(s) stored after a rejected create", n)
	}
}

// Update answers a rejected listener the same way.
func TestUpdateRejectsWithErrors(t *testing.T) {
	server := webhookServer(t)
	code, _ := call(t, server.handleWebhooks, http.MethodPost, "/api/webhooks",
		`{"name":"l","webhook_url":"https://example.com","enabled":true,"triggers":[{"trigger_type":"all","match_type":"exact","enabled":true}]}`)
	if code != http.StatusOK {
		t.Fatalf("create = %d", code)
	}
	configs, _ := server.messageStore.GetAllWebhookConfigs()
	id := configs[0].ID

	code, resp := call(t, server.handleWebhookByID, http.MethodPut, fmt.Sprintf("/api/webhooks/%d", id), twoChatsUnderAnd)
	if code != http.StatusBadRequest || len(resp.Errors) != 1 || resp.Errors[0].Rule != "R3" {
		t.Fatalf("update = %d %+v, want 400 with R3", code, resp)
	}
	stored, _ := server.messageStore.GetWebhookConfig(id)
	if stored.TriggerMode != "or" || len(stored.Triggers) != 1 {
		t.Fatalf("a rejected update changed the listener: %+v", stored)
	}
}

func TestValidateAcceptsWithoutInserting(t *testing.T) {
	server := webhookServer(t)
	code, resp := call(t, server.handleValidateWebhook, http.MethodPost, "/api/webhooks/validate",
		`{"name":"l","webhook_url":"https://example.com","trigger_mode":"and","triggers":[
			{"trigger_type":"chat_jid","trigger_value":"111@s.whatsapp.net","match_type":"exact","enabled":true},
			{"trigger_type":"media_type","trigger_value":"audio","match_type":"exact","enabled":true}]}`)
	if code != http.StatusOK || !resp.Success || resp.Errors == nil || len(resp.Errors) != 0 {
		t.Fatalf("validate = %d %+v, want 200 with an empty errors array", code, resp)
	}
	if n := countWebhooks(t, server); n != 0 {
		t.Fatalf("validate stored %d listener(s)", n)
	}
}

func TestUnknownTriggerModeIs400(t *testing.T) {
	server := webhookServer(t)
	body := `{"name":"l","webhook_url":"https://example.com","trigger_mode":"xor","triggers":[{"trigger_type":"all","match_type":"exact","enabled":true}]}`
	for name, handler := range map[string]http.HandlerFunc{
		"validate": server.handleValidateWebhook,
		"create":   server.handleWebhooks,
	} {
		code, resp := call(t, handler, http.MethodPost, "/api/webhooks/validate", body)
		if code != http.StatusBadRequest || len(resp.Errors) != 1 ||
			resp.Errors[0].Rule != "" || resp.Errors[0].TriggerIndex != -1 {
			t.Errorf("%s = %d %+v, want 400 with one form-level error", name, code, resp)
		}
	}
	if n := countWebhooks(t, server); n != 0 {
		t.Fatalf("stored %d listener(s)", n)
	}
}

func TestValidateRejectsGET(t *testing.T) {
	server := webhookServer(t)
	rec := httptest.NewRecorder()
	server.handleValidateWebhook(rec, httptest.NewRequest(http.MethodGet, "/api/webhooks/validate", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}

// Enable writes the stored listener back as it is: the mode survives, and the rules are not
// re-run, since enable does not change triggers (ADR 0001, Review).
func TestEnableKeepsModeAndDoesNotRevalidate(t *testing.T) {
	server := webhookServer(t)
	// Stored directly, bypassing validation: under AND two chats is an R3 failure.
	config := &types.WebhookConfig{
		Name: "legacy", WebhookURL: "https://example.com", Enabled: false, TriggerMode: "and",
		Triggers: []types.WebhookTrigger{
			{TriggerType: "chat_jid", TriggerValue: "111@s.whatsapp.net", MatchType: "exact", Enabled: true},
			{TriggerType: "chat_jid", TriggerValue: "222@s.whatsapp.net", MatchType: "exact", Enabled: true},
		},
	}
	if err := server.messageStore.StoreWebhookConfig(config); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	server.handleWebhookByID(rec, httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/%d/enable", config.ID), strings.NewReader(`{"enabled":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable = %d %s, want 200", rec.Code, rec.Body.String())
	}

	stored, err := server.messageStore.GetWebhookConfig(config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || stored.TriggerMode != "and" || len(stored.Triggers) != 2 {
		t.Fatalf("after enable: enabled=%v mode=%q triggers=%d", stored.Enabled, stored.TriggerMode, len(stored.Triggers))
	}
}
