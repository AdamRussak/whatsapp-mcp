package types

import (
	"time"
)

// Message represents a chat message for our client
type Message struct {
	Time      time.Time
	Sender    string
	Content   string
	IsFromMe  bool
	MediaType string
	Filename  string
}

// MediaMetadata describes the media attached to a message, in the shape messages.db
// stores it. Download state (local path, status) is not here: it lives in media.db.
type MediaMetadata struct {
	MediaType     string
	Filename      string
	URL           string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64

	// Added by schema v1, captured from the protobuf for audio messages
	DirectPath string
	Mimetype   string
	Seconds    uint32
	IsPTT      bool // true for a voice note, false for an attached audio file
}

// WebhookConfig represents a webhook configuration.
//
// A config is a listener: its triggers decide which messages it matches, and its actions
// decide what happens to them. Delivering an HTTP webhook is one action; auto-downloading
// the matched media is another (schema v2). A config may do either or both, so WebhookURL
// is optional when AutoDownload is on.
type WebhookConfig struct {
	ID          int              `json:"id"`
	Name        string           `json:"name"`
	WebhookURL  string           `json:"webhook_url"`
	SecretToken string           `json:"secret_token"`
	Enabled     bool             `json:"enabled"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Triggers    []WebhookTrigger `json:"triggers"`

	// AutoDownload makes a matched audio message download as it arrives.
	AutoDownload bool `json:"auto_download"`
	// AutoDownloadVoiceOnly restricts that to voice notes (is_ptt). It is a pointer so an
	// absent JSON field means "true" — the column default — rather than "download all audio".
	AutoDownloadVoiceOnly *bool `json:"auto_download_voice_only"`
}

// VoiceOnly reports whether auto-download is restricted to voice notes, defaulting to true
// when the field was never set.
func (c *WebhookConfig) VoiceOnly() bool {
	return c.AutoDownloadVoiceOnly == nil || *c.AutoDownloadVoiceOnly
}

// WebhookTrigger represents a trigger condition for webhooks
type WebhookTrigger struct {
	ID              int    `json:"id"`
	WebhookConfigID int    `json:"webhook_config_id"`
	TriggerType     string `json:"trigger_type"` // chat_jid, sender, keyword, media_type, all
	TriggerValue    string `json:"trigger_value"`
	MatchType       string `json:"match_type"` // exact, contains, regex
	Enabled         bool   `json:"enabled"`
}

// WebhookPayload represents the standardized payload structure for webhook notifications
type WebhookPayload struct {
	EventType     string             `json:"event_type"`
	Timestamp     string             `json:"timestamp"`
	WebhookConfig WebhookConfigInfo  `json:"webhook_config"`
	Trigger       WebhookTriggerInfo `json:"trigger"`
	Message       WebhookMessageInfo `json:"message"`
	Metadata      WebhookMetadata    `json:"metadata"`
}

type WebhookConfigInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type WebhookTriggerInfo struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	MatchType string `json:"match_type"`
}

type WebhookMessageInfo struct {
	ID               string        `json:"id"`
	ChatJID          string        `json:"chat_jid"`
	ChatName         string        `json:"chat_name"`
	Sender           string        `json:"sender"`
	SenderName       string        `json:"sender_name"`
	Content          string        `json:"content"`
	Timestamp        string        `json:"timestamp"`
	PushName         string        `json:"push_name,omitempty"`
	IsFromMe         bool          `json:"is_from_me"`
	MediaType        string        `json:"media_type"`
	Filename         string        `json:"filename"`
	IsPTT            bool          `json:"is_ptt,omitempty"`  // voice note rather than an audio file
	Seconds          uint32        `json:"seconds,omitempty"` // audio duration
	MediaDownloadURL string        `json:"media_download_url"`
	PollInfo         *PollInfo     `json:"poll_info,omitempty"`
	PollVoteInfo     *PollVoteInfo `json:"poll_vote_info,omitempty"`
}

// PollInfo represents a poll creation message
type PollInfo struct {
	Question        string       `json:"question"`
	Options         []PollOption `json:"options"`
	SelectableCount uint32       `json:"selectable_count"`
}

// PollOption represents a single option in a poll
type PollOption struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"` // hex-encoded hash for vote matching
}

// PollVoteInfo represents a poll vote message
type PollVoteInfo struct {
	Voter             string   `json:"voter"`
	SelectedOptions   []string `json:"selected_options"` // option names (if decryptable)
	SelectedHashes    []string `json:"selected_hashes"`  // hex SHA256 hashes
	CreationMessageID string   `json:"creation_message_id"`
}

type WebhookMetadata struct {
	GroupInfo        *GroupInfo `json:"group_info,omitempty"`
	DeliveryAttempt  int        `json:"delivery_attempt"`
	ProcessingTimeMs int64      `json:"processing_time_ms"`
}

type GroupInfo struct {
	IsGroup          bool   `json:"is_group"`
	GroupName        string `json:"group_name"`
	ParticipantCount int    `json:"participant_count"`
}

// WebhookLog represents a webhook delivery log entry
type WebhookLog struct {
	ID              int        `json:"id"`
	WebhookConfigID int        `json:"webhook_config_id"`
	MessageID       string     `json:"message_id"`
	ChatJID         string     `json:"chat_jid"`
	TriggerType     string     `json:"trigger_type"`
	TriggerValue    string     `json:"trigger_value"`
	Payload         string     `json:"payload"`
	ResponseStatus  int        `json:"response_status"`
	ResponseBody    string     `json:"response_body"`
	AttemptCount    int        `json:"attempt_count"`
	DeliveredAt     *time.Time `json:"delivered_at"`
	CreatedAt       time.Time  `json:"created_at"`
	// EventType is "delivery" for a webhook POST, "auto_download" for a capture. Both are
	// actions of the same listener, so they share one log.
	EventType string `json:"event_type"`
}

// SendMessageRequest represents the request body for the send message API
type SendMessageRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	MediaPath string `json:"media_path,omitempty"`
}

// SendMessageResponse represents the response for the send message API
type SendMessageResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
