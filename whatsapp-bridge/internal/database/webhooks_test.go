package database

import (
	"database/sql"
	"os"
	"testing"
	"whatsapp-bridge/internal/types"
)

func TestUpdateWebhookConfig(t *testing.T) {
	// Create a temporary database for testing
	tempDB := "test_webhooks.db"
	defer os.Remove(tempDB)

	db, err := sql.Open("sqlite3", tempDB)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	defer db.Close()

	// Create tables
	err = createTables(db)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	store := &MessageStore{db: db}

	// Create initial webhook config
	config := &types.WebhookConfig{
		Name:        "Test Webhook",
		WebhookURL:  "https://example.com/webhook",
		SecretToken: "secret123",
		Enabled:     true,
		Triggers: []types.WebhookTrigger{
			{
				TriggerType:  "keyword",
				TriggerValue: "test",
				MatchType:    "contains",
				Enabled:      true,
			},
		},
	}

	// Store the initial config
	err = store.StoreWebhookConfig(config)
	if err != nil {
		t.Fatalf("Failed to store initial webhook config: %v", err)
	}

	initialID := config.ID
	if initialID == 0 {
		t.Fatal("Config ID should be set after storing")
	}

	// Update the config with new triggers
	config.Name = "Updated Test Webhook"
	config.WebhookURL = "https://example.com/updated"
	config.SecretToken = "newsecret456"
	config.Triggers = []types.WebhookTrigger{
		{
			TriggerType:  "keyword",
			TriggerValue: "urgent",
			MatchType:    "contains",
			Enabled:      true,
		},
		{
			TriggerType:  "sender",
			TriggerValue: "123456@s.whatsapp.net",
			MatchType:    "exact",
			Enabled:      true,
		},
	}

	// Update the config
	err = store.UpdateWebhookConfig(config)
	if err != nil {
		t.Fatalf("Failed to update webhook config: %v", err)
	}

	// Retrieve the updated config
	updatedConfig, err := store.GetWebhookConfig(initialID)
	if err != nil {
		t.Fatalf("Failed to retrieve updated webhook config: %v", err)
	}

	// Verify the main config was updated
	if updatedConfig.Name != "Updated Test Webhook" {
		t.Errorf("Expected name 'Updated Test Webhook', got '%s'", updatedConfig.Name)
	}
	if updatedConfig.WebhookURL != "https://example.com/updated" {
		t.Errorf("Expected URL 'https://example.com/updated', got '%s'", updatedConfig.WebhookURL)
	}
	if updatedConfig.SecretToken != "newsecret456" {
		t.Errorf("Expected secret 'newsecret456', got '%s'", updatedConfig.SecretToken)
	}

	// Verify the triggers were updated
	if len(updatedConfig.Triggers) != 2 {
		t.Errorf("Expected 2 triggers, got %d", len(updatedConfig.Triggers))
	}

	// Check first trigger
	if updatedConfig.Triggers[0].TriggerValue != "urgent" {
		t.Errorf("Expected first trigger value 'urgent', got '%s'", updatedConfig.Triggers[0].TriggerValue)
	}

	// Check second trigger
	if updatedConfig.Triggers[1].TriggerType != "sender" {
		t.Errorf("Expected second trigger type 'sender', got '%s'", updatedConfig.Triggers[1].TriggerType)
	}
	if updatedConfig.Triggers[1].TriggerValue != "123456@s.whatsapp.net" {
		t.Errorf("Expected second trigger value '123456@s.whatsapp.net', got '%s'", updatedConfig.Triggers[1].TriggerValue)
	}

	// Verify all triggers have proper IDs
	for i, trigger := range updatedConfig.Triggers {
		if trigger.ID == 0 {
			t.Errorf("Trigger %d should have a valid ID", i)
		}
		if trigger.WebhookConfigID != initialID {
			t.Errorf("Trigger %d should have webhook_config_id %d, got %d", i, initialID, trigger.WebhookConfigID)
		}
	}

	t.Log("✓ Webhook update test passed successfully")
}

