package webhook

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"whatsapp-bridge/internal/autodownload"
	"whatsapp-bridge/internal/database"
	"whatsapp-bridge/internal/types"
	"whatsapp-bridge/internal/whatsapp"

	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// AutoDownloader receives the media of matched messages. It is satisfied by
// *autodownload.Manager; the interface keeps the webhook manager testable on its own.
type AutoDownloader interface {
	Enqueue(job autodownload.Job) bool
}

// Manager handles webhook processing and delivery
type Manager struct {
	messageStore   *database.MessageStore
	logger         waLog.Logger
	configs        []*types.WebhookConfig
	mutex          sync.RWMutex
	delivery       *DeliveryService
	publicBaseURL  string
	autoDownloader AutoDownloader
}

// NewManager creates a new webhook manager. publicBaseURL is the address webhook
// consumers can reach this bridge on; it appears in media_download_url.
func NewManager(messageStore *database.MessageStore, logger waLog.Logger, publicBaseURL string) *Manager {
	if publicBaseURL == "" {
		publicBaseURL = "http://localhost:8080"
	}
	return &Manager{
		messageStore:  messageStore,
		logger:        logger,
		configs:       make([]*types.WebhookConfig, 0),
		delivery:      NewDeliveryService(messageStore, logger),
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

// SetAutoDownloader attaches the auto-download pool. Left unset (or set to nil), configs
// with auto_download on still match and deliver; nothing is captured.
func (wm *Manager) SetAutoDownloader(ad AutoDownloader) {
	wm.mutex.Lock()
	defer wm.mutex.Unlock()
	wm.autoDownloader = ad
}

// LoadWebhookConfigs loads webhook configurations from database
func (wm *Manager) LoadWebhookConfigs() error {
	wm.mutex.Lock()
	defer wm.mutex.Unlock()

	configs, err := wm.messageStore.GetAllWebhookConfigs()
	if err != nil {
		return fmt.Errorf("failed to load webhook configs: %v", err)
	}

	wm.configs = configs
	wm.logger.Infof("Loaded %d webhook configurations", len(configs))

	// Debug logging
	for i, config := range configs {
		wm.logger.Infof("Webhook %d: ID=%d, Name=%s, Triggers=%d", i, config.ID, config.Name, len(config.Triggers))
		for j, trigger := range config.Triggers {
			wm.logger.Infof("  Trigger %d: type=%s, value=%s, match=%s, enabled=%t",
				j, trigger.TriggerType, trigger.TriggerValue, trigger.MatchType, trigger.Enabled)
		}
	}

	return nil
}

// GetWebhookConfigs returns a copy of current webhook configurations
func (wm *Manager) GetWebhookConfigs() []*types.WebhookConfig {
	wm.mutex.RLock()
	defer wm.mutex.RUnlock()

	// Return a copy to avoid race conditions
	configs := make([]*types.WebhookConfig, len(wm.configs))
	copy(configs, wm.configs)
	return configs
}

// MatchesTriggers checks if a message matches any webhook triggers
func (wm *Manager) MatchesTriggers(msg *events.Message, chatName string) []*types.WebhookConfig {
	wm.mutex.RLock()
	defer wm.mutex.RUnlock()

	var matchedConfigs []*types.WebhookConfig

	// Extract message content
	content := whatsapp.ExtractTextContent(msg.Message)
	mediaType := whatsapp.ExtractMediaInfo(msg.Message, msg.Info.ID).MediaType

	for _, config := range wm.configs {
		if !config.Enabled {
			continue
		}

		matched := false
		for _, trigger := range config.Triggers {
			if !trigger.Enabled {
				continue
			}

			if wm.matchesTrigger(trigger, msg, content, mediaType, chatName) {
				matched = true
				break
			}
		}

		if matched {
			matchedConfigs = append(matchedConfigs, config)
		}
	}

	return matchedConfigs
}

// matchesTrigger checks if a single trigger matches the message
func (wm *Manager) matchesTrigger(trigger types.WebhookTrigger, msg *events.Message, content, mediaType, chatName string) bool {
	switch trigger.TriggerType {
	case "all":
		return true

	case "chat_jid":
		return wm.matchesString(msg.Info.Chat.String(), trigger.TriggerValue, trigger.MatchType)

	case "sender":
		senderJID := msg.Info.Sender.String()
		senderUser := msg.Info.Sender.User
		return wm.matchesString(senderJID, trigger.TriggerValue, trigger.MatchType) ||
			wm.matchesString(senderUser, trigger.TriggerValue, trigger.MatchType)

	case "keyword":
		return wm.matchesString(content, trigger.TriggerValue, trigger.MatchType)

	case "media_type":
		return wm.matchesString(mediaType, trigger.TriggerValue, trigger.MatchType)

	default:
		wm.logger.Warnf("Unknown trigger type: %s", trigger.TriggerType)
		return false
	}
}

// matchesString performs string matching based on match type
func (wm *Manager) matchesString(text, pattern, matchType string) bool {
	switch matchType {
	case "exact":
		return text == pattern

	case "contains":
		return strings.Contains(strings.ToLower(text), strings.ToLower(pattern))

	case "regex":
		matched, err := regexp.MatchString(pattern, text)
		if err != nil {
			wm.logger.Warnf("Invalid regex pattern '%s': %v", pattern, err)
			return false
		}
		return matched

	default:
		wm.logger.Warnf("Unknown match type: %s", matchType)
		return false
	}
}

// ProcessMessage processes a message and sends webhooks if triggers match
func (wm *Manager) ProcessMessage(client interface{}, msg *events.Message, chatName string, pollInfo *types.PollInfo, pollVoteInfo *types.PollVoteInfo) {
	startTime := time.Now()

	// Find matching webhook configurations
	matchedConfigs := wm.MatchesTriggers(msg, chatName)
	if len(matchedConfigs) == 0 {
		return
	}

	wm.logger.Infof("Found %d matching webhook configs for message %s", len(matchedConfigs), msg.Info.ID)

	// Extract message content and media info
	content := whatsapp.ExtractTextContent(msg.Message)
	media := whatsapp.ExtractMediaInfo(msg.Message, msg.Info.ID)
	mediaType, filename := media.MediaType, media.Filename

	// Capture the media for any matched listener that asks for it. This runs on the
	// whatsmeow event path, so it only ever queues (§4.5) — never downloads.
	wm.enqueueAutoDownloads(msg, media, matchedConfigs)

	// Determine sender name
	senderName := msg.Info.Sender.User
	// Note: We'll need to handle contact lookup when integrating with the client

	// Determine event type
	eventType := "message_received"
	if mediaType == "poll" {
		eventType = "poll_created"
	} else if mediaType == "poll_vote" {
		eventType = "poll_vote"
	}

	// Build base payload
	basePayload := types.WebhookPayload{
		EventType: eventType,
		Timestamp: msg.Info.Timestamp.Format(time.RFC3339),
		Message: types.WebhookMessageInfo{
			ID:           msg.Info.ID,
			ChatJID:      msg.Info.Chat.String(),
			ChatName:     chatName,
			Sender:       msg.Info.Sender.String(),
			SenderName:   senderName,
			Content:      content,
			Timestamp:    msg.Info.Timestamp.Format(time.RFC3339),
			PushName:     msg.Info.PushName,
			IsFromMe:     msg.Info.IsFromMe,
			MediaType:    mediaType,
			Filename:     filename,
			IsPTT:        media.IsPTT,
			Seconds:      media.Seconds,
			PollInfo:     pollInfo,
			PollVoteInfo: pollVoteInfo,
		},
		Metadata: types.WebhookMetadata{
			ProcessingTimeMs: time.Since(startTime).Milliseconds(),
		},
	}

	// Add media download URL if it's a media message (not polls)
	if mediaType != "" && mediaType != "poll" && mediaType != "poll_vote" {
		basePayload.Message.MediaDownloadURL = wm.publicBaseURL + "/api/download"
	}

	// Add group info if it's a group chat
	if msg.Info.Chat.Server == "g.us" {
		basePayload.Metadata.GroupInfo = &types.GroupInfo{
			IsGroup:   true,
			GroupName: chatName,
			// ParticipantCount would require additional API call
		}
	}

	// Send webhooks for each matched configuration
	for _, config := range matchedConfigs {
		// A listener with auto-download on and no URL captures the media and posts nowhere.
		if config.WebhookURL == "" {
			continue
		}

		// Find the specific trigger that matched
		var matchedTrigger *types.WebhookTrigger
		content := whatsapp.ExtractTextContent(msg.Message)
		mediaType := whatsapp.ExtractMediaInfo(msg.Message, msg.Info.ID).MediaType

		for _, trigger := range config.Triggers {
			if trigger.Enabled && wm.matchesTrigger(trigger, msg, content, mediaType, chatName) {
				matchedTrigger = &trigger
				break
			}
		}

		if matchedTrigger == nil {
			continue
		}

		// Customize payload for this webhook
		payload := basePayload
		payload.WebhookConfig = types.WebhookConfigInfo{
			ID:   config.ID,
			Name: config.Name,
		}
		payload.Trigger = types.WebhookTriggerInfo{
			Type:      matchedTrigger.TriggerType,
			Value:     matchedTrigger.TriggerValue,
			MatchType: matchedTrigger.MatchType,
		}
		payload.Metadata.DeliveryAttempt = 1

		// Send webhook asynchronously
		go wm.delivery.DeliverWebhook(config, &payload, msg.Info.ID, msg.Info.Chat.String(), matchedTrigger)
	}
}

// enqueueAutoDownloads queues the matched message's media for every listener that asked for
// it. The triggers decide who is captured; these flags decide what:
//
//	audio ∧ (¬voiceOnly ∨ is_ptt) ∧ (url ∨ direct_path present)
//
// so a sender or keyword trigger that also matches text messages costs nothing here, and
// view-once audio — deliberately stored without url, key or direct path (§2.11) — is never
// queued for a download that could only fail.
func (wm *Manager) enqueueAutoDownloads(msg *events.Message, media types.MediaMetadata, matched []*types.WebhookConfig) {
	wm.mutex.RLock()
	downloader := wm.autoDownloader
	wm.mutex.RUnlock()

	if downloader == nil || media.MediaType != "audio" {
		return
	}
	if media.URL == "" && media.DirectPath == "" {
		return
	}

	for _, config := range matched {
		if !config.AutoDownload {
			continue
		}
		if config.VoiceOnly() && !media.IsPTT {
			continue
		}

		// One job per message: the first listener that wants it queues it, and the pool
		// drops a duplicate anyway. Attribution is that listener.
		downloader.Enqueue(autodownload.Job{
			MessageID: msg.Info.ID,
			ChatJID:   msg.Info.Chat.String(),
			RuleID:    config.ID,
			RuleName:  config.Name,
		})
		return
	}
}
