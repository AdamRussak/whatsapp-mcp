package webhook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"whatsapp-bridge/internal/types"
)

// ValidateWebhookConfig validates a webhook configuration
func (wm *Manager) ValidateWebhookConfig(config *types.WebhookConfig) error {
	if config.Name == "" {
		return fmt.Errorf("webhook name is required")
	}

	if len(config.Name) > 255 {
		return fmt.Errorf("webhook name must be less than 256 characters")
	}

	// A config with auto-download on is a valid listener without a URL: it captures the
	// matched media and delivers nothing. Any other config still needs somewhere to post.
	if config.WebhookURL == "" && !config.AutoDownload {
		return fmt.Errorf("webhook URL is required unless auto-download is enabled")
	}

	if config.WebhookURL != "" {
		if len(config.WebhookURL) > 2048 {
			return fmt.Errorf("webhook URL must be less than 2048 characters")
		}

		if !strings.HasPrefix(config.WebhookURL, "http://") && !strings.HasPrefix(config.WebhookURL, "https://") {
			return fmt.Errorf("webhook URL must start with http:// or https://")
		}
	}

	// Ten years is a generous ceiling; the point is to catch a typo, not to limit anyone.
	if config.RetentionDays < 0 || config.RetentionDays > 3650 {
		return fmt.Errorf("retention_days must be between 0 (keep forever) and 3650")
	}

	// Validate triggers
	for _, trigger := range config.Triggers {
		if trigger.TriggerType == "" {
			return fmt.Errorf("trigger type is required")
		}

		validTypes := []string{"all", "chat_jid", "sender", "keyword", "media_type"}
		valid := false
		for _, validType := range validTypes {
			if trigger.TriggerType == validType {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid trigger type: %s", trigger.TriggerType)
		}

		validMatchTypes := []string{"exact", "contains", "regex"}
		valid = false
		for _, validType := range validMatchTypes {
			if trigger.MatchType == validType {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid match type: %s", trigger.MatchType)
		}

		// Test regex patterns
		if trigger.MatchType == "regex" && trigger.TriggerValue != "" {
			_, err := regexp.Compile(trigger.TriggerValue)
			if err != nil {
				return fmt.Errorf("invalid regex pattern '%s': %v", trigger.TriggerValue, err)
			}
		}
	}

	return nil
}

// TestWebhook sends a test webhook to verify connectivity
func (wm *Manager) TestWebhook(config *types.WebhookConfig) error {
	if config.WebhookURL == "" {
		return fmt.Errorf("webhook %q has no URL to test: it is a download-only listener", config.Name)
	}

	testPayload := types.WebhookPayload{
		EventType: "test",
		Timestamp: time.Now().Format(time.RFC3339),
		WebhookConfig: types.WebhookConfigInfo{
			ID:   config.ID,
			Name: config.Name,
		},
		Message: types.WebhookMessageInfo{
			ID:         "test-message-id",
			ChatJID:    "test@s.whatsapp.net",
			ChatName:   "Test Chat",
			Sender:     "test",
			SenderName: "Test User",
			Content:    "This is a test message",
			Timestamp:  time.Now().Format(time.RFC3339),
			IsFromMe:   false,
		},
		Metadata: types.WebhookMetadata{
			DeliveryAttempt:  1,
			ProcessingTimeMs: 0,
		},
	}

	payloadBytes, err := json.Marshal(testPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal test payload: %v", err)
	}

	success, statusCode, responseBody := wm.delivery.sendHTTPRequest(config, payloadBytes)
	if !success {
		return fmt.Errorf("test webhook failed: status %d, response: %s", statusCode, responseBody)
	}

	return nil
}
