package database

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"whatsapp-bridge/internal/types"
)

// StoreChat stores a chat in the database
func (store *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := store.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime,
	)
	return err
}

// StoreMessage stores a message in the database.
//
// An existing row is never rewritten (§4.3): a history-sync replay of a message we
// already hold only fills in schema-v1 metadata columns that are still NULL. Content,
// timestamp, sender, filename, url and the decryption material stay as first stored.
func (store *MessageStore) StoreMessage(id, chatJID, sender, content string, timestamp time.Time, isFromMe bool,
	media types.MediaMetadata) error {
	// Only store if there's actual content or media
	if content == "" && media.MediaType == "" {
		return nil
	}

	// Empty means "unknown" for the v1 columns, so store NULL and let a later replay that
	// does carry the value fill it in. is_ptt is meaningful only for audio.
	var isPTT interface{}
	if media.MediaType == "audio" {
		isPTT = media.IsPTT
	}

	_, err := store.db.Exec(
		`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length,
		 direct_path, mimetype, seconds, is_ptt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, chat_jid) DO UPDATE SET
			direct_path = COALESCE(messages.direct_path, excluded.direct_path),
			mimetype    = COALESCE(messages.mimetype,    excluded.mimetype),
			seconds     = COALESCE(messages.seconds,     excluded.seconds),
			is_ptt      = COALESCE(messages.is_ptt,      excluded.is_ptt)`,
		id, chatJID, sender, content, timestamp, isFromMe, media.MediaType, media.Filename, media.URL,
		media.MediaKey, media.FileSHA256, media.FileEncSHA256, media.FileLength,
		nullIfEmpty(media.DirectPath), nullIfEmpty(media.Mimetype), nullIfZero(media.Seconds), isPTT,
	)
	return err
}