// TestWebhookConfigAutoDownloadRoundTrip covers the schema-v2 fields through the three
// paths the API uses: create, read back, and update.
func TestWebhookConfigAutoDownloadRoundTrip(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	voiceOnly := false
	config := &types.WebhookConfig{
		Name:                  "Capture a contact's voice notes",
		WebhookURL:            "", // download-only listener
		Enabled:               true,
		AutoDownload:          true,
		AutoDownloadVoiceOnly: &voiceOnly,
		Triggers: []types.WebhookTrigger{
			{TriggerType: "sender", TriggerValue: "972523996302", MatchType: "exact", Enabled: true},
		},
	}
	if err := store.StoreWebhookConfig(config); err != nil {
		t.Fatalf("StoreWebhookConfig: %v", err)
	}

	got, err := store.GetWebhookConfig(config.ID)
	if err != nil {
		t.Fatalf("GetWebhookConfig: %v", err)
	}
	if !got.AutoDownload {
		t.Error("auto_download did not survive the round trip")
	}
	if got.VoiceOnly() {
		t.Error("auto_download_voice_only=false did not survive the round trip")
	}
	if got.WebhookURL != "" {
		t.Errorf("webhook_url = %q, want empty", got.WebhookURL)
	}

	// The same values must come back from the list the manager loads.
	all, err := store.GetAllWebhookConfigs()
	if err != nil {
		t.Fatalf("GetAllWebhookConfigs: %v", err)
	}
	if len(all) != 1 || !all[0].AutoDownload || all[0].VoiceOnly() {
		t.Fatalf("GetAllWebhookConfigs returned %+v", all)
	}

	// Turning capture off through an update.
	got.AutoDownload = false
	got.WebhookURL = "https://example.com/hook"
	if err := store.UpdateWebhookConfig(got); err != nil {
		t.Fatalf("UpdateWebhookConfig: %v", err)
	}
	after, err := store.GetWebhookConfig(config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoDownload {
		t.Error("auto_download stayed on after being updated off")
	}
}

// A client that omits auto_download_voice_only must get the column default (voice notes
// only), never "all audio".
func TestWebhookConfigVoiceOnlyDefaultsToTrue(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	config := &types.WebhookConfig{
		Name:         "no voice_only field sent",
		Enabled:      true,
		AutoDownload: true,
		Triggers:     []types.WebhookTrigger{{TriggerType: "all", MatchType: "exact", Enabled: true}},
	}
	if config.AutoDownloadVoiceOnly != nil {
		t.Fatal("fixture should leave the field unset")
	}
	if err := store.StoreWebhookConfig(config); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetWebhookConfig(config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.VoiceOnly() {
		t.Fatal("an unset auto_download_voice_only was stored as false; it must default to true")
	}
}

// Trigger mode round-trips through create and update, and an unset mode is stored as "or".
func TestWebhookTriggerModeRoundTrip(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	config := &types.WebhookConfig{Name: "l", WebhookURL: "https://example.com", Enabled: true}
	if err := store.StoreWebhookConfig(config); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetWebhookConfig(config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TriggerMode != "or" {
		t.Fatalf("unset trigger_mode stored as %q, want or", got.TriggerMode)
	}

	config.TriggerMode = "and"
	if err := store.UpdateWebhookConfig(config); err != nil {
		t.Fatal(err)
	}
	all, err := store.GetAllWebhookConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].TriggerMode != "and" {
		t.Fatalf("after update: %+v", all)
	}
}

// Triggers come back in the order they were saved: the payload's "first matching trigger"
// depends on it.
func TestWebhookTriggersKeepSavedOrder(t *testing.T) {
	store, err := NewMessageStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	values := []string{"z", "a", "m", "b", "y"}
	config := &types.WebhookConfig{Name: "l", WebhookURL: "https://example.com", Enabled: true}
	for _, v := range values {
		config.Triggers = append(config.Triggers, types.WebhookTrigger{
			TriggerType: "keyword", TriggerValue: v, MatchType: "exact", Enabled: true,
		})
	}
	if err := store.StoreWebhookConfig(config); err != nil {
		t.Fatal(err)
	}
	// Update rewrites every trigger; the order must survive that too.
	if err := store.UpdateWebhookConfig(config); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetWebhookTriggers(config.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, trigger := range got {
		if trigger.TriggerValue != values[i] {
			t.Fatalf("trigger %d = %q, want %q (order %v)", i, trigger.TriggerValue, values[i], got)
		}
	}
}
