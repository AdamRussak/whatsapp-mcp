package webhook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"whatsapp-bridge/internal/types"
)

// ValidateWebhookConfig validates a webhook configuration. It collects every failure rather
// than stopping at the first, and returns them as ValidationErrors (nil when valid), so
// the form can show each one next to the trigger it concerns.
//
// The basic checks come first: name, URL, retention, trigger_mode and each trigger's
// syntax. Only when every trigger passes those do R1–R6 (ADR 0001) run, since they reason
// about what the triggers could match.
func (wm *Manager) ValidateWebhookConfig(config *types.WebhookConfig) error {
	var errs ValidationErrors
	form := func(format string, args ...interface{}) {
		errs = append(errs, ValidationError{TriggerIndex: -1, Message: fmt.Sprintf(format, args...)})
	}

	if config.Name == "" {
		form("webhook name is required")
	} else if len(config.Name) > 255 {
		form("webhook name must be less than 256 characters")
	}

	// A config with auto-download on is a valid listener without a URL: it captures the
	// matched media and delivers nothing. Any other config still needs somewhere to post.
	if config.WebhookURL == "" && !config.AutoDownload {
		form("webhook URL is required unless auto-download is enabled")
	}

	if config.WebhookURL != "" {
		if len(config.WebhookURL) > 2048 {
			form("webhook URL must be less than 2048 characters")
		} else if !strings.HasPrefix(config.WebhookURL, "http://") && !strings.HasPrefix(config.WebhookURL, "https://") {
			form("webhook URL must start with http:// or https://")
		}
	}

	// Ten years is a generous ceiling; the point is to catch a typo, not to limit anyone.
	if config.RetentionDays < 0 || config.RetentionDays > 3650 {
		form("retention_days must be between 0 (keep forever) and 3650")
	}

	// Anything but "or" or "and" is refused, never quietly treated as OR.
	mode := config.Mode()
	modeOK := mode == types.TriggerModeOr || mode == types.TriggerModeAnd
	if !modeOK {
		form(`trigger_mode must be "or" or "and", not %q`, config.TriggerMode)
	}

	triggersOK := true
	for i, trigger := range config.Triggers {
		if msg := triggerSyntaxProblem(trigger); msg != "" {
			errs = append(errs, ValidationError{TriggerIndex: i, Message: msg})
			triggersOK = false
		}
	}
	if triggersOK {
		errs = append(errs, wm.checkTriggerRules(config, modeOK)...)
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// triggerSyntaxProblem checks one trigger on its own: a known type and match type, a regex
// that compiles, and a value on an enabled trigger that needs one. An empty value is not
// harmless: "contains" nothing matches every message.
func triggerSyntaxProblem(trigger types.WebhookTrigger) string {
	switch trigger.TriggerType {
	case "":
		return "trigger type is required"
	case "all", "chat_jid", "sender", "keyword", "media_type":
	default:
		return fmt.Sprintf("invalid trigger type: %s", trigger.TriggerType)
	}

	switch trigger.MatchType {
	case "exact", "contains", "regex":
	default:
		return fmt.Sprintf("invalid match type: %s", trigger.MatchType)
	}

	if trigger.MatchType == "regex" && trigger.TriggerValue != "" {
		if _, err := regexp.Compile(trigger.TriggerValue); err != nil {
			return fmt.Sprintf("invalid regex pattern '%s': %v", trigger.TriggerValue, err)
		}
	}

	if trigger.Enabled && trigger.TriggerType != "all" && strings.TrimSpace(trigger.TriggerValue) == "" {
		return "enter a value for this trigger, or remove it"
	}
	return ""
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