// nullIfEmpty stores an absent string as NULL, so COALESCE on a later replay can fill it.
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullIfZero stores an absent duration as NULL rather than a misleading 0.
func nullIfZero(v uint32) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// GetMessages gets messages from a chat
func (store *MessageStore) GetMessages(chatJID string, limit int) ([]types.Message, error) {
	rows, err := store.db.Query(
		"SELECT sender, content, timestamp, is_from_me, media_type, filename FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT ?",
		chatJID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []types.Message
	for rows.Next() {
		var msg types.Message
		var timestamp time.Time
		err := rows.Scan(&msg.Sender, &msg.Content, &timestamp, &msg.IsFromMe, &msg.MediaType, &msg.Filename)
		if err != nil {
			return nil, err
		}
		msg.Time = timestamp
		messages = append(messages, msg)
	}

	return messages, nil
}

// GetChats gets all chats
func (store *MessageStore) GetChats() (map[string]time.Time, error) {
	rows, err := store.db.Query("SELECT jid, last_message_time FROM chats ORDER BY last_message_time DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make(map[string]time.Time)
	for rows.Next() {
		var jid string
		var lastMessageTime time.Time
		err := rows.Scan(&jid, &lastMessageTime)
		if err != nil {
			return nil, err
		}
		chats[jid] = lastMessageTime
	}

	return chats, nil
}

// StorePollOptions stores the options for a poll
func (store *MessageStore) StorePollOptions(pollID string, options []types.PollOption) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR REPLACE INTO poll_options (poll_id, option_name, option_hash) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, opt := range options {
		_, err = stmt.Exec(pollID, opt.Name, opt.SHA256)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetPollOptionName retrieves the text name for a poll option hash
func (store *MessageStore) GetPollOptionName(pollID, optionHash string) (string, error) {
	var name string
	err := store.db.QueryRow("SELECT option_name FROM poll_options WHERE poll_id = ? AND option_hash = ?", pollID, optionHash).Scan(&name)
	return name, err
}

// MediaInfo holds metadata for a media message in messages.db
type MediaInfo struct {
	ID            string    `json:"id"`
	ChatJID       string    `json:"chat_jid"`
	Sender        string    `json:"sender"`
	Content       string    `json:"content"`
	Timestamp     time.Time `json:"timestamp"`
	IsFromMe      bool      `json:"is_from_me"`
	MediaType     string    `json:"media_type"`
	Filename      string    `json:"filename"`
	URL           string    `json:"url"`
	MediaKey      []byte    `json:"-"`
	FileSHA256    []byte    `json:"-"`
	FileEncSHA256 []byte    `json:"-"`
	FileLength    uint64    `json:"file_length"`

	// DirectPath is captured from the protobuf since schema v1 and carries the CDN query
	// string (ccb/oh/oe/_nc_sid). NULL for rows stored before v1.
	DirectPath string `json:"direct_path,omitempty"`

	// Mimetype is captured since schema v1: for audio, and since ADR 0002 for image, video
	// and document. It names the extension of a downloaded image or video. NULL for older rows.
	Mimetype string `json:"mimetype,omitempty"`
}

// GetMediaInfo retrieves media information for a specific message by ID and chat JID (read-only)
func (store *MessageStore) GetMediaInfo(id, chatJID string) (*MediaInfo, error) {
	row := store.db.QueryRow(`
		SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url,
		       media_key, file_sha256, file_enc_sha256, file_length, direct_path, mimetype
		FROM messages
		WHERE id = ? AND chat_jid = ?
	`, id, chatJID)

	var info MediaInfo
	var sender, content, filename, url, directPath, mimetype sql.NullString
	var fileLength sql.NullInt64

	err := row.Scan(
		&info.ID, &info.ChatJID, &sender, &content, &info.Timestamp, &info.IsFromMe,
		&info.MediaType, &filename, &url, &info.MediaKey, &info.FileSHA256, &info.FileEncSHA256, &fileLength,
		&directPath, &mimetype,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	info.Sender = sender.String
	info.Content = content.String
	info.Filename = filename.String
	info.URL = url.String
	info.DirectPath = directPath.String
	info.Mimetype = mimetype.String
	if fileLength.Valid && fileLength.Int64 > 0 {
		info.FileLength = uint64(fileLength.Int64)
	}

	return &info, nil
}

// storedTimestampLayout is how go-sqlite3 writes messages.timestamp (always UTC in this DB)
const storedTimestampLayout = "2006-01-02 15:04:05-07:00"

// timeFilterLayouts are the accepted formats for after/before filters
var timeFilterLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// NormalizeTimeFilter converts an after/before filter to the stored timestamp format, so the
// text comparison in SQL orders correctly ("2026-01-01T00:00:00Z" would otherwise sort after
// every stored "2026-01-01 ..." value). Inputs without a zone are taken as UTC. Empty stays empty.
func NormalizeTimeFilter(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	for _, layout := range timeFilterLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(storedTimestampLayout), nil
		}
	}
	return "", fmt.Errorf("unrecognized time %q (use RFC 3339, \"YYYY-MM-DD HH:MM:SS\" or YYYY-MM-DD)", s)
}

// ListAudioMessages lists audio messages across the given chat JIDs (read-only).
//
// voiceOnly narrows the result to voice notes (the virtual "voice" media type: audio with
// is_ptt set). Only messages received since schema v1 carry is_ptt, so older history is
// excluded by that filter even where it is in fact voice notes.
func (store *MessageStore) ListAudioMessages(chatJIDs []string, after, before string, limit int, voiceOnly bool) ([]MediaInfo, error) {
	if len(chatJIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url,
		       media_key, file_sha256, file_enc_sha256, file_length
		FROM messages
		WHERE media_type = 'audio' AND chat_jid IN (`

	var placeholders []string
	var args []interface{}
	for _, jid := range chatJIDs {
		placeholders = append(placeholders, "?")
		args = append(args, jid)
	}
	query += strings.Join(placeholders, ",") + ")"

	if voiceOnly {
		query += " AND is_ptt = 1"
	}

	after, err := NormalizeTimeFilter(after)
	if err != nil {
		return nil, fmt.Errorf("invalid after: %w", err)
	}
	before, err = NormalizeTimeFilter(before)
	if err != nil {
		return nil, fmt.Errorf("invalid before: %w", err)
	}
	if after != "" {
		query += " AND timestamp > ?"
		args = append(args, after)
	}
	if before != "" {
		query += " AND timestamp < ?"
		args = append(args, before)
	}

	query += " ORDER BY timestamp DESC, id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := store.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MediaInfo
	for rows.Next() {
		var info MediaInfo
		var sender, content, filename, url sql.NullString
		var fileLength sql.NullInt64

		err := rows.Scan(
			&info.ID, &info.ChatJID, &sender, &content, &info.Timestamp, &info.IsFromMe,
			&info.MediaType, &filename, &url, &info.MediaKey, &info.FileSHA256, &info.FileEncSHA256, &fileLength,
		)
		if err != nil {
			return nil, err
		}

		info.Sender = sender.String
		info.Content = content.String
		info.Filename = filename.String
		info.URL = url.String
		if fileLength.Valid && fileLength.Int64 > 0 {
			info.FileLength = uint64(fileLength.Int64)
		}

		result = append(result, info)
	}

	return result, rows.Err()
}

// ChatSummary is one row of the chat list the UI's picker offers. It carries only what the
// picker shows and writes into a trigger: never message content.
type ChatSummary struct {
	JID             string `json:"jid"`
	Name            string `json:"name"`
	LastMessageTime string `json:"last_message_time"`
	IsGroup         bool   `json:"is_group"`
	// Phone is the other party's number for a 1:1 chat, when WhatsApp's LID map knows it.
	// Filled at read time by the API, not stored.
	Phone string `json:"phone,omitempty"`
}

// Chat kinds accepted by ListChats.
const (
	ChatKindAll     = "all"
	ChatKindContact = "contact"
	ChatKindGroup   = "group"
)

// ListChats returns chats for the picker, newest conversation first.
//
// query is an optional case-insensitive substring of the name or the JID; kind narrows to
// 1:1 chats or groups. Read-only, with every column named, per §4.2.
//
// Note on names: chats.name falls back to the JID's user part when a contact never
// resolved, so a row can legitimately read as a bare number. That is what the chat is
// called here; inventing a friendlier label would be a guess.
func (store *MessageStore) ListChats(query, kind string, limit int) ([]ChatSummary, error) {
	sqlQuery := `SELECT jid, name, last_message_time FROM chats WHERE 1 = 1`
	var args []interface{}

	switch kind {
	case ChatKindContact:
		sqlQuery += ` AND jid NOT LIKE '%@g.us' AND jid NOT LIKE '%@broadcast'`
	case ChatKindGroup:
		sqlQuery += ` AND jid LIKE '%@g.us'`
	}

	if query != "" {
		// LIKE is case-insensitive for ASCII in SQLite; LOWER covers the rest well enough
		// for a picker, and both sides are parameterised.
		sqlQuery += ` AND (LOWER(name) LIKE ? OR LOWER(jid) LIKE ?)`
		pattern := "%" + strings.ToLower(query) + "%"
		args = append(args, pattern, pattern)
	}

	sqlQuery += ` ORDER BY last_message_time DESC LIMIT ?`
	args = append(args, limit)

	rows, err := store.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make([]ChatSummary, 0, limit)
	for rows.Next() {
		var chat ChatSummary
		var name, lastMessageTime sql.NullString
		if err := rows.Scan(&chat.JID, &name, &lastMessageTime); err != nil {
			return nil, err
		}
		chat.Name = name.String
		chat.LastMessageTime = lastMessageTime.String
		chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")
		chats = append(chats, chat)
	}
	return chats, rows.Err()
}

// DownloadMetadata is what the downloads view shows about the message behind a file.
type DownloadMetadata struct {
	Sender    string
	Timestamp time.Time
	IsFromMe  bool
	Seconds   int
	IsPTT     bool
	Mimetype  string
}

// metadataBatch keeps each IN (...) list well under SQLite's bound-parameter limit.
const metadataBatch = 500

// DownloadKey identifies one message in one chat; the same message ID can exist under a
// contact's phone JID and its LID JID.
func DownloadKey(messageID, chatJID string) string {
	return chatJID + "/" + messageID
}

// GetDownloadMetadata looks up the messages behind a set of downloads, keyed by
// DownloadKey. Read-only: download bookkeeping never writes to messages.db (§5.1).
//
// It selects by message ID and matches the chat in Go, because the pair is what identifies
// a message and a plain IN list is portable across SQLite versions.
func (store *MessageStore) GetDownloadMetadata(keys map[string][]string) (map[string]DownloadMetadata, error) {
	// keys: message ID -> the chat JIDs it is wanted under
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}

	out := make(map[string]DownloadMetadata, len(ids))
	for start := 0; start < len(ids); start += metadataBatch {
		end := start + metadataBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}

		rows, err := store.db.Query(`SELECT id, chat_jid, sender, timestamp, is_from_me, seconds, is_ptt, mimetype
			FROM messages WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var id, chatJID string
			var sender, mimetype sql.NullString
			var timestamp time.Time
			var isFromMe sql.NullBool
			var seconds sql.NullInt64
			var isPTT sql.NullBool
			if err := rows.Scan(&id, &chatJID, &sender, &timestamp, &isFromMe, &seconds, &isPTT, &mimetype); err != nil {
				rows.Close()
				return nil, err
			}
			wanted := false
			for _, jid := range keys[id] {
				if jid == chatJID {
					wanted = true
					break
				}
			}
			if !wanted {
				continue
			}
			out[DownloadKey(id, chatJID)] = DownloadMetadata{
				Sender:    sender.String,
				Timestamp: timestamp,
				IsFromMe:  isFromMe.Bool,
				Seconds:   int(seconds.Int64),
				IsPTT:     isPTT.Bool,
				Mimetype:  mimetype.String,
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// GetChatNames returns the stored name for each chat JID that has one.
func (store *MessageStore) GetChatNames(jids []string) (map[string]string, error) {
	out := make(map[string]string, len(jids))
	for start := 0; start < len(jids); start += metadataBatch {
		end := start + metadataBatch
		if end > len(jids) {
			end = len(jids)
		}
		batch := jids[start:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for i, jid := range batch {
			placeholders[i] = "?"
			args[i] = jid
		}

		rows, err := store.db.Query(`SELECT jid, name FROM chats WHERE jid IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var jid string
			var name sql.NullString
			if err := rows.Scan(&jid, &name); err != nil {
				rows.Close()
				return nil, err
			}
			if name.String != "" {
				out[jid] = name.String
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ParseTimeFilter parses an after/before filter in any accepted layout. ok is false for an
// empty filter.
func ParseTimeFilter(s string) (t time.Time, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range timeFilterLayouts {
		if parsed, perr := time.Parse(layout, s); perr == nil {
			return parsed.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unrecognized time %q (use RFC 3339, \"YYYY-MM-DD HH:MM:SS\" or YYYY-MM-DD)", s)
}

// GetChatsByJIDs returns the chats among jids that exist, newest conversation first. The
// picker uses it for people found by name or number in WhatsApp's contact store, whose
// chats a text search over chats.jid and chats.name cannot reach.
func (store *MessageStore) GetChatsByJIDs(jids []string) ([]ChatSummary, error) {
	var chats []ChatSummary
	for start := 0; start < len(jids); start += metadataBatch {
		end := start + metadataBatch
		if end > len(jids) {
			end = len(jids)
		}
		batch := jids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for i, jid := range batch {
			placeholders[i] = "?"
			args[i] = jid
		}

		rows, err := store.db.Query(`SELECT jid, name, last_message_time FROM chats WHERE jid IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var chat ChatSummary
			var name, lastMessageTime sql.NullString
			if err := rows.Scan(&chat.JID, &name, &lastMessageTime); err != nil {
				rows.Close()
				return nil, err
			}
			chat.Name = name.String
			chat.LastMessageTime = lastMessageTime.String
			chat.IsGroup = strings.HasSuffix(chat.JID, "@g.us")
			chats = append(chats, chat)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(chats, func(i, j int) bool { return chats[i].LastMessageTime > chats[j].LastMessageTime })
	return chats, nil
}
