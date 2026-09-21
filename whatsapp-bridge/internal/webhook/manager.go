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
	storeDir       string

	// triggerAliases holds the other JIDs each exact sender/chat_jid trigger should also
	// match, keyed by trigger ID. See buildTriggerAliases.
	triggerAliases map[int][]string
}

// NewManager creates a new webhook manager. publicBaseURL is the address webhook
// consumers can reach this bridge on; it appears in media_download_url.
// storeDir is where whatsapp.db lives, used to expand phone numbers to the LID JIDs the
// same contact actually writes from.
func NewManager(messageStore *database.MessageStore, logger waLog.Logger, publicBaseURL, storeDir string) *Manager {
	if publicBaseURL == "" {
		publicBaseURL = "http://localhost:8080"
	}
	return &Manager{
		messageStore:   messageStore,
		logger:         logger,
		configs:        make([]*types.WebhookConfig, 0),
		delivery:       NewDeliveryService(messageStore, logger),
		publicBaseURL:  strings.TrimRight(publicBaseURL, "/"),
		storeDir:       storeDir,
		triggerAliases: make(map[int][]string),
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
	wm.triggerAliases = wm.buildTriggerAliases(configs)
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

// configMatch is one listener that a message fired, with the triggers that made it fire.
type configMatch struct {
	config   *types.WebhookConfig
	triggers []types.WebhookTrigger
}

// MatchesTriggers returns the enabled listeners the message fires.
func (wm *Manager) MatchesTriggers(msg *events.Message, chatName string) []*types.WebhookConfig {
	var configs []*types.WebhookConfig
	for _, m := range wm.matchConfigs(msg, chatName) {
		configs = append(configs, m.config)
	}
	return configs
}

// matchConfigs returns every enabled listener the message fires, each with its matched
// triggers.
func (wm *Manager) matchConfigs(msg *events.Message, chatName string) []configMatch {
	wm.mutex.RLock()
	defer wm.mutex.RUnlock()

	content := whatsapp.ExtractTextContent(msg.Message)
	mediaType := whatsapp.ExtractMediaInfo(msg.Message, msg.Info.ID).MediaType

	var matches []configMatch
	for _, config := range wm.configs {
		if !config.Enabled {
			continue
		}
		if triggers := wm.matchingTriggers(config, msg, content, mediaType, chatName); len(triggers) > 0 {
			matches = append(matches, configMatch{config: config, triggers: triggers})
		}
	}
	return matches
}

// matchingTriggers is the one place a listener's match decision is made (ADR 0001). It
// returns the enabled triggers that made the listener fire, in trigger order, or nothing
// when it does not fire:
//
//   - "or": the first enabled trigger that matches — one entry, as before match modes.
//   - "and": every enabled trigger, and only when every one of them matches. "all" is
//     always true, so it adds no requirement.
//
// Disabled triggers are skipped in both modes, so a listener with no enabled triggers
// matches nothing. The payload's trigger and matched_triggers come from this same list, so
// they can never name a trigger the decision did not use.
func (wm *Manager) matchingTriggers(config *types.WebhookConfig, msg *events.Message, content, mediaType, chatName string) []types.WebhookTrigger {
	and := config.Mode() == types.TriggerModeAnd

	var matched []types.WebhookTrigger
	for _, trigger := range config.Triggers {
		if !trigger.Enabled {
			continue
		}
		if !wm.matchesTrigger(trigger, msg, content, mediaType, chatName) {
			if and {
				return nil
			}
			continue
		}
		matched = append(matched, trigger)
		if !and {
			break
		}
	}
	return matched
}

// matchesTrigger checks if a single trigger matches the message
func (wm *Manager) matchesTrigger(trigger types.WebhookTrigger, msg *events.Message, content, mediaType, chatName string) bool {
	switch trigger.TriggerType {
	case "all":
		return true

	case "chat_jid":
		if wm.matchesString(msg.Info.Chat.String(), trigger.TriggerValue, trigger.MatchType) {
			return true
		}
		return wm.matchesAlias(trigger, msg.Info.Chat.String())

	case "sender":
		senderJID := msg.Info.Sender.String()
		senderUser := msg.Info.Sender.User
		if wm.matchesString(senderJID, trigger.TriggerValue, trigger.MatchType) ||
			wm.matchesString(senderUser, trigger.TriggerValue, trigger.MatchType) {
			return true
		}
		return wm.matchesAlias(trigger, senderJID, senderUser)

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
	matches := wm.matchConfigs(msg, chatName)
	if len(matches) == 0 {
		return
	}

	wm.logger.Infof("Found %d matching webhook configs for message %s", len(matches), msg.Info.ID)

	matchedConfigs := make([]*types.WebhookConfig, len(matches))
	for i, m := range matches {
		matchedConfigs[i] = m.config
	}

	// Extract message content and media info
	content := whatsapp.ExtractTextContent(msg.Message)
	media := whatsapp.ExtractMediaInfo(msg.Message, msg.Info.ID)
	mediaType, filename := media.MediaType, media.Filename

	// Capture the media for any matched listener that asks for it. This runs on the
	// whatsmeow event path, so it only ever queues (§4.5) — never downloads.
	wm.enqueueAutoDownloads(msg, media, chatName, matchedConfigs)

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
	for _, m := range matches {
		config := m.config
		// A listener with auto-download on and no URL captures the media and posts nowhere.
		if config.WebhookURL == "" {
			continue
		}

		// The first matched trigger is the one n8n reads; matched_triggers has them all.
		matchedTrigger := m.triggers[0]
		matchedInfo := make([]types.WebhookTriggerInfo, len(m.triggers))
		for i, trigger := range m.triggers {
			matchedInfo[i] = triggerInfo(trigger)
		}

		// Customize payload for this webhook
		payload := basePayload
		payload.WebhookConfig = types.WebhookConfigInfo{
			ID:   config.ID,
			Name: config.Name,
		}
		payload.Trigger = matchedInfo[0]
		payload.MatchedTriggers = matchedInfo
		payload.TriggerMode = config.Mode()
		payload.Metadata.DeliveryAttempt = 1

		// Send webhook asynchronously
		go wm.delivery.DeliverWebhook(config, &payload, msg.Info.ID, msg.Info.Chat.String(), &matchedTrigger)
	}
}

func triggerInfo(trigger types.WebhookTrigger) types.WebhookTriggerInfo {
	return types.WebhookTriggerInfo{
		Type:      trigger.TriggerType,
		Value:     trigger.TriggerValue,
		MatchType: trigger.MatchType,
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
func (wm *Manager) enqueueAutoDownloads(msg *events.Message, media types.MediaMetadata, chatName string, matched []*types.WebhookConfig) {
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
		// drops a duplicate anyway. Attribution is that listener and the trigger that fired.
		job := autodownload.Job{
			MessageID: msg.Info.ID,
			ChatJID:   msg.Info.Chat.String(),
			RuleID:    config.ID,
			RuleName:  config.Name,
		}
		if trigger := wm.firstMatchingTrigger(config, msg, chatName, media.MediaType); trigger != nil {
			job.TriggerType, job.TriggerValue = trigger.TriggerType, trigger.TriggerValue
		}
		downloader.Enqueue(job)
		return
	}
}

// buildTriggerAliases resolves each exact sender/chat_jid trigger to the other JIDs that
// belong to the same conversation, so a rule typed as a phone number still matches.
//
// This closes B11. WhatsApp moved 1:1 chats to LID addressing (§3.2 finding 6): a contact
// saved as 972547669192 now writes from 62156783493249@lid, and matchesTrigger compares
// raw strings. Without this, a rule entered as a phone number looks correct in the UI and
// matches nothing, silently and forever. ResolveChatJIDs already does this expansion for
// downloads; here it runs once per load rather than per message.
//
// Only exact triggers are expanded: extending a substring or a regex to other values would
// change what the user wrote into something they did not.
func (wm *Manager) buildTriggerAliases(configs []*types.WebhookConfig) map[int][]string {
	aliases := make(map[int][]string)

	for _, config := range configs {
		for _, trigger := range config.Triggers {
			if trigger.MatchType != "exact" || trigger.TriggerValue == "" {
				continue
			}
			if trigger.TriggerType != "sender" && trigger.TriggerType != "chat_jid" {
				continue
			}

			jids, err := whatsapp.ResolveChatJIDs(trigger.TriggerValue, wm.storeDir)
			if err != nil {
				wm.logger.Warnf("Could not expand trigger %d (%s=%s): %v",
					trigger.ID, trigger.TriggerType, trigger.TriggerValue, err)
				continue
			}

			var extra []string
			for _, jid := range jids {
				if jid == trigger.TriggerValue {
					continue
				}
				extra = append(extra, jid)
				// A sender is matched by bare user as well as by full JID.
				if user, _, ok := strings.Cut(jid, "@"); ok && user != trigger.TriggerValue {
					extra = append(extra, user)
				}
			}
			if len(extra) > 0 {
				aliases[trigger.ID] = extra
				wm.logger.Infof("Trigger %d (%s=%s) also matches %v",
					trigger.ID, trigger.TriggerType, trigger.TriggerValue, extra)
			}
		}
	}

	return aliases
}

// matchesAlias reports whether any of the message's identifiers equals one of the trigger's
// resolved alternatives. Exact comparison only, by construction of buildTriggerAliases.
func (wm *Manager) matchesAlias(trigger types.WebhookTrigger, candidates ...string) bool {
	if trigger.MatchType != "exact" {
		return false
	}
	for _, alias := range wm.triggerAliases[trigger.ID] {
		for _, candidate := range candidates {
			if candidate == alias {
				return true
			}
		}
	}
	return false
}

// firstMatchingTrigger returns the trigger that made this config match, for attribution in
// logs. It repeats the matching the caller already did, which is cheap and keeps the match
// decision in exactly one place: matchingTriggers.
func (wm *Manager) firstMatchingTrigger(config *types.WebhookConfig, msg *events.Message, chatName, mediaType string) *types.WebhookTrigger {
	content := whatsapp.ExtractTextContent(msg.Message)
	triggers := wm.matchingTriggers(config, msg, content, mediaType, chatName)
	if len(triggers) == 0 {
		return nil
	}
	return &triggers[0]
}
